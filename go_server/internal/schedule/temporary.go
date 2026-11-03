package schedule

import (
	"context"
	"errors"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// TemporaryStore is the part of the storage the temporary cleanup task needs.
type TemporaryStore interface {
	// ExpiredTemporaryUsers returns the temporary accounts whose expirationTime
	// attribute is in the past.
	ExpiredTemporaryUsers(ctx context.Context, before time.Time) ([]model.User, error)
	// DeleteUser removes an account together with its grants and tokens.
	DeleteUser(ctx context.Context, userID int64) error
}

// TemporaryCleanup deletes the accounts a share link created once their
// expiration has passed. The Java server does the same with
// TaskDeleteTemporary. Unlike Expiration, which only disables an ordinary
// account so its history survives, a share account owns nothing but the grant
// and the token, so the row goes away.
type TemporaryCleanup struct {
	Store TemporaryStore
	Now   func() time.Time
	// Report is told how many accounts a pass deleted, for the log line.
	Report func(kind string, deleted int64)
}

// Name implements Task.
func (c *TemporaryCleanup) Name() string { return "temporary" }

// Run implements Task.
func (c *TemporaryCleanup) Run(ctx context.Context) error {
	if c.Store == nil {
		return nil
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}

	users, err := c.Store.ExpiredTemporaryUsers(ctx, now)
	if err != nil {
		return err
	}

	var deleted int64
	for _, user := range users {
		if err := c.Store.DeleteUser(ctx, user.ID); err != nil {
			// An admin removing the account first is not a failure.
			if errors.Is(err, model.ErrUserNotFound) {
				continue
			}
			return err
		}
		deleted++
	}
	if c.Report != nil && deleted > 0 {
		c.Report("temporary accounts", deleted)
	}
	return nil
}
