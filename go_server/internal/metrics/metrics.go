// Package metrics exposes the gateway counters in the Prometheus text format.
// It is deliberately dependency free: the exposition format is small and the
// gateway only needs counters, gauges and one histogram.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// defaultHistogramBounds are the buckets of traccar_db_write_seconds, in
// seconds.
var defaultHistogramBounds = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type collector interface {
	writeTo(w io.Writer)
}

// Counter is a monotonically increasing value.
type Counter struct {
	name, help string
	value      atomic.Int64
}

// NewCounter creates a counter.
func NewCounter(name, help string) *Counter {
	return &Counter{name: name, help: help}
}

// Inc adds one.
func (c *Counter) Inc() { c.value.Add(1) }

// Add adds n, ignoring non-positive values.
func (c *Counter) Add(n int64) {
	if n > 0 {
		c.value.Add(n)
	}
}

// Value returns the current value.
func (c *Counter) Value() int64 { return c.value.Load() }

func (c *Counter) writeTo(w io.Writer) {
	writeHeader(w, c.name, c.help, "counter")
	fmt.Fprintf(w, "%s %d\n", c.name, c.value.Load())
}

// Gauge is a value that can go up and down.
type Gauge struct {
	name, help string
	value      atomic.Int64
}

// NewGauge creates a gauge.
func NewGauge(name, help string) *Gauge {
	return &Gauge{name: name, help: help}
}

// Inc adds one.
func (g *Gauge) Inc() { g.value.Add(1) }

// Dec subtracts one.
func (g *Gauge) Dec() { g.value.Add(-1) }

// Set stores an absolute value.
func (g *Gauge) Set(value int64) { g.value.Store(value) }

// Value returns the current value.
func (g *Gauge) Value() int64 { return g.value.Load() }

func (g *Gauge) writeTo(w io.Writer) {
	writeHeader(w, g.name, g.help, "gauge")
	fmt.Fprintf(w, "%s %d\n", g.name, g.value.Load())
}

// CounterVec is a set of counters split by one label.
type CounterVec struct {
	name, help string
	label      string
	mu         sync.RWMutex
	counters   map[string]*Counter
}

// NewCounterVec creates a counter vector with a single label.
func NewCounterVec(name, help, label string) *CounterVec {
	return &CounterVec{
		name:     name,
		help:     help,
		label:    label,
		counters: make(map[string]*Counter),
	}
}

