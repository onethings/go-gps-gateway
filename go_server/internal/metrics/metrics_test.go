package metrics

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCounterAndGauge(t *testing.T) {
	counter := NewCounter("traccar_test_total", "Test counter.")
	counter.Inc()
	counter.Inc()
	counter.Add(-5)
	if got := counter.Value(); got != 2 {
		t.Fatalf("counter = %d, want 2", got)
	}

	gauge := NewGauge("traccar_test_active", "Test gauge.")
	gauge.Inc()
	gauge.Inc()
	gauge.Dec()
	if got := gauge.Value(); got != 1 {
		t.Fatalf("gauge = %d, want 1", got)
	}
	gauge.Set(7)
	if got := gauge.Value(); got != 7 {
		t.Fatalf("gauge = %d, want 7", got)
	}
}

func TestCounterVecRendering(t *testing.T) {
	vec := NewCounterVec("traccar_frames_total", "Frames.", "protocol")
	vec.With("gt06").Inc()
	vec.With("h02").Add(2)
	vec.With("gt06").Inc()

	var buffer bytes.Buffer
	vec.writeTo(&buffer)

	want := `# HELP traccar_frames_total Frames.
# TYPE traccar_frames_total counter
traccar_frames_total{protocol="gt06"} 2
traccar_frames_total{protocol="h02"} 2
`
	if got := buffer.String(); got != want {
		t.Fatalf("rendered:\n%s\nwant:\n%s", got, want)
	}
}

func TestGaugeVecRenderingAndEscaping(t *testing.T) {
	vec := NewGaugeVec("traccar_connections_active", "Connections.", "protocol")
	vec.With(`odd"name`).Set(3)

	var buffer bytes.Buffer
	vec.writeTo(&buffer)

	if !strings.Contains(buffer.String(), `traccar_connections_active{protocol="odd\"name"} 3`) {
		t.Fatalf("rendered:\n%s", buffer.String())
	}
}

func TestHistogramIsCumulative(t *testing.T) {
	histogram := NewHistogram("traccar_db_write_seconds", "Duration.")
	histogram.Observe(0.003)
	histogram.Observe(0.03)
	histogram.Observe(20)

	if got := histogram.Count(); got != 3 {
		t.Fatalf("count = %d, want 3", got)
	}

	var buffer bytes.Buffer
	histogram.writeTo(&buffer)
	rendered := buffer.String()

	for _, want := range []string{
		`# TYPE traccar_db_write_seconds histogram`,
		`traccar_db_write_seconds_bucket{le="0.005"} 1`,
		`traccar_db_write_seconds_bucket{le="0.01"} 1`,
		`traccar_db_write_seconds_bucket{le="0.05"} 2`,
		`traccar_db_write_seconds_bucket{le="10"} 2`,
		`traccar_db_write_seconds_bucket{le="+Inf"} 3`,
		`traccar_db_write_seconds_count 3`,
		`traccar_db_write_seconds_sum `,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output is missing %q:\n%s", want, rendered)
		}
	}
}

func TestSetRendersEveryMetric(t *testing.T) {
	set := New()
	set.Frames.With("gt06").Inc()

	var buffer bytes.Buffer
	set.Render(&buffer)
	rendered := buffer.String()

	for _, name := range []string{
		"traccar_connections_active",
		"traccar_frames_total",
		"traccar_decode_errors_total",
		"traccar_positions_total",
		"traccar_position_dropped_total",
		"traccar_db_write_seconds",
		"traccar_db_write_errors_total",
		"traccar_alerts_total",
		"traccar_alert_dropped_total",
		"traccar_notifications_total",
		"traccar_notification_errors_total",
		"traccar_notification_dropped_total",
		"traccar_commands_sent_total",
		"traccar_command_retry_total",
		"traccar_command_dropped_total",
		"traccar_commands_queued",
	} {
		if !strings.Contains(rendered, "# TYPE "+name) {
			t.Errorf("rendered output is missing %q:\n%s", name, rendered)
		}
	}
}

func TestHandlerContentType(t *testing.T) {
	recorder := httptest.NewRecorder()
	New().Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))

	if recorder.Code != 200 {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain; version=0.0.4") {
		t.Fatalf("content type = %q", got)
	}
}
