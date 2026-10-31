// Package notify delivers alerts to external channels: an HTTP webhook and
// SMTP email. Formatting, per-device routing and the send queue live here so
// the alert pipeline stays free of network I/O.
//
// The Java server routes notifications through the notifications and
// user_notifications tables. The gateway has no such tables yet, so it uses
// server-wide channel settings (see the -notify-* flags) plus per-device
// devices.attributes overrides (see AttrNotify* below).
package notify

import (
	"context"

	"traccar-server_go/go_server/internal/model"
)

// Device attributes that override a channel for one device. They live in
// devices.attributes and are refreshed together with the alert thresholds.
const (
	// AttrNotifyTypes restricts the alert types delivered for the device. The
	// value is a comma separated list of alert types; "none" disables every
	// notification and "all" (or "*") enables every type the server delivers.
	AttrNotifyTypes = "notifyTypes"
	// AttrNotifyWebhook overrides the webhook URLs for the device. false (or
	// an empty value) disables the channel for that device.
	AttrNotifyWebhook = "notifyWebhook"
	// AttrNotifyEmail overrides the email recipients for the device. false (or
	// an empty value) disables the channel for that device.
	AttrNotifyEmail = "notifyEmail"
)

// DeviceLookup resolves the device an alert belongs to. *postgres.Store
// implements it, which is why notifications are only wired with -dsn.
type DeviceLookup interface {
	Device(ctx context.Context, deviceID int64) (*model.Device, error)
}

// Message is one formatted alert. Device is nil when the device could not be
// resolved, in which case the body only names the device id.
type Message struct {
	Alert   *model.Alert
	Device  *model.Device
	Subject string
	Body    string
}

// Notifier sends one message to the resolved recipients of a channel.
type Notifier interface {
	// Name labels the channel in logs and metrics ("webhook", "mail").
	Name() string
	// Send delivers the message to every recipient, reporting the failures
	// after trying all of them.
	Send(ctx context.Context, message Message, recipients []string) error
}

// Channel binds a notifier to its default recipients and to the device
// attribute that overrides them.
type Channel struct {
	Notifier Notifier
	// Defaults apply when the device does not set Attribute. An empty slice
	// means the channel is only used for devices that override it.
	Defaults []string
	// Attribute is the devices.attributes key that overrides Defaults.
	Attribute string
}
