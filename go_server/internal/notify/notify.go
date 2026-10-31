// Package notify delivers alerts to external channels: an HTTP webhook and
// SMTP email. Formatting, per-device routing and the send queue live here so
// the alert pipeline stays free of network I/O.
//
// Routing has three layers. Users subscribe to a notification preset (a type
// plus channels) through the notifications and user_notifications tables, per
// device attributes (see AttrNotify* below) override a channel for one device,
// and the -notify-* flags provide the server-wide defaults a device or its
// subscribers fall back to.
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"traccar-server_go/go_server/internal/model"
)

// Notificator names one delivery channel. The values match Channel.Notifier
// Name(), which is what subscriptions store in notifications.notificators.
const (
	// NotificatorMail delivers to the account email.
	NotificatorMail = "mail"
	// NotificatorWebhook delivers to the server-wide webhook URLs.
	NotificatorWebhook = "webhook"
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

// SubscriptionLookup resolves the user subscriptions that apply to one alert
// on a device. *postgres.Store implements it. The dispatcher only calls it
// when a Dispatcher was built with WithSubscriptions.
type SubscriptionLookup interface {
	DeviceSubscriptions(ctx context.Context, deviceID int64, alertType string) ([]model.NotificationSubscription, error)
}

// ValidNotificationType reports whether value may be stored as a preset type.
// "all" and "*" subscribe to every alert type the engine produces.
func ValidNotificationType(value string) bool {
	return value == "all" || value == "*" || model.IsAlertType(value)
}

// ParseNotificators canonicalises and validates a notification channel list.
// "email" is accepted as an alias for "mail"; duplicates are removed. An empty
// list is an error because the preset could never deliver anything.
func ParseNotificators(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	channels := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range SplitList(value) {
			name := strings.ToLower(part)
			if name == "email" {
				name = NotificatorMail
			}
			switch name {
			case NotificatorMail, NotificatorWebhook:
			default:
				return nil, fmt.Errorf("unknown notification channel %q", part)
			}
			if _, duplicate := seen[name]; duplicate {
				continue
			}
			seen[name] = struct{}{}
			channels = append(channels, name)
		}
	}
	if len(channels) == 0 {
		return nil, errors.New("at least one notification channel is required")
	}
	return channels, nil
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
