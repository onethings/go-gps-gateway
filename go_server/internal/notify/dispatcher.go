package notify

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
)

const (
	defaultQueueSize = 256
	defaultTimeout   = 10 * time.Second
)

// Dispatcher formats alerts and forwards them to every channel that has
// recipients. Alerts are queued and delivered from one background goroutine so
// that Enqueue never blocks a device connection.
type Dispatcher struct {
	devices       DeviceLookup
	subscriptions SubscriptionLookup
	channels      []Channel
	types         map[string]struct{}
	timeout       time.Duration
	queueSize     int
	logf          func(format string, args ...any)
	meters        *metrics.Set

	queue chan *model.Event

	closeOnce sync.Once
	wg        sync.WaitGroup

	// mu guards queue once it may be closed, and the dropped counter.
	mu      sync.Mutex
	closed  bool
	dropped int64
}

// Option customises a Dispatcher.
type Option func(*Dispatcher)

// WithLogger reports lookup and delivery failures.
func WithLogger(logf func(format string, args ...any)) Option {
	return func(d *Dispatcher) {
		if logf != nil {
			d.logf = logf
		}
	}
}

// WithMetrics publishes delivered, failed and dropped notifications.
func WithMetrics(set *metrics.Set) Option {
	return func(d *Dispatcher) { d.meters = set }
}

// WithSubscriptions enables per-user routing: when an account subscribed to a
// matching notification preset for the device, the subscribed channels replace
// the server-wide defaults for that alert. Devices that set a channel
// attribute still override both.
func WithSubscriptions(subscriptions SubscriptionLookup) Option {
	return func(d *Dispatcher) { d.subscriptions = subscriptions }
}

// WithTimeout caps the device lookup and each channel delivery.
func WithTimeout(timeout time.Duration) Option {
	return func(d *Dispatcher) {
		if timeout > 0 {
			d.timeout = timeout
		}
	}
}

// WithQueueSize caps how many alerts may wait for delivery.
func WithQueueSize(size int) Option {
	return func(d *Dispatcher) {
		if size > 0 {
			d.queueSize = size
		}
	}
}

// WithTypes restricts the delivered alert types. An empty list means every
// type; "none" disables notifications and "all"/"*" re-enables every type.
func WithTypes(types []string) Option {
	return func(d *Dispatcher) {
		if len(types) == 0 {
			return
		}
		d.types = parseTypes(strings.Join(types, ","))
	}
}

// NewDispatcher starts the background sender. Channels without a notifier are
// skipped.
func NewDispatcher(devices DeviceLookup, channels []Channel, options ...Option) *Dispatcher {
	dispatcher := &Dispatcher{
		devices:   devices,
		channels:  append([]Channel(nil), channels...),
		timeout:   defaultTimeout,
		logf:      func(string, ...any) {},
		queueSize: defaultQueueSize,
	}
	for _, option := range options {
		option(dispatcher)
	}
	if dispatcher.queueSize <= 0 {
		dispatcher.queueSize = defaultQueueSize
	}
	dispatcher.queue = make(chan *model.Event, dispatcher.queueSize)

	dispatcher.wg.Add(1)
	go dispatcher.run()
	return dispatcher
}

// Enqueue queues one alert. It never blocks: when the queue is full the alert
// is dropped and counted.
func (d *Dispatcher) Enqueue(alert *model.Event) {
	if alert == nil || len(d.channels) == 0 {
		return
	}
	if !d.typeEnabled(alert.Type) {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		d.dropped++
		if d.meters != nil {
			d.meters.NotificationsDropped.Inc()
		}
		return
	}

	select {
	case d.queue <- alert:
	default:
		d.dropped++
		if d.meters != nil {
			d.meters.NotificationsDropped.Inc()
		}
		d.logf("notify: queue full, dropped %s for device %d", alert.Type, alert.DeviceID)
	}
}

// Dropped reports how many alerts were discarded: the queue was full or the
// dispatcher had already been closed.
func (d *Dispatcher) Dropped() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dropped
}

// Close stops accepting alerts, delivers everything queued and waits for the
// background sender.
func (d *Dispatcher) Close(ctx context.Context) error {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		close(d.queue)
		d.mu.Unlock()
	})

	wait := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(wait)
	}()

	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Dispatcher) run() {
	defer d.wg.Done()

	for alert := range d.queue {
		d.deliver(alert)
	}
}

