package alert

import (
	"context"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
)

func TestPipelineMetrics(t *testing.T) {
	meters := metrics.New()
	pipeline := NewPipeline(New(Config{OverspeedKnots: 20}, nil), nil, nil, WithMetrics(meters))

	pipeline.Handle(speed(30))
	pipeline.Handle(speed(30))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	if got := meters.Alerts.With(model.EventDeviceOverspeed).Value(); got != 2 {
		t.Errorf("overspeed alerts = %d, want 2", got)
	}
	if got := meters.AlertsDropped.Value(); got != 0 {
		t.Errorf("dropped = %d, want 0", got)
	}
}

func TestPipelineMetricsAfterClose(t *testing.T) {
	meters := metrics.New()
	pipeline := NewPipeline(New(Config{OverspeedKnots: 20}, nil), nil, nil, WithMetrics(meters))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Alerts raised after Close are dropped, which the counter has to show.
	pipeline.Handle(speed(30))

	if got := meters.AlertsDropped.Value(); got != 1 {
		t.Errorf("dropped = %d, want 1", got)
	}
}