// With returns the counter for a label value, creating it on first use.
func (v *CounterVec) With(value string) *Counter {
	v.mu.RLock()
	counter, ok := v.counters[value]
	v.mu.RUnlock()
	if ok {
		return counter
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if counter, ok := v.counters[value]; ok {
		return counter
	}
	counter = &Counter{name: v.name}
	v.counters[value] = counter
	return counter
}

func (v *CounterVec) writeTo(w io.Writer) {
	writeHeader(w, v.name, v.help, "counter")

	for _, value := range v.labelValues() {
		fmt.Fprintf(w, "%s{%s} %d\n", v.name, label(v.label, value), v.With(value).Value())
	}
}

func (v *CounterVec) labelValues() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()

	values := make([]string, 0, len(v.counters))
	for value := range v.counters {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

// GaugeVec is a set of gauges split by one label.
type GaugeVec struct {
	name, help string
	label      string
	mu         sync.RWMutex
	gauges     map[string]*Gauge
}

// NewGaugeVec creates a gauge vector with a single label.
func NewGaugeVec(name, help, label string) *GaugeVec {
	return &GaugeVec{
		name:   name,
		help:   help,
		label:  label,
		gauges: make(map[string]*Gauge),
	}
}

// With returns the gauge for a label value, creating it on first use.
func (v *GaugeVec) With(value string) *Gauge {
	v.mu.RLock()
	gauge, ok := v.gauges[value]
	v.mu.RUnlock()
	if ok {
		return gauge
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if gauge, ok := v.gauges[value]; ok {
		return gauge
	}
	gauge = &Gauge{name: v.name}
	v.gauges[value] = gauge
	return gauge
}

func (v *GaugeVec) writeTo(w io.Writer) {
	writeHeader(w, v.name, v.help, "gauge")

	v.mu.RLock()
	values := make([]string, 0, len(v.gauges))
	for value := range v.gauges {
		values = append(values, value)
	}
	sort.Strings(values)
	gauges := make(map[string]int64, len(v.gauges))
	for value, gauge := range v.gauges {
		gauges[value] = gauge.Value()
	}
	v.mu.RUnlock()

	for _, value := range values {
		fmt.Fprintf(w, "%s{%s} %d\n", v.name, label(v.label, value), gauges[value])
	}
}

// Histogram counts observations in cumulative buckets.
type Histogram struct {
	name, help string
	bounds     []float64

	mu      sync.Mutex
	buckets []int64
	sum     float64
	count   int64
}

// NewHistogram creates a histogram with the default buckets.
func NewHistogram(name, help string) *Histogram {
	return &Histogram{
		name:    name,
		help:    help,
		bounds:  defaultHistogramBounds,
		buckets: make([]int64, len(defaultHistogramBounds)),
	}
}

// Observe records one measurement, in seconds.
func (h *Histogram) Observe(value float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.count++
	h.sum += value
	for i, bound := range h.bounds {
		if value <= bound {
			h.buckets[i]++
			return
		}
	}
}

// Count returns how many observations were recorded.
func (h *Histogram) Count() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

func (h *Histogram) writeTo(w io.Writer) {
	h.mu.Lock()
	buckets := append([]int64(nil), h.buckets...)
	sum, count := h.sum, h.count
	h.mu.Unlock()

	writeHeader(w, h.name, h.help, "histogram")

	cumulative := int64(0)
	for i, bound := range h.bounds {
		cumulative += buckets[i]
		fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", h.name, formatFloat(bound), cumulative)
	}
	fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n", h.name, count)
	fmt.Fprintf(w, "%s_sum %s\n", h.name, formatFloat(sum))
	fmt.Fprintf(w, "%s_count %d\n", h.name, count)
}

// Set holds every metric the gateway exports.
type Set struct {
	ConnectionsActive    *GaugeVec
	Frames               *CounterVec
	DecodeErrors         *CounterVec
	Positions            *CounterVec
	PositionsDropped     *Counter
	DBWriteSeconds       *Histogram
	DBWriteErrors        *Counter
	Alerts               *CounterVec
	AlertsDropped        *Counter
	Notifications        *CounterVec
	NotificationErrors   *CounterVec
	NotificationsDropped *Counter
}

// New creates the metric set with the documented names.
func New() *Set {
	return &Set{
		ConnectionsActive: NewGaugeVec("traccar_connections_active",
			"Open device connections.", "protocol"),
		Frames: NewCounterVec("traccar_frames_total",
			"Frames decoded successfully.", "protocol"),
		DecodeErrors: NewCounterVec("traccar_decode_errors_total",
			"Frames that failed to decode.", "protocol"),
		Positions: NewCounterVec("traccar_positions_total",
			"Positions decoded.", "protocol"),
		PositionsDropped: NewCounter("traccar_position_dropped_total",
			"Positions dropped because the write queue was full."),
		DBWriteSeconds: NewHistogram("traccar_db_write_seconds",
			"Duration of one position batch insert."),
		DBWriteErrors: NewCounter("traccar_db_write_errors_total",
			"Failed position batch inserts."),
		Alerts: NewCounterVec("traccar_alerts_total",
			"Alerts accepted for writing.", "type"),
		AlertsDropped: NewCounter("traccar_alert_dropped_total",
			"Alerts dropped because the write queue was full."),
		Notifications: NewCounterVec("traccar_notifications_total",
			"Alert notifications delivered.", "channel"),
		NotificationErrors: NewCounterVec("traccar_notification_errors_total",
			"Failed alert notification sends.", "channel"),
		NotificationsDropped: NewCounter("traccar_notification_dropped_total",
			"Notifications dropped because the send queue was full."),
	}
}

// WriteTo renders every metric in the Prometheus text format.
func (s *Set) Render(w io.Writer) {
	for _, collector := range s.collectors() {
		collector.writeTo(w)
	}
}

// Handler serves the metrics for a single scrape.
func (s *Set) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		s.Render(w)
	})
}

func (s *Set) collectors() []collector {
	return []collector{
		s.ConnectionsActive,
		s.Frames,
		s.DecodeErrors,
		s.Positions,
		s.PositionsDropped,
		s.DBWriteSeconds,
		s.DBWriteErrors,
		s.Alerts,
		s.AlertsDropped,
		s.Notifications,
		s.NotificationErrors,
		s.NotificationsDropped,
	}
}

func writeHeader(w io.Writer, name, help, kind string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, escapeHelp(help), name, kind)
}

func label(name, value string) string {
	return name + "=" + strconv.Quote(value)
}

func escapeHelp(help string) string {
	help = strings.ReplaceAll(help, `\`, `\\`)
	return strings.ReplaceAll(help, "\n", `\n`)
}

// formatFloat keeps the bucket bounds readable (0.005, 1, 10) instead of using
// the exponent notation fmt's %v would produce for small values.
func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
