package schedule

import (
	"context"
	"time"
)

// CleanupStore is the part of the storage the cleanup task needs.
type CleanupStore interface {
	// DeleteExpiredTokens removes the API tokens whose expiry has passed.
	DeleteExpiredTokens(ctx context.Context, before time.Time) (int64, error)
	// DeleteExpiredPasswordResets removes the reset codes that expired or were
	// already used.
	DeleteExpiredPasswordResets(ctx context.Context, before time.Time) (int64, error)
	// DeleteOldActions removes the audit trail older than before.
	DeleteOldActions(ctx context.Context, before time.Time) (int64, error)
}

// Cleanup deletes the rows nothing will read again: expired tokens, expired or
// used password reset codes and the audit trail past its retention.
type Cleanup struct {
	Store CleanupStore
	// ActionRetention keeps the audit trail this long. Zero keeps it forever,
	// which is what the Java server does until an operator sets a value.
	ActionRetention time.Duration
	Now             func() time.Time
	// Report reports how many rows each step deleted, for the log line.
	Report func(kind string, deleted int64)
}

// Name implements Task.
func (c *Cleanup) Name() string { return "cleanup" }

// Run implements Task.
func (c *Cleanup) Run(ctx context.Context) error {
	if c.Store == nil {
		return nil
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}

	deleted, err := c.Store.DeleteExpiredTokens(ctx, now)
	if err != nil {
		return err
	}
	c.report("tokens", deleted)

	deleted, err = c.Store.DeleteExpiredPasswordResets(ctx, now)
	if err != nil {
		return err
	}
	c.report("password resets", deleted)

	if c.ActionRetention > 0 {
		deleted, err = c.Store.DeleteOldActions(ctx, now.Add(-c.ActionRetention))
		if err != nil {
			return err
		}
		c.report("audit entries", deleted)
	}
	return nil
}

func (c *Cleanup) report(kind string, deleted int64) {
	if c.Report != nil && deleted > 0 {
		c.Report(kind, deleted)
	}
}
