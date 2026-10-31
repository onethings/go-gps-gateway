package notify

import (
	"context"
	"strings"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
)

const (
	defaultQueueSize = 256
	defaultTimeout   = 10 * time.Second
)

// Dispatcher formats alerts and forwards them to every channel that has
// recipients. Alerts are queued and delivered from one background goroutine so
// that Enqueue never blocks a device connection.
type Dispatcher struct {
	devices   DeviceLookup
	channels  []Channel
	types     map[string]struct{}
	timeout   time.Duration
	queueSize int
	logf      func(format string, args ...any)
	meters    *metrics.Set

	queue chan *model.Alert

	closeOnce sync.Once
	wg        sync.WaitGroup

	// mu guards queue once it may be closed, and the dropped counter.
	mu      sync.Mutex
	closed  bool
	dropped int64
}

// Option customises a Dispatcher.
type Option func(*Dispatcher)

// WithLogger reports lookup and delivery failures.
func WithLogger(logf func(format string, args ...any)) Option {
	return func(d *Dispatcher) {
		if logf != nil {
			d.logf = logf
		}
	}
}

// WithMetrics publishes delivered, failed and dropped notifications.
func WithMetrics(set *metrics.Set) Option {
	return func(d *Dispatcher) { d.meters = set }
}

// WithTimeout caps the device lookup and each channel delivery.
func WithTimeout(timeout time.Duration) Option {
	return func(d *Dispatcher) {
		if timeout > 0 {
			d.timeout = timeout
		}
	}
}

// WithQueueSize caps how many alerts may wait for delivery.
func WithQueueSize(size int) Option {
	return func(d *Dispatcher) {
		if size > 0 {
			d.queueSize = size
		}
	}
}

// WithTypes restricts the delivered alert types. An empty list means every
// type; "none" disables notifications and "all"/"*" re-enables every type.
func WithTypes(types []string) Option {
	return func(d *Dispatcher) {
		if len(types) == 0 {
			return
		}
		d.types = parseTypes(strings.Join(types, ","))
	}
}

// NewDispatcher starts the background sender. Channels without a notifier are
// skipped.
func NewDispatcher(devices DeviceLookup, channels []Channel, options ...Option) *Dispatcher {
	dispatcher := &Dispatcher{
		devices:   devices,
		channels:  append([]Channel(nil), channels...),
		timeout:   defaultTimeout,
		logf:      func(string, ...any) {},
		queueSize: defaultQueueSize,
	}
	for _, option := range options {
		option(dispatcher)
	}
	if dispatcher.queueSize <= 0 {
		dispatcher.queueSize = defaultQueueSize
	}
	dispatcher.queue = make(chan *model.Alert, dispatcher.queueSize)

	dispatcher.wg.Add(1)
	go dispatcher.run()
	return dispatcher
}

// Enqueue queues one alert. It never blocks: when the queue is full the alert
// is dropped and counted.
func (d *Dispatcher) Enqueue(alert *model.Alert) {
	if alert == nil || len(d.channels) == 0 {
		return
	}
	if !d.typeEnabled(alert.Type) {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		d.dropped++
		if d.meters != nil {
			d.meters.NotificationsDropped.Inc()
		}
		return
	}

	select {
	case d.queue <- alert:
	default:
		d.dropped++
		if d.meters != nil {
			d.meters.NotificationsDropped.Inc()
		}
		d.logf("notify: queue full, dropped %s for device %d", alert.Type, alert.DeviceID)
	}
}

// Dropped reports how many alerts were discarded: the queue was full or the
// dispatcher had already been closed.
func (d *Dispatcher) Dropped() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dropped
}

// Close stops accepting alerts, delivers everything queued and waits for the
// background sender.
func (d *Dispatcher) Close(ctx context.Context) error {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		close(d.queue)
		d.mu.Unlock()
	})

	wait := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(wait)
	}()

	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Dispatcher) run() {
	defer d.wg.Done()

	for alert := range d.queue {
		d.deliver(alert)
	}
}

