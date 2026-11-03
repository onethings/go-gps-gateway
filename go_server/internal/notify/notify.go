// Package notify delivers alerts to external channels: an HTTP webhook and
// SMTP email. Formatting, per-device routing and the send queue live here so
// the alert pipeline stays free of network I/O.
//
// Routing has three layers: the -notify-* flags define the server-wide channels
// and their defaults, devices.attributes (see AttrNotify* below) can override a
// channel for one device, and users subscribe to notification presets (type
// plus channels) through the notifications / user_notifications tables. A
// matching subscription set replaces the defaults for that device and alert;
// an explicit device attribute wins over both.
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
	// NotificatorTelegram sends a message through the Telegram bot API.
	NotificatorTelegram = "telegram"
	// NotificatorPushover sends a push notification through Pushover.
	NotificatorPushover = "pushover"
	// NotificatorSms sends a text message through an HTTP gateway or AWS SNS.
	NotificatorSms = "sms"
	// NotificatorFirebase sends a push notification through Firebase Cloud
	// Messaging.
	NotificatorFirebase = "firebase"
	// NotificatorTraccar sends a push notification through the Traccar push
	// service.
	NotificatorTraccar = "traccar"
	// NotificatorWhatsapp sends a template message through the WhatsApp Cloud
	// API.
	NotificatorWhatsapp = "whatsapp"
	// NotificatorCommand forwards the alert as a device command.
	NotificatorCommand = "command"
)

// KnownNotificators is every channel name a subscription may store.
var KnownNotificators = []string{
	NotificatorMail, NotificatorWebhook, NotificatorTelegram, NotificatorPushover,
	NotificatorSms, NotificatorFirebase, NotificatorTraccar, NotificatorWhatsapp,
	NotificatorCommand,
}

// canonicalNotificator maps the historical spellings onto the channel names the
// code uses: "email" was accepted for "mail" and "web" for "webhook".
func canonicalNotificator(value string) string {
	switch name := strings.ToLower(strings.TrimSpace(value)); name {
	case "email":
		return NotificatorMail
	case "web":
		return NotificatorWebhook
	default:
		return name
	}
}

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

// DeviceLookup resolves the device an alert belongs to. *storage.Store
// implements it, which is why notifications are only wired with -dsn.
type DeviceLookup interface {
	Device(ctx context.Context, deviceID int64) (*model.Device, error)
}

// SubscriptionLookup resolves the user subscriptions that apply to one alert
// on a device. *storage.Store implements it. The dispatcher only calls it
// when a Dispatcher was built with WithSubscriptions.
type SubscriptionLookup interface {
	DeviceSubscriptions(ctx context.Context, deviceID int64, alertType string) ([]model.NotificationSubscription, error)
}

// ValidNotificationType reports whether value may be stored as a preset type.
// "all" and "*" subscribe to every alert type the engine produces.
func ValidNotificationType(value string) bool {
	return value == "all" || value == "*" || model.IsEventType(value)
}

// ParseNotificators canonicalises and validates a notification channel list.
// "email" is accepted as an alias for "mail"; duplicates are removed. An empty
// list is an error because the preset could never deliver anything.
func ParseNotificators(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	channels := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range SplitList(value) {
			name := canonicalNotificator(part)
			if !knownNotificator(name) {
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
	Alert   *model.Event
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

// knownNotificator reports whether name is one of KnownNotificators.
func knownNotificator(name string) bool {
	for _, known := range KnownNotificators {
		if known == name {
			return true
		}
	}
	return false
}

// Channel binds a notifier to its default recipients and to the device
// attribute that overrides them.
type Channel struct {
	Notifier Notifier
	// Defaults are the server-wide targets. They apply when the device does not
	// set Attribute and nobody subscribed for the alert. A webhook subscriber
	// reuses them as the destination because the channel has no per-user URL.
	Defaults []string
	// Attribute is the devices.attributes key that overrides Defaults and any
	// subscription.
	Attribute string
	// UserTarget resolves the per-account recipients of a subscription, such as
	// telegramChatId or phone. Nil means the channel has no per-user target, so
	// a subscription only enables Defaults.
	UserTarget func(subscription model.NotificationSubscription) []string
}
