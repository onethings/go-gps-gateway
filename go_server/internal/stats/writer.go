// Package stats writes the periodic row of the statistics table. The Prometheus
// metrics stay the live view; this is the history /api/statistics reads back.
package stats

import (
	"context"
	"time"

	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
)

// DefaultInterval is how often a row is written. The Java server uses the same
// order of magnitude, and a row a minute keeps the table readable for a year.
const DefaultInterval = time.Minute

// Store is the part of the storage the writer needs.
type Store interface {
	// InsertStatistics appends one snapshot.
	InsertStatistics(ctx context.Context, row *model.Statistics) error
	// OnlineCounts returns how many devices are connected and how many distinct
	// users own them.
	OnlineCounts(ctx context.Context) (devices, users int, err error)
}

// Writer turns the process counters into rows. Every counter is written as the
// increment since the previous row, so a row says what happened in that minute
// rather than what happened since the process started.
type Writer struct {
	Store    Store
	Metrics  *metrics.Set
	Interval time.Duration
	Now      func() time.Time

	last countersWithProtocols
}

// counters is the previous value of every counter a row carries.
type counters struct {
	requests            int64
	frames              int64
	positions           int64
	alerts              int64
	notifications       map[string]int64
	geocoderRequests    int64
	geolocationRequests int64
}

// Capture writes one row. It is exported so a test and the shutdown path can
// write the final snapshot without waiting for a tick.
func (w *Writer) Capture(ctx context.Context) error {
	if w.Store == nil || w.Metrics == nil {
		return nil
	}
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}

	devices, users, err := w.Store.OnlineCounts(ctx)
	if err != nil {
		return err
	}

	current := w.snapshot()
	row := &model.Statistics{
		CaptureTime:         now.UTC(),
		ActiveDevices:       devices,
		ActiveUsers:         users,
		Requests:            int(difference(current.requests, w.last.requests)),
		MessagesReceived:    int(difference(current.frames, w.last.frames)),
		MessagesStored:      int(difference(current.positions, w.last.positions)),
		MailSent:            int(difference(current.notifications["mail"], w.last.notifications["mail"])),
		SmsSent:             int(difference(current.notifications["sms"], w.last.notifications["sms"])),
		GeocoderRequests:    int(difference(current.geocoderRequests, w.last.geocoderRequests)),
		GeolocationRequests: int(difference(current.geolocationRequests, w.last.geolocationRequests)),
		Protocols:           differences(current.framesByProtocol, w.last.framesByProtocol),
		Attributes:          model.Attributes{},
	}
	if err := w.Store.InsertStatistics(ctx, row); err != nil {
		return err
	}
	w.last = current
	return nil
}

// Run writes a row per interval until ctx is cancelled.
func (w *Writer) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	// The first row is written after one interval: a row at startup would only
	// describe the empty counters.
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.Capture(ctx); err != nil && ctx.Err() == nil {
				// The writer has no logger of its own; the caller wires the
				// scheduler or logs the return of Capture.
				_ = err
			}
		}
	}
}

// countersWithProtocols is the in-flight snapshot, kept separate so the
// "previous" struct only holds what a row needs.
type countersWithProtocols struct {
	counters
	framesByProtocol map[string]int64
}

// snapshot reads every counter once.
func (w *Writer) snapshot() countersWithProtocols {
	set := w.Metrics
	return countersWithProtocols{
		counters: counters{
			requests:            set.Requests.Value(),
			frames:              total(set.Frames.Snapshot()),
			positions:           total(set.Positions.Snapshot()),
			alerts:              total(set.Alerts.Snapshot()),
			notifications:       set.Notifications.Snapshot(),
			geocoderRequests:    set.GeocoderRequests.Value(),
			geolocationRequests: set.GeolocationRequests.Value(),
		},
		framesByProtocol: set.Frames.Snapshot(),
	}
}

func total(values map[string]int64) int64 {
	var sum int64
	for _, value := range values {
		sum += value
	}
	return sum
}

// difference is the increment, never negative: a counter reset (a restart is the
// only way) would otherwise write a negative row.
func difference(current, previous int64) int64 {
	if current < previous {
		return current
	}
	return current - previous
}

func differences(current, previous map[string]int64) map[string]int {
	result := make(map[string]int, len(current))
	for key, value := range current {
		result[key] = int(difference(value, previous[key]))
	}
	return result
}
