package postgres

import (
	"context"
	"errors"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
)

// ErrSinkFull is returned when the queue is saturated and the position was
// dropped instead of blocking the device connection.
var ErrSinkFull = errors.New("postgres: sink queue full")

// PositionWriter is the minimal contract the batch sink needs, which keeps it
// testable without a database.
type PositionWriter interface {
	InsertPositions(ctx context.Context, positions []*model.Position) error
}

// BatchSink buffers positions and writes them in batches. Handle never blocks
// the connection goroutine.
type BatchSink struct {
	writer   PositionWriter
	size     int
	interval time.Duration
	timeout  time.Duration
	logf     func(format string, args ...any)

	queue   chan *model.Position
	flushCh chan chan error
	done    chan struct{}

	closeOnce sync.Once
	wg        sync.WaitGroup

	mu      sync.Mutex
	dropped int64
	meters  *metrics.Set
}

// BatchOption customises a BatchSink.
type BatchOption func(*BatchSink)

// WithBatchTimeout caps the duration of one insert.
func WithBatchTimeout(timeout time.Duration) BatchOption {
	return func(b *BatchSink) {
		if timeout > 0 {
			b.timeout = timeout
		}
	}
}

// WithBatchLogger reports write failures and dropped positions.
func WithBatchLogger(logf func(format string, args ...any)) BatchOption {
	return func(b *BatchSink) {
		if logf != nil {
			b.logf = logf
		}
	}
}

// WithBatchMetrics publishes write durations, write failures and drops.
func WithBatchMetrics(set *metrics.Set) BatchOption {
	return func(b *BatchSink) { b.meters = set }
}

// NewBatchSink starts the background writer.
func NewBatchSink(writer PositionWriter, size int, interval time.Duration, options ...BatchOption) *BatchSink {
	if size <= 0 {
		size = 500
	}
	if interval <= 0 {
		interval = time.Second
	}

	sink := &BatchSink{
		writer:   writer,
		size:     size,
		interval: interval,
		timeout:  10 * time.Second,
		logf:     func(string, ...any) {},
		queue:    make(chan *model.Position, size*4),
		flushCh:  make(chan chan error),
		done:     make(chan struct{}),
	}
	for _, option := range options {
		option(sink)
	}

	sink.wg.Add(1)
	go sink.run()
	return sink
}

// Handle implements gateway.Sink without blocking.
func (b *BatchSink) Handle(position *model.Position) error {
	select {
	case b.queue <- position:
		return nil
	default:
		b.mu.Lock()
		b.dropped++
		b.mu.Unlock()
		if b.meters != nil {
			b.meters.PositionsDropped.Inc()
		}
		return ErrSinkFull
	}
}

// Dropped reports how many positions were discarded because the queue was
// full.
func (b *BatchSink) Dropped() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

// Flush writes everything currently queued and waits for it to complete.
func (b *BatchSink) Flush(ctx context.Context) error {
	reply := make(chan error, 1)
	select {
	case b.flushCh <- reply:
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close flushes the queue and stops the background writer.
func (b *BatchSink) Close(ctx context.Context) error {
	var flushErr error
	b.closeOnce.Do(func() {
		flushErr = b.Flush(ctx)
		close(b.queue)
	})

	wait := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(wait)
	}()

	select {
	case <-wait:
		return flushErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *BatchSink) run() {
	defer b.wg.Done()

	batch := make([]*model.Position, 0, b.size)
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()

	write := func() error {
		if len(batch) == 0 {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
		defer cancel()

		start := time.Now()
		err := b.writer.InsertPositions(ctx, batch)
		if b.meters != nil {
			b.meters.DBWriteSeconds.Observe(time.Since(start).Seconds())
			if err != nil {
				b.meters.DBWriteErrors.Inc()
			}
		}
		if err != nil {
			b.logf("postgres: batch write (%d positions): %v", len(batch), err)
		}
		batch = batch[:0]
		return err
	}

	for {
		select {
		case position, ok := <-b.queue:
			if !ok {
				_ = write()
				close(b.done)
				return
			}
			batch = append(batch, position)
			if len(batch) >= b.size {
				_ = write()
			}

		case <-ticker.C:
			_ = write()

		case reply := <-b.flushCh:
		drain:
			for {
				select {
				case position, ok := <-b.queue:
					if !ok {
						break drain
					}
					batch = append(batch, position)
					if len(batch) >= b.size {
						_ = write()
					}
				default:
					break drain
				}
			}
			reply <- write()
		}
	}
}
