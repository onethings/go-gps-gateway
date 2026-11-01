package schedule

import (
	"context"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// ExpirationStore is the part of the storage the expiration task needs.
type ExpirationStore interface {
	// ExpiredUsers returns the enabled users whose expirationTime attribute is
	// in the past.
	ExpiredUsers(ctx context.Context, before time.Time) ([]model.User, error)
	// SetUserDisabled flips users.disabled.
	SetUserDisabled(ctx context.Context, userID int64, disabled bool) error
}

// Expiration disables the accounts whose expirationTime attribute has passed.
// The Java server does the same with its ExpirationTask; deleting the rows would
// lose the history, so a user is only disabled.
type Expiration struct {
	Store ExpirationStore
	Now   func() time.Time
}

// Name implements Task.
func (e *Expiration) Name() string { return "expiration" }

// Run implements Task.
func (e *Expiration) Run(ctx context.Context) error {
	if e.Store == nil {
		return nil
	}
	now := time.Now()
	if e.Now != nil {
		now = e.Now()
	}

	users, err := e.Store.ExpiredUsers(ctx, now)
	if err != nil {
		return err
	}
	for _, user := range users {
		if err := e.Store.SetUserDisabled(ctx, user.ID, true); err != nil {
			return err
		}
	}
	return nil
}
