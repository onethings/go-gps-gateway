package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
)

type failingWriter struct{}

func (failingWriter) InsertPositions(context.Context, []*model.Position) error {
	return errors.New("boom")
}

type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingWriter) InsertPositions(_ context.Context, _ []*model.Position) error {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return nil
}

func TestBatchSinkMetrics(t *testing.T) {
	meters := metrics.New()
	writer := &recordingWriter{}
	sink := NewBatchSink(writer, 10, time.Hour, WithBatchMetrics(meters))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := sink.Handle(samplePosition()); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := sink.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := sink.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	if got := meters.DBWriteSeconds.Count(); got != 1 {
		t.Errorf("db write observations = %d, want 1", got)
	}
	if got := meters.DBWriteErrors.Value(); got != 0 {
		t.Errorf("db write errors = %d, want 0", got)
	}
	if got := meters.PositionsDropped.Value(); got != 0 {
		t.Errorf("dropped = %d, want 0", got)
	}
}

func TestBatchSinkMetricsWriteError(t *testing.T) {
	meters := metrics.New()
	sink := NewBatchSink(failingWriter{}, 10, time.Hour, WithBatchMetrics(meters))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := sink.Handle(samplePosition()); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := sink.Flush(ctx); err == nil {
		t.Fatal("flush succeeded, want the writer error")
	}

	if got := meters.DBWriteErrors.Value(); got != 1 {
		t.Errorf("db write errors = %d, want 1", got)
	}
	if got := meters.DBWriteSeconds.Count(); got != 1 {
		t.Errorf("db write observations = %d, want 1", got)
	}
	// The failed batch is discarded, so closing has nothing left to write.
	if err := sink.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := meters.DBWriteErrors.Value(); got != 1 {
		t.Errorf("db write errors = %d, want 1", got)
	}
}

func TestBatchSinkMetricsDrops(t *testing.T) {
	meters := metrics.New()
	writer := &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}

	// With size 1 the queue holds four positions (4*size) and the writer blocks
	// on the first batch, which makes the drop deterministic.
	sink := NewBatchSink(writer, 1, time.Hour, WithBatchMetrics(meters))

	if err := sink.Handle(samplePosition()); err != nil {
		t.Fatalf("handle: %v", err)
	}
	<-writer.started

	for i := 0; i < 4; i++ {
		if err := sink.Handle(samplePosition()); err != nil {
			t.Fatalf("handle %d: %v", i, err)
		}
	}
	if err := sink.Handle(samplePosition()); !errors.Is(err, ErrSinkFull) {
		t.Fatalf("err = %v, want ErrSinkFull", err)
	}
	if got := meters.PositionsDropped.Value(); got != 1 {
		t.Errorf("dropped = %d, want 1", got)
	}

	close(writer.release)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sink.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}
