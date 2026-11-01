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

// Writer stores a batch of reports. A report carries the position together with
// the events the rules raised for it, which is what lets the writer store the
// two in one transaction and put the position id on every event.
type Writer interface {
	InsertReports(ctx context.Context, reports []*model.PositionReport) error
}

type writerFunc func(ctx context.Context, reports []*model.PositionReport) error

func (f writerFunc) InsertReports(ctx context.Context, reports []*model.PositionReport) error {
	return f(ctx, reports)
}

// Sink is the position sink the pipeline forwards to. It matches
// gateway.Sink structurally, so this package does not depend on the gateway.
type Sink interface {
	Handle(position *model.Position) error
}

// Pipeline evaluates every position and writes the events it raised together
// with the position. A position that raises nothing - the common case - goes
// straight on to the next sink; one that raises something is queued as a report,
// so the device connection still never waits for the database.
type Pipeline struct {
	engine *Engine
	next   Sink
	writer Writer

	size     int
	interval time.Duration
	timeout  time.Duration
	logf     func(format string, args ...any)
	meters   *metrics.Set
	hook     func(*model.Event)
	// positionHook observes every position, whatever the rules decide.
	positionHook func(*model.Position)

	queue chan *model.PositionReport

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

// WithAlertHook observes every event the engine raises, including the ones the
// write queue drops. The hook runs on the device goroutine, so it must not
// block; the notify dispatcher only enqueues.
func WithAlertHook(hook func(*model.Event)) Option {
	return func(p *Pipeline) {
		if hook != nil {
			p.hook = hook
		}
	}
}

// WithPositionHook observes every position the pipeline processes, including
// the ones that raise an event and are written by the report queue instead of
// the next sink. The hook runs on the device goroutine, so it must not block;
// the socket hub only enqueues.
func WithPositionHook(hook func(*model.Position)) Option {
	return func(p *Pipeline) {
		if hook != nil {
			p.positionHook = hook
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
		pipeline.writer = writerFunc(func(context.Context, []*model.PositionReport) error { return nil })
	}
	pipeline.queue = make(chan *model.PositionReport, pipeline.size*4)

	pipeline.wg.Add(1)
	go pipeline.run()
	return pipeline
}

// Handle implements the position sink contract: evaluate, and either queue a
// report or forward the position untouched.
func (p *Pipeline) Handle(position *model.Position) error {
	if p.positionHook != nil {
		p.positionHook(position)
	}
	if events := p.evaluate(position); len(events) > 0 {
		p.enqueue(&model.PositionReport{Position: position, Events: events})
		return nil
	}
	if p.next == nil {
		return nil
	}
	return p.next.Handle(position)
}

// evaluate runs the rules and reports every event to the hook, including the
// ones the queue may drop: a notification is worth sending even when the history
// row is lost.
func (p *Pipeline) evaluate(position *model.Position) []*model.Event {
	if p.engine == nil {
		return nil
	}

	events := p.engine.Evaluate(position)
	for _, event := range events {
		if p.meters != nil {
			p.meters.Alerts.With(event.Type).Inc()
		}
		if p.hook != nil {
			p.hook(event)
		}
	}
	return events
}

func (p *Pipeline) enqueue(report *model.PositionReport) {
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
	case p.queue <- report:
	default:
		p.dropped++
		if p.meters != nil {
			p.meters.AlertsDropped.Inc()
		}
		p.logf("alert: queue full, dropped %d events for device %d",
			len(report.Events), report.Position.DeviceID)
	}
}

// Dropped reports how many reports were discarded: the queue was full or the
// pipeline had already been closed. A dropped report is a lost position and its
// events, so the counter is the one to watch.
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

	batch := make([]*model.PositionReport, 0, p.size)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	write := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
		defer cancel()

		if err := p.writer.InsertReports(ctx, batch); err != nil {
			p.logf("alert: write %d reports: %v", len(batch), err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case report, ok := <-p.queue:
			if !ok {
				// Reading a closed channel drains the buffer first, so this is
				// the moment to flush and stop.
				write()
				return
			}
			batch = append(batch, report)
			if len(batch) >= p.size {
				write()
			}

		case <-ticker.C:
			write()
		}
	}
}
