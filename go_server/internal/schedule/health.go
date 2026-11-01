package schedule

import (
	"context"
)

// Pinger is the database health check the task needs.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Health pings the database so a broken connection shows up in the log and in
// the metrics of the health probe, instead of only when a device reports.
type Health struct {
	Pinger Pinger
	// OnResult reports whether the ping worked, so the caller can publish a
	// gauge.
	OnResult func(ok bool)
}

// Name implements Task.
func (h *Health) Name() string { return "health" }

// Run implements Task.
func (h *Health) Run(ctx context.Context) error {
	if h.Pinger == nil {
		return nil
	}
	err := h.Pinger.Ping(ctx)
	if h.OnResult != nil {
		h.OnResult(err == nil)
	}
	return err
}
