package alert

import (
	"context"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

func TestPipelineAlertHookSeesEveryAlert(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []*model.Alert
	)

	engine := New(Config{OverspeedKnots: 30}, nil)
	pipeline := NewPipeline(engine, nil, &recordingWriter{},
		WithBatchInterval(time.Hour),
		WithAlertHook(func(alert *model.Alert) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, alert)
		}))

	position := speed(40)
	position.DeviceID = 7
	if err := pipeline.Handle(position); err != nil {
		t.Fatalf("handle: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("hook calls = %d, want 1", len(seen))
	}
	if seen[0].Type != model.AlertOverspeed {
		t.Errorf("alert type = %q", seen[0].Type)
	}
	if seen[0].DeviceID != 7 {
		t.Errorf("device id = %d", seen[0].DeviceID)
	}
}

func TestPipelineHookRunsDespiteWriteBackpressure(t *testing.T) {
	var (
		mu   sync.Mutex
		seen int
	)

	writer := &recordingWriter{release: make(chan struct{})}
	engine := New(Config{OverspeedKnots: 30}, nil)
	pipeline := NewPipeline(engine, nil, writer,
		WithBatchSize(1),
		WithBatchInterval(time.Hour),
		WithAlertHook(func(*model.Alert) {
			mu.Lock()
			defer mu.Unlock()
			seen++
		}))

	for i := 0; i < 5; i++ {
		position := speed(40)
		position.DeviceID = int64(i + 1)
		if err := pipeline.Handle(position); err != nil {
			t.Fatalf("handle: %v", err)
		}
	}

	close(writer.release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if seen != 5 {
		t.Errorf("hook calls = %d, want 5", seen)
	}
}