func (d *Dispatcher) deliver(alert *model.Event) {
	device := d.lookup(alert.DeviceID)
	if !deviceAllows(device, alert.Type) {
		return
	}

	message := Format(alert, device)
	var attributes map[string]any
	if device != nil {
		attributes = device.Attributes
	}
	// The unfiltered set decides whether an account subscribed at all: a preset
	// that the alert does not match must silence the channel instead of
	// falling back to the server-wide defaults.
	resolved := d.subscribers(alert)
	subscriptions := d.matchingSubscriptions(alert, resolved)
	subscribed := len(resolved) > 0

	for _, channel := range d.channels {
		if channel.Notifier == nil {
			continue
		}
		targets, enabled, overridden := resolveTargets(attributes, channel)
		if !enabled && !channel.SubscriptionOnly {
			continue
		}
		// An explicit device attribute wins over every subscription. Otherwise
		// a matching subscription set decides which channels run; when nobody
		// subscribed, the server-wide defaults apply.
		if !overridden && subscribed {
			targets = subscriptionTargets(channel, subscriptions)
		}
		if len(targets) == 0 {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
		err := channel.Notifier.Send(ctx, message, targets)
		cancel()

		name := channel.Notifier.Name()
		if err != nil {
			if d.meters != nil {
				d.meters.NotificationErrors.With(name).Inc()
			}
			d.logf("notify: %s: %v", name, err)
			continue
		}
		if d.meters != nil {
			d.meters.Notifications.With(name).Inc()
		}
	}
}

// lookup resolves the device without failing the alert: a database hiccup
// still lets the webhook deliver the alert with the bare device id.
func (d *Dispatcher) lookup(deviceID int64) *model.Device {
	if d.devices == nil || deviceID == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()

	device, err := d.devices.Device(ctx, deviceID)
	if err != nil {
		d.logf("notify: lookup device %d: %v", deviceID, err)
		return nil
	}
	return device
}

// subscribers resolves the user subscriptions for the alert. A failed lookup
// is logged and treated as "nobody subscribed" so the server-wide defaults
// still deliver the alert.
func (d *Dispatcher) subscribers(alert *model.Event) []model.NotificationSubscription {
	if d.subscriptions == nil || alert == nil || alert.DeviceID == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()

	subscriptions, err := d.subscriptions.DeviceSubscriptions(ctx, alert.DeviceID, alert.Type)
	if err != nil {
		d.logf("notify: lookup subscriptions for device %d: %v", alert.DeviceID, err)
		return nil
	}
	return subscriptions
}

// matchingSubscriptions applies the per-user and per-preset filters of M4-3 to
// the subscriptions resolved for the device. A preset can restrict itself to
// certain alarms and geofences, and an account can restrict the event types it
// receives with its notifyTypes attribute.
func (d *Dispatcher) matchingSubscriptions(
	alert *model.Event, subscriptions []model.NotificationSubscription,
) []model.NotificationSubscription {
	if alert == nil {
		return nil
	}
	kept := make([]model.NotificationSubscription, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		if !userAllows(subscription.Attributes, alert.Type) {
			continue
		}
		if !presetMatches(subscription.NotificationAttributes, alert) {
			continue
		}
		kept = append(kept, subscription)
	}
	return kept
}

// userAllows applies the accounts notifyTypes attribute. An absent attribute
// leaves the server-wide and device filters as the only decision.
func userAllows(attributes model.Attributes, alertType string) bool {
	if attributes == nil {
		return true
	}
	raw, ok := attributes[AttrNotifyTypes]
	if !ok {
		return true
	}
	switch typed := raw.(type) {
	case bool:
		return typed
	case string:
		return allowedBy(parseTypes(typed), alertType)
	case []any:
		var parts []string
		for _, item := range typed {
			if text, ok := item.(string); ok {
				parts = append(parts, text)
			}
		}
		if len(parts) == 0 {
			return false
		}
		return allowedBy(parseTypes(strings.Join(parts, ",")), alertType)
	default:
		return true
	}
}

// presetMatches applies the filters a notification preset carries: a list of
// alarms for the alarm event and a list of geofence ids for geofence events.
func presetMatches(attributes model.Attributes, alert *model.Event) bool {
	if attributes == nil {
		return true
	}
	if alert.Type == model.EventAlarm {
		if raw := attributes.GetString("alarms", ""); raw != "" {
			alarm := alert.Attributes.GetString(model.KeyAlarm, "")
			matched := false
			for _, candidate := range SplitList(raw) {
				if candidate == alarm {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
	}
	if alert.GeofenceID != 0 {
		if raw := attributes.GetString("geofenceIds", ""); raw != "" {
			matched := false
			for _, candidate := range SplitList(raw) {
				if candidate == strconv.FormatInt(alert.GeofenceID, 10) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
	}
	return true
}

func (d *Dispatcher) typeEnabled(alertType string) bool {
	if d.types == nil {
		return true
	}
	if _, all := d.types["*"]; all {
		return true
	}
	_, enabled := d.types[alertType]
	return enabled
}

// deviceAllows applies the per-device notifyTypes attribute. An absent
// attribute leaves the server-wide filter as the only decision.
func deviceAllows(device *model.Device, alertType string) bool {
	if device == nil {
		return true
	}
	return userAllows(model.Attributes(device.Attributes), alertType)
}

func allowedBy(types map[string]struct{}, alertType string) bool {
	if _, all := types["*"]; all {
		return true
	}
	_, allowed := types[alertType]
	return allowed
}

// parseTypes turns "overspeed, powerCut" into a set. "none" yields an empty set
// and "all" or "*" yields the wildcard.
func parseTypes(value string) map[string]struct{} {
	types := make(map[string]struct{})
	for _, part := range SplitList(value) {
		switch strings.ToLower(part) {
		case "none":
			return map[string]struct{}{}
		case "all":
			types["*"] = struct{}{}
		default:
			types[part] = struct{}{}
		}
	}
	if len(types) == 0 {
		return map[string]struct{}{}
	}
	return types
}

// resolveTargets returns the recipients for one channel. The second result is
// false when the device disabled the channel or when no target remains. The
// third result is true when the device set the attribute, in which case
// subscriptions must not add or replace anything.
func resolveTargets(attributes map[string]any, channel Channel) ([]string, bool, bool) {
	raw, ok := attributes[channel.Attribute]
	if !ok {
		return channel.Defaults, len(channel.Defaults) > 0, false
	}

	switch typed := raw.(type) {
	case nil:
		return nil, false, true
	case bool:
		if !typed {
			return nil, false, true
		}
		return channel.Defaults, len(channel.Defaults) > 0, true
	case string:
		targets := SplitList(typed)
		return targets, len(targets) > 0, true
	case []any:
		var targets []string
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			targets = append(targets, SplitList(text)...)
		}
		return targets, len(targets) > 0, true
	case []string:
		var targets []string
		for _, item := range typed {
			targets = append(targets, SplitList(item)...)
		}
		return targets, len(targets) > 0, true
	default:
		return channel.Defaults, len(channel.Defaults) > 0, true
	}
}

// subscriptionTargets selects the recipients a channel gets from the accounts
// subscribed for this device. A channel with a per-user target takes the target
// of every subscribed account (the account email, its telegram chat id, its
// phone); a channel without one, such as the webhook, is enabled by a
// subscription and keeps its server-wide targets.
func subscriptionTargets(channel Channel, subscriptions []model.NotificationSubscription) []string {
	name := channel.Notifier.Name()
	if channel.UserTarget != nil {
		var targets []string
		for _, subscription := range subscriptions {
			if !hasNotificator(subscription, name) {
				continue
			}
			for _, target := range channel.UserTarget(subscription) {
				targets = appendRecipient(targets, target)
			}
		}
		return targets
	}
	// Mail keeps its built-in target even for a channel built without a
	// UserTarget, which is how the package worked before the other providers
	// existed and what the tests of that contract pin.
	if name == NotificatorMail {
		var targets []string
		for _, subscription := range subscriptions {
			if subscription.Email == "" || !hasNotificator(subscription, NotificatorMail) {
				continue
			}
			targets = appendRecipient(targets, subscription.Email)
		}
		return targets
	}

	for _, subscription := range subscriptions {
		if hasNotificator(subscription, name) {
			return channel.Defaults
		}
	}
	return nil
}

// hasNotificator reports whether the subscription names the channel. "email"
// stays an accepted alias for rows written directly with SQL.
func hasNotificator(subscription model.NotificationSubscription, name string) bool {
	for _, value := range subscription.Notificators {
		if canonicalNotificator(value) == name {
			return true
		}
	}
	return false
}

// appendRecipient adds a recipient unless it is already present, so a user
// with several matching presets does not receive duplicates.
func appendRecipient(recipients []string, recipient string) []string {
	for _, existing := range recipients {
		if existing == recipient {
			return recipients
		}
	}
	return append(recipients, recipient)
}

// SplitList accepts commas, semicolons and whitespace as separators so that a
// flag or devices.attributes value can be written the way it reads best.
func SplitList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		switch r {
		case ',', ';', ' ', '\t', '\n', '\r':
			return true
		default:
			return false
		}
	})

	var result []string
	for _, field := range fields {
		if field != "" {
			result = append(result, field)
		}
	}
	return result
}