func (d *Dispatcher) deliver(alert *model.Alert) {
	device := d.lookup(alert.DeviceID)
	if !deviceAllows(device, alert.Type) {
		return
	}

	message := Format(alert, device)
	var attributes map[string]any
	if device != nil {
		attributes = device.Attributes
	}

	for _, channel := range d.channels {
		if channel.Notifier == nil {
			continue
		}
		targets, enabled := resolveTargets(attributes, channel)
		if !enabled || len(targets) == 0 {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
		err := channel.Notifier.Send(ctx, message, targets)
		cancel()

		name := channel.Notifier.Name()
		if err != nil {
			if d.meters != nil {
				d.meters.NotificationErrors.With(name).Inc()
			}
			d.logf("notify: %s: %v", name, err)
			continue
		}
		if d.meters != nil {
			d.meters.Notifications.With(name).Inc()
		}
	}
}

// lookup resolves the device without failing the alert: a database hiccup
// still lets the webhook deliver the alert with the bare device id.
func (d *Dispatcher) lookup(deviceID int64) *model.Device {
	if d.devices == nil || deviceID == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()

	device, err := d.devices.Device(ctx, deviceID)
	if err != nil {
		d.logf("notify: lookup device %d: %v", deviceID, err)
		return nil
	}
	return device
}

func (d *Dispatcher) typeEnabled(alertType string) bool {
	if d.types == nil {
		return true
	}
	if _, all := d.types["*"]; all {
		return true
	}
	_, enabled := d.types[alertType]
	return enabled
}

// deviceAllows applies the per-device notifyTypes attribute. An absent
// attribute leaves the server-wide filter as the only decision.
func deviceAllows(device *model.Device, alertType string) bool {
	if device == nil || device.Attributes == nil {
		return true
	}
	raw, ok := device.Attributes[AttrNotifyTypes]
	if !ok {
		return true
	}

	switch typed := raw.(type) {
	case bool:
		return typed
	case string:
		return allowedBy(parseTypes(typed), alertType)
	case []any:
		var parts []string
		for _, item := range typed {
			if text, ok := item.(string); ok {
				parts = append(parts, text)
			}
		}
		if len(parts) == 0 {
			return false
		}
		return allowedBy(parseTypes(strings.Join(parts, ",")), alertType)
	default:
		return true
	}
}

func allowedBy(types map[string]struct{}, alertType string) bool {
	if _, all := types["*"]; all {
		return true
	}
	_, allowed := types[alertType]
	return allowed
}

// parseTypes turns "overspeed, powerCut" into a set. "none" yields an empty set
// and "all" or "*" yields the wildcard.
func parseTypes(value string) map[string]struct{} {
	types := make(map[string]struct{})
	for _, part := range SplitList(value) {
		switch strings.ToLower(part) {
		case "none":
			return map[string]struct{}{}
		case "all":
			types["*"] = struct{}{}
		default:
			types[part] = struct{}{}
		}
	}
	if len(types) == 0 {
		return map[string]struct{}{}
	}
	return types
}

// resolveTargets returns the recipients for one channel. The second result is
// false when the device disabled the channel or when no target remains.
func resolveTargets(attributes map[string]any, channel Channel) ([]string, bool) {
	raw, ok := attributes[channel.Attribute]
	if !ok {
		return channel.Defaults, len(channel.Defaults) > 0
	}

	switch typed := raw.(type) {
	case nil:
		return nil, false
	case bool:
		if !typed {
			return nil, false
		}
		return channel.Defaults, len(channel.Defaults) > 0
	case string:
		targets := SplitList(typed)
		return targets, len(targets) > 0
	case []any:
		var targets []string
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			targets = append(targets, SplitList(text)...)
		}
		return targets, len(targets) > 0
	case []string:
		var targets []string
		for _, item := range typed {
			targets = append(targets, SplitList(item)...)
		}
		return targets, len(targets) > 0
	default:
		return channel.Defaults, len(channel.Defaults) > 0
	}
}

// SplitList accepts commas, semicolons and whitespace as separators so that a
// flag or devices.attributes value can be written the way it reads best.
func SplitList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		switch r {
		case ',', ';', ' ', '\t', '\n', '\r':
			return true
		default:
			return false
		}
	})

	var result []string
	for _, field := range fields {
		if field != "" {
			result = append(result, field)
		}
	}
	return result
}
