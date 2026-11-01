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
	// Notificators names the delivery channels: "mail" and "webhook".
	Notificators []string
	Description  string
	CreatedAt    time.Time
}

// NotificationCreate holds the fields accepted when creating a subscription
// preset. Notificators must not be empty, otherwise the row can never deliver.
type NotificationCreate struct {
	Type         string
	Notificators []string
	Description  string
}

// NotificationUpdate holds the mutable fields of a notification. Nil fields
// stay unchanged, which is what makes the PUT body a partial update.
type NotificationUpdate struct {
	Type         *string
	Notificators *[]string
	Description  *string
}

// NotificationSubscription is one account's subscription resolved for a
// device. The dispatcher turns Notificators into channel targets; Email is the
// recipient of the mail channel.
type NotificationSubscription struct {
	UserID       int64
	Email        string
	Notificators []string
	// Attributes is the users.attributes row, which is where a per-user channel
	// target lives (telegramChatId, pushoverUserKey, phone, notificationTokens).
	Attributes Attributes
}
