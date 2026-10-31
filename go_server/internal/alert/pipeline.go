package alert

import (
	"context"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
)

const (
	defaultBatchSize    = 200
	defaultBatchDelay   = time.Second
	defaultWriteTimeout = 10 * time.Second
)

// Writer stores a batch of alerts.
type Writer interface {
	InsertAlerts(ctx context.Context, alerts []*model.Alert) error
}

type writerFunc func(ctx context.Context, alerts []*model.Alert) error

func (f writerFunc) InsertAlerts(ctx context.Context, alerts []*model.Alert) error {
	return f(ctx, alerts)
}

// Sink is the position sink the pipeline forwards to. It matches
// gateway.Sink structurally, so this package does not depend on the gateway.
type Sink interface {
	Handle(position *model.Position) error
}

// Pipeline evaluates every position, buffers the alerts and writes them in
// batches. Handle never blocks the device connection.
type Pipeline struct {
	engine *Engine
	next   Sink
	writer Writer

	size     int
	interval time.Duration
	timeout  time.Duration
	logf     func(format string, args ...any)
	meters   *metrics.Set
	hook     func(*model.Alert)

	queue chan *model.Alert

	closeOnce sync.Once
	wg        sync.WaitGroup

	// mu guards queue once it may be closed, and the dropped counter.
	mu      sync.Mutex
	closed  bool
	dropped int64
}

// Option customises a Pipeline.
type Option func(*Pipeline)

// WithLogger reports write failures and dropped alerts.
func WithLogger(logf func(format string, args ...any)) Option {
	return func(p *Pipeline) {
		if logf != nil {
			p.logf = logf
		}
	}
}

// WithBatchSize caps how many alerts one insert carries.
func WithBatchSize(size int) Option {
	return func(p *Pipeline) {
		if size > 0 {
			p.size = size
		}
	}
}

// WithBatchInterval caps how long an alert waits before it is written.
func WithBatchInterval(interval time.Duration) Option {
	return func(p *Pipeline) {
		if interval > 0 {
			p.interval = interval
		}
	}
}

// WithWriteTimeout caps the duration of one insert.
func WithWriteTimeout(timeout time.Duration) Option {
	return func(p *Pipeline) {
		if timeout > 0 {
			p.timeout = timeout
		}
	}
}

// WithMetrics publishes generated alerts and dropped alerts.
func WithMetrics(set *metrics.Set) Option {
	return func(p *Pipeline) { p.meters = set }
}

// WithAlertHook observes every alert the engine raises, including the ones the
// write queue drops. The hook runs on the device goroutine, so it must not
// block; the notify dispatcher only enqueues.
func WithAlertHook(hook func(*model.Alert)) Option {
	return func(p *Pipeline) {
		if hook != nil {
			p.hook = hook
		}
	}
}

// NewPipeline starts the background writer. next may be nil, which makes the
// pipeline a terminal sink.
func NewPipeline(engine *Engine, next Sink, writer Writer, options ...Option) *Pipeline {
	pipeline := &Pipeline{
		engine:   engine,
		next:     next,
		writer:   writer,
		size:     defaultBatchSize,
		interval: defaultBatchDelay,
		timeout:  defaultWriteTimeout,
		logf:     func(string, ...any) {},
	}
	for _, option := range options {
		option(pipeline)
	}
	if pipeline.size <= 0 {
		pipeline.size = defaultBatchSize
	}
	if pipeline.writer == nil {
		pipeline.writer = writerFunc(func(context.Context, []*model.Alert) error { return nil })
	}
	pipeline.queue = make(chan *model.Alert, pipeline.size*4)

	pipeline.wg.Add(1)
	go pipeline.run()
	return pipeline
}

// Handle implements the position sink contract: evaluate, enqueue, forward.
func (p *Pipeline) Handle(position *model.Position) error {
	if p.engine != nil {
		for _, alert := range p.engine.Evaluate(position) {
			p.enqueue(alert)
			if p.hook != nil {
				p.hook(alert)
			}
		}
	}
	if p.next == nil {
		return nil
	}
	return p.next.Handle(position)
}

func (p *Pipeline) enqueue(alert *model.Alert) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		p.dropped++
		if p.meters != nil {
			p.meters.AlertsDropped.Inc()
		}
		return
	}

	select {
	case p.queue <- alert:
		if p.meters != nil {
			p.meters.Alerts.With(alert.Type).Inc()
		}
	default:
		p.dropped++
		if p.meters != nil {
			p.meters.AlertsDropped.Inc()
		}
		p.logf("alert: queue full, dropped %s for device %d", alert.Type, alert.DeviceID)
	}
}

// Dropped reports how many alerts were discarded: the queue was full or the
// pipeline had already been closed.
func (p *Pipeline) Dropped() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dropped
}

// Close stops accepting alerts, writes everything queued and waits for the
// background writer.
func (p *Pipeline) Close(ctx context.Context) error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		close(p.queue)
		p.mu.Unlock()
	})

	wait := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(wait)
	}()

	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Pipeline) run() {
	defer p.wg.Done()

	batch := make([]*model.Alert, 0, p.size)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	write := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
		defer cancel()

		if err := p.writer.InsertAlerts(ctx, batch); err != nil {
			p.logf("alert: write (%d alerts): %v", len(batch), err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case alert, ok := <-p.queue:
			if !ok {
				// Reading a closed channel drains the buffer first, so this is
				// the moment to flush and stop.
				write()
				return
			}
			batch = append(batch, alert)
			if len(batch) >= p.size {
				write()
			}

		case <-ticker.C:
			write()
		}
	}
}
