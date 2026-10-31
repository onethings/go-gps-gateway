package api

import (
	"net/http"
	"strings"
	"testing"

	"traccar-server_go/go_server/internal/metrics"
)

func TestMetricsEndpoint(t *testing.T) {
	meters := metrics.New()
	meters.Frames.With("gt06").Inc()
	meters.Alerts.With("powerCut").Inc()

	handler := NewServer(nil, WithMetrics(meters)).Handler()

	recorder := get(t, handler, "/metrics")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain; version=0.0.4") {
		t.Fatalf("content type = %q", got)
	}

	body := recorder.Body.String()
	for _, want := range []string{
		`traccar_frames_total{protocol="gt06"} 1`,
		`traccar_alerts_total{type="powerCut"} 1`,
		"# TYPE traccar_db_write_seconds histogram",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q:\n%s", want, body)
		}
	}
}

func TestMetricsRequiresAPIKey(t *testing.T) {
	handler := NewServer(nil, WithMetrics(metrics.New()), WithAPIKey("secret")).Handler()

	if recorder := get(t, handler, "/metrics"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestMetricsNotConfigured(t *testing.T) {
	handler := NewServer(nil).Handler()

	if recorder := get(t, handler, "/metrics"); recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", recorder.Code)
	}
}
