package model

import (
	"errors"
	"time"
)

// ErrNotificationNotFound is returned when an id has no notifications row.
var ErrNotificationNotFound = errors.New("notification not found")

// Notification is one row of notifications: an alert type and the channels a
// subscribed account wants it delivered on. Accounts opt in through
// user_notifications; the dispatcher expands the rows for a device into
// per-channel recipients.
type Notification struct {
	ID int64
	// Type is an alert type, or "all"/"*" for every type the engine produces.
	Type string
	// Notificators names the delivery channels (mail, telegram, sms, ...).
	Notificators []string
	Description  string
	// Always applies the preset to every device the subscribed account can
	// see. Without it the preset only applies to the devices it is linked to
	// through a device -> notification permission pair.
	Always bool
	// CommandID names a saved command the "command" notificator sends. The
	// gateway keeps command history only, so the id is stored but not resolved.
	CommandID int64
	// CalendarID limits when the preset fires. Calendars are stored but their
	// recurrence rules are not evaluated yet.
	CalendarID int64
	// Attributes holds the rule filters: alarms, geofenceIds and priority.
	Attributes Attributes
	CreatedAt  time.Time
}

// NotificationCreate holds the fields accepted when creating a subscription
// preset. Notificators must not be empty, otherwise the row can never deliver.
type NotificationCreate struct {
	Type         string
	Notificators []string
	Description  string
	Always       bool
	CommandID    int64
	CalendarID   int64
	Attributes   Attributes
}

// NotificationUpdate holds the mutable fields of a notification. Nil fields
// stay unchanged, which is what makes the PUT body a partial update.
type NotificationUpdate struct {
	Type         *string
	Notificators *[]string
	Description  *string
	Always       *bool
	CommandID    *int64
	CalendarID   *int64
	Attributes   *Attributes
}

// NotificationSubscription is one account's subscription resolved for a
// device. The dispatcher turns Notificators into channel targets; Email is the
// recipient of the mail channel.
type NotificationSubscription struct {
	UserID       int64
	Email        string
	Notificators []string
	// Attributes is the users.attributes row, which is where a per-user channel
	// target lives (telegramChatId, pushoverUserKey, phone, notificationTokens)
	// and the per-user notifyTypes override.
	Attributes Attributes
	// NotificationID names the preset this row came from; a user with several
	// matching presets appears once per preset so the dispatcher can apply the
	// per-preset filters.
	NotificationID int64
	// NotificationType is the preset type ("all" or "*" for the wildcard).
	NotificationType string
	// Always makes the preset apply to every device the account can see.
	Always bool
	// NotificationAttributes holds the preset filters (alarms, geofenceIds).
	NotificationAttributes Attributes
}
