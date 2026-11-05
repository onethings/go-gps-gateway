package schedule

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
			// The temporary cleanup may delete a share account between the query
			// and this update; a row that is already gone needs no disabling.
			if errors.Is(err, model.ErrUserNotFound) {
				continue
			}
			return err
		}
	}
	return nil
}

// NoticeStore is what the expiration notices read.
type NoticeStore interface {
	UsersExpiringBetween(ctx context.Context, from, to time.Time) ([]model.User, error)
	DevicesExpiringBetween(ctx context.Context, from, to time.Time) ([]model.Device, error)
	DeviceUsers(ctx context.Context, deviceID int64) ([]model.User, error)
}

// NoticeMailer delivers one plain text notice to one account.
type NoticeMailer interface {
	SendNotice(ctx context.Context, recipient, subject, body string) error
}

// ExpirationNotice tells an account that its own subscription or one of its
// devices is about to expire, and once more when it has. It ports
// TaskExpirations: one tick is the window a notice is due in, so a notice goes
// out once and a late run loses it instead of repeating it.
type ExpirationNotice struct {
	Store  NoticeStore
	Mailer NoticeMailer
	// Users enables the account notices, Devices the device ones.
	Users   bool
	Devices bool
	// UserReminder and DeviceReminder are how long before the expiration the
	// reminder goes out. Zero leaves the reminder off.
	UserReminder   time.Duration
	DeviceReminder time.Duration
	// Tick is the window one run covers. Zero means one hour, the interval the
	// Java server schedules this task at.
	Tick time.Duration
	// Now overrides the clock, for tests.
	Now func() time.Time
}

// Name implements Task.
func (n *ExpirationNotice) Name() string { return "expirationNotice" }

// Run implements Task.
func (n *ExpirationNotice) Run(ctx context.Context) error {
	if n.Store == nil || n.Mailer == nil {
		return nil
	}
	now := time.Now()
	if n.Now != nil {
		now = n.Now()
	}
	tick := n.Tick
	if tick <= 0 {
		tick = time.Hour
	}
	previous := now.Add(-tick)

	var failures []error
	if n.Users {
		failures = append(failures, n.userNotices(ctx, previous, now)...)
	}
	if n.Devices {
		failures = append(failures, n.deviceNotices(ctx, previous, now)...)
	}
	return errors.Join(failures...)
}

const noticeDateLayout = "2006-01-02"

func (n *ExpirationNotice) userNotices(ctx context.Context, previous, now time.Time) []error {
	var failures []error

	expired, err := n.Store.UsersExpiringBetween(ctx, previous, now)
	if err != nil {
		failures = append(failures, err)
	}
	for _, user := range expired {
		failures = append(failures, n.send(ctx, user.Email,
			"Account expiration", "Your user account has expired.")...)
	}

	if n.UserReminder > 0 {
		reminders, err := n.Store.UsersExpiringBetween(ctx,
			previous.Add(n.UserReminder), now.Add(n.UserReminder))
		if err != nil {
			failures = append(failures, err)
		}
		for _, user := range reminders {
			expires, ok := attributeExpiration(user.Attributes)
			if !ok {
				continue
			}
			failures = append(failures, n.send(ctx, user.Email, "Account expiration reminder",
				"Your user account will expire on "+expires.Format(noticeDateLayout)+".")...)
		}
	}

	return failures
}

func (n *ExpirationNotice) deviceNotices(ctx context.Context, previous, now time.Time) []error {
	var failures []error

	expired, err := n.Store.DevicesExpiringBetween(ctx, previous, now)
	if err != nil {
		failures = append(failures, err)
	}
	for _, device := range expired {
		failures = append(failures, n.notifyDeviceUsers(ctx, device,
			"Device expiration", "Your device "+device.Name+" has expired.")...)
	}

	if n.DeviceReminder > 0 {
		reminders, err := n.Store.DevicesExpiringBetween(ctx,
			previous.Add(n.DeviceReminder), now.Add(n.DeviceReminder))
		if err != nil {
			failures = append(failures, err)
		}
		for _, device := range reminders {
			failures = append(failures, n.notifyDeviceUsers(ctx, device, "Device expiration reminder",
				"Your device "+device.Name+" will expire on "+
					device.ExpirationTime.Format(noticeDateLayout)+".")...)
		}
	}

	return failures
}

// notifyDeviceUsers reaches every account that holds a grant on the device,
// which is what the Java task resolves through a permission condition.
func (n *ExpirationNotice) notifyDeviceUsers(
	ctx context.Context, device model.Device, subject, body string,
) []error {
	users, err := n.Store.DeviceUsers(ctx, device.ID)
	if err != nil {
		return []error{err}
	}
	var failures []error
	for _, user := range users {
		if user.Disabled {
			continue
		}
		failures = append(failures, n.send(ctx, user.Email, subject, body)...)
	}
	return failures
}

func (n *ExpirationNotice) send(ctx context.Context, recipient, subject, body string) []error {
	if strings.TrimSpace(recipient) == "" {
		return nil
	}
	if err := n.Mailer.SendNotice(ctx, recipient, subject, body); err != nil {
		return []error{fmt.Errorf("expiration notice to %s: %w", recipient, err)}
	}
	return nil
}

// attributeExpiration reads the users.attributes expirationTime, the ISO 8601
// timestamp the REST layer writes.
func attributeExpiration(attributes model.Attributes) (time.Time, bool) {
	raw := strings.TrimSpace(attributes.GetString("expirationTime", ""))
	if raw == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, true
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
