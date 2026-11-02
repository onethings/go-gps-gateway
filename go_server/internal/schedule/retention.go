package schedule

import (
	"context"
	"time"
)

// Retention deletes rows of one table when they are older than its retention.
// It is used for the events, positions and command history, so the three tasks
// share one implementation and differ only in the delete function they are
// given.
type Retention struct {
	// Label is the task name, for example "events".
	Label string
	// Keep is how long a row is kept. Zero or less turns the task off, which is
	// the safe default: deleting history must be asked for.
	Keep time.Duration
	// Delete removes the rows older than the supplied time and reports how many
	// it removed.
	Delete func(ctx context.Context, before time.Time) (int64, error)
	// Report is told how many rows a pass deleted, for the log line.
	Report func(kind string, deleted int64)
	Now    func() time.Time
}

// Name implements Task.
func (r *Retention) Name() string { return r.Label }

// Run implements Task.
func (r *Retention) Run(ctx context.Context) error {
	if r.Delete == nil || r.Keep <= 0 {
		return nil
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	deleted, err := r.Delete(ctx, now.Add(-r.Keep))
	if err != nil {
		return err
	}
	if r.Report != nil && deleted > 0 {
		r.Report(r.Label, deleted)
	}
	return nil
}

// DeviceExpirationStore is the part of the storage the device expiration task
// needs.
type DeviceExpirationStore interface {
	// DisableExpiredDevices flips the disabled flag of every device whose
	// expiration time has passed and reports how many it changed.
	DisableExpiredDevices(ctx context.Context, before time.Time) (int64, error)
}

// DeviceExpiration suspends the devices whose expirationTime has passed. The
// Java server does the same with DeviceExpirationTask; deleting the row would
// lose the history, so the device is only disabled.
type DeviceExpiration struct {
	Store DeviceExpirationStore
	Now   func() time.Time
}

// Name implements Task.
func (d *DeviceExpiration) Name() string { return "deviceExpiration" }

// Run implements Task.
func (d *DeviceExpiration) Run(ctx context.Context) error {
	if d.Store == nil {
		return nil
	}
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	_, err := d.Store.DisableExpiredDevices(ctx, now)
	return err
}

// RetentionStore is the storage side of the events, positions and command
// history cleanup. The command history is keyed by age rather than a timestamp,
// which is why the scheduler adapts it before handing it to Retention.
type RetentionStore interface {
	DeleteEventsBefore(ctx context.Context, before time.Time) (int64, error)
	DeletePositionsBefore(ctx context.Context, before time.Time) (int64, error)
	DeleteCommandsBefore(ctx context.Context, age time.Duration) (int64, error)
}
