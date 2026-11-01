// Package forward pushes positions and events to external systems. It owns the
// queue, the retry and the reconnect loop, so a consumer that is down never
// blocks the device connection.
package forward

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// Queue, retry and timeout defaults, exported so the gateway can report them.
const (
	DefaultQueueSize   = 128
	DefaultMaxBackoff  = 30 * time.Second
	DefaultMaxAttempts = 5
	DefaultTimeout     = 15 * time.Second
)

// ErrQueueFull is returned by a forwarder that cannot accept another message.
var ErrQueueFull = errors.New("forward: queue is full")

// Message is one position or event together with what the transports need to
// render it.
type Message struct {
	Position *model.Position
	Event    *model.Event
	Device   *model.Device
	// EventPosition is the position an event happened at, when known.
	EventPosition *model.Position
}

// Transport delivers one message to one destination.
type Transport interface {
	// Name labels the transport in logs and metrics.
	Name() string
	// Send delivers one message. A failure makes the worker retry with a
	// backoff, so a transport can simply return the connection error.
	Send(ctx context.Context, message Message) error
	// Close releases a persistent connection.
	Close() error
}

// Config holds the queue and retry settings.
type Config struct {
	QueueSize   int
	MaxBackoff  time.Duration
	MaxAttempts int
}

// Service fans one message out to every transport, each with its own queue so a
// slow consumer does not delay the others.
type Service struct {
	ctx       context.Context
	cancel    context.CancelFunc
	workers   []*worker
	logf      func(format string, args ...any)
	dropped   atomic.Int64
	failed    atomic.Int64
	closeOnce sync.Once
	wg        sync.WaitGroup
}

type worker struct {
	transport Transport
	queue     chan Message
}

// New starts one background sender per transport.
func New(config Config, transports []Transport, options ...Option) *Service {
	if config.QueueSize <= 0 {
		config.QueueSize = DefaultQueueSize
	}
	if config.MaxBackoff <= 0 {
		config.MaxBackoff = DefaultMaxBackoff
	}
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = DefaultMaxAttempts
	}

	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		ctx:    ctx,
		cancel: cancel,
		logf:   func(string, ...any) {},
	}
	for _, option := range options {
		option(service)
	}

	for _, transport := range transports {
		if transport == nil {
			continue
		}
		sender := &worker{transport: transport, queue: make(chan Message, config.QueueSize)}
		service.workers = append(service.workers, sender)
		service.wg.Add(1)
		go service.run(sender, config)
	}
	return service
}

// Option customises a Service.
type Option func(*Service)

// WithLogger reports a dropped message or an exhausted retry.
func WithLogger(logf func(format string, args ...any)) Option {
	return func(s *Service) {
		if logf != nil {
			s.logf = logf
		}
	}
}

// ForwardPosition queues a position for every transport.
func (s *Service) ForwardPosition(position *model.Position, device *model.Device) {
	if s == nil || position == nil {
		return
	}
	s.enqueue(Message{Position: position, Device: device})
}

// ForwardEvent queues an event for every transport.
func (s *Service) ForwardEvent(event *model.Event, device *model.Device, position *model.Position) {
	if s == nil || event == nil {
		return
	}
	s.enqueue(Message{Event: event, Device: device, EventPosition: position})
}

func (s *Service) enqueue(message Message) {
	for _, sender := range s.workers {
		select {
		case sender.queue <- message:
		default:
			s.dropped.Add(1)
			s.logf("forward: %s queue full, dropped a message", sender.transport.Name())
		}
	}
}

// run drains one transport queue, retrying with an exponential backoff.
func (s *Service) run(sender *worker, config Config) {
	defer s.wg.Done()
	for message := range sender.queue {
		s.deliver(sender, message, config)
	}
	_ = sender.transport.Close()
}

func (s *Service) deliver(sender *worker, message Message, config Config) {
	backoff := 100 * time.Millisecond
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(s.ctx, DefaultTimeout)
		err := sender.transport.Send(ctx, message)
		cancel()
		if err == nil {
			return
		}
		if attempt >= config.MaxAttempts {
			s.failed.Add(1)
			s.logf("forward: %s gave up after %d attempts: %v",
				sender.transport.Name(), attempt, err)
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > config.MaxBackoff {
			backoff = config.MaxBackoff
		}
	}
}

// Dropped reports the messages discarded because a queue was full.
func (s *Service) Dropped() int64 { return s.dropped.Load() }

// Failed reports the messages a transport gave up on.
func (s *Service) Failed() int64 { return s.failed.Load() }

// Close stops accepting messages, drains the queues and closes the transports.
func (s *Service) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		for _, sender := range s.workers {
			close(sender.queue)
		}
	})

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		s.cancel()
		return nil
	case <-ctx.Done():
		s.cancel()
		return ctx.Err()
	}
}

// payload renders the JSON document a JSON/MQTT/Redis consumer reads. Field
// names follow the REST responses.
func payload(message Message) map[string]any {
	document := map[string]any{}
	if message.Position != nil {
		document["position"] = positionMap(*message.Position)
	}
	if message.Event != nil {
		event := map[string]any{
			"id":         message.Event.ID,
			"deviceId":   message.Event.DeviceID,
			"type":       message.Event.Type,
			"eventTime":  message.Event.Time.UTC().Format(time.RFC3339),
			"attributes": attributesOf(message.Event.Attributes),
		}
		if message.Event.PositionID != 0 {
			event["positionId"] = message.Event.PositionID
		}
		if message.Event.GeofenceID != 0 {
			event["geofenceId"] = message.Event.GeofenceID
		}
		document["event"] = event
		if message.EventPosition != nil {
			document["position"] = positionMap(*message.EventPosition)
		}
	}
	if message.Device != nil {
		document["device"] = map[string]any{
			"id":         message.Device.ID,
			"uniqueId":   message.Device.UniqueID,
			"name":       message.Device.Name,
			"model":      message.Device.Model,
			"protocol":   message.Device.Protocol,
			"groupId":    message.Device.GroupID,
			"status":     message.Device.Status,
			"lastUpdate": message.Device.LastUpdate.UTC().Format(time.RFC3339),
			"attributes": attributesOf(message.Device.Attributes),
		}
	}
	return document
}

func positionMap(position model.Position) map[string]any {
	return map[string]any{
		"id":         position.ID,
		"deviceId":   position.DeviceID,
		"protocol":   position.Protocol,
		"deviceTime": position.Time.UTC().Format(time.RFC3339),
		"serverTime": position.ServerTime.UTC().Format(time.RFC3339),
		"valid":      position.Valid,
		"latitude":   position.Latitude,
		"longitude":  position.Longitude,
		"altitude":   position.Altitude,
		"speed":      position.Speed,
		"course":     position.Course,
		"accuracy":   position.Accuracy,
		"attributes": attributesOf(position.Attributes),
	}
}

func attributesOf(attributes model.Attributes) map[string]any {
	if attributes == nil {
		return map[string]any{}
	}
	return map[string]any(attributes)
}

// Only restricts a transport to positions or to events. The server keeps
// separate position and event forwarders, so the same destination can be
// configured for one of the two feeds.
func Only(transport Transport, events bool) Transport {
	if transport == nil {
		return nil
	}
	return filtered{Transport: transport, events: events}
}

type filtered struct {
	Transport
	events bool
}

// Send drops the messages the transport was not configured for. Dropping is not
// a failure, so the worker does not retry.
func (f filtered) Send(ctx context.Context, message Message) error {
	if (message.Event != nil) != f.events {
		return nil
	}
	return f.Transport.Send(ctx, message)
}
