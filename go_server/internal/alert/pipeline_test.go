package alert

import (
	"context"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

type recordingWriter struct {
	mu      sync.Mutex
	batches [][]*model.PositionReport
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *recordingWriter) InsertReports(_ context.Context, reports []*model.PositionReport) error {
	w.once.Do(func() {
		if w.started != nil {
			close(w.started)
		}
	})
	if w.release != nil {
		<-w.release
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.batches = append(w.batches, append([]*model.PositionReport(nil), reports...))
	return nil
}

// snapshot returns the batches and how many events they carried in total.
func (w *recordingWriter) snapshot() ([][]*model.PositionReport, int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	total := 0
	for _, batch := range w.batches {
		for _, report := range batch {
			total += len(report.Events)
		}
	}
	return w.batches, total
}

type recordingSink struct {
	mu        sync.Mutex
	positions int
	err       error
}

func (s *recordingSink) Handle(*model.Position) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.positions++
	return s.err
}

func TestPipelineQueuesReportsAndForwardsQuietPositions(t *testing.T) {
	writer := &recordingWriter{}
	next := &recordingSink{}
	engine := New(Config{OverspeedKnots: 30}, nil)
	pipeline := NewPipeline(engine, next, writer, WithBatchSize(10), WithBatchInterval(time.Hour))

	for i := 0; i < 3; i++ {
		position := speed(40)
		position.DeviceID = int64(i + 1)
		if err := pipeline.Handle(position); err != nil {
			t.Fatalf("handle: %v", err)
		}
	}

	// A position that raises nothing still goes to the next sink.
	if err := pipeline.Handle(speed(1)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The three speeding positions are stored with their event, so they do not
	// reach the forwarding sink: the writer owns them.
	if next.positions != 1 {
		t.Errorf("forwarded positions = %d, want 1", next.positions)
	}
	batches, total := writer.snapshot()
	if total != 3 {
		t.Fatalf("written events = %d, want 3", total)
	}
	for i, batch := range batches {
		if len(batch) > 10 {
			t.Errorf("batch %d has %d reports", i, len(batch))
		}
	}
	for _, batch := range batches {
		for _, report := range batch {
			if report.Position == nil || report.Position.DeviceID == 0 {
				t.Errorf("report without a position: %#v", report)
			}
		}
	}
	if pipeline.Dropped() != 0 {
		t.Errorf("dropped = %d", pipeline.Dropped())
	}
}

func TestPipelineDropsAlertsWhenQueueIsFull(t *testing.T) {
	writer := &recordingWriter{started: make(chan struct{}), release: make(chan struct{})}
	engine := New(Config{OverspeedKnots: 30}, nil)
	// A batch size of one puts the writer to work on the very first alert, so
	// the queue below fills up predictably.
	pipeline := NewPipeline(engine, nil, writer, WithBatchSize(1), WithBatchInterval(time.Hour))

	if err := pipeline.Handle(speed(40)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	select {
	case <-writer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not start")
	}

	for i := 0; i < 20; i++ {
		if err := pipeline.Handle(speed(40)); err != nil {
			t.Fatalf("handle: %v", err)
		}
	}
	if pipeline.Dropped() == 0 {
		t.Error("expected alerts to be dropped while the writer is blocked")
	}

	close(writer.release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestPipelineIgnoresAlertsAfterClose(t *testing.T) {
	writer := &recordingWriter{}
	pipeline := NewPipeline(New(Config{OverspeedKnots: 30}, nil), nil, writer)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A late position must neither panic on the closed queue nor be written.
	if err := pipeline.Handle(speed(40)); err != nil {
		t.Fatalf("handle after close: %v", err)
	}
	if _, total := writer.snapshot(); total != 0 {
		t.Errorf("written alerts = %d, want 0", total)
	}
	if pipeline.Dropped() != 1 {
		t.Errorf("dropped = %d, want 1", pipeline.Dropped())
	}
}

func TestPipelineForwardsSinkError(t *testing.T) {
	next := &recordingSink{err: context.Canceled}
	pipeline := NewPipeline(New(Config{}, nil), next, &recordingWriter{})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = pipeline.Close(ctx)
	}()

	if err := pipeline.Handle(speed(1)); err != context.Canceled {
		t.Fatalf("err = %v, want the sink error", err)
	}
}

func TestPipelineWithoutEngineStillForwards(t *testing.T) {
	next := &recordingSink{}
	pipeline := NewPipeline(nil, next, &recordingWriter{})

	if err := pipeline.Handle(speed(1)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if next.positions != 1 {
		t.Errorf("forwarded positions = %d", next.positions)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}
