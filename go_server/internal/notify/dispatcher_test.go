package notify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
)

type fakeLookup struct {
	devices map[int64]*model.Device
	err     error
}

func (f *fakeLookup) Device(_ context.Context, deviceID int64) (*model.Device, error) {
	if f.err != nil {
		return nil, f.err
	}
	device, ok := f.devices[deviceID]
	if !ok {
		return nil, model.ErrDeviceNotFound
	}
	return device, nil
}

type delivery struct {
	message    Message
	recipients []string
}

type recordingNotifier struct {
	name  string
	err   error
	mu    sync.Mutex
	calls []delivery
}

func (n *recordingNotifier) Name() string { return n.name }

func (n *recordingNotifier) Send(_ context.Context, message Message, recipients []string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, delivery{
		message:    message,
		recipients: append([]string(nil), recipients...),
	})
	return n.err
}

func (n *recordingNotifier) snapshot() []delivery {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]delivery(nil), n.calls...)
}

func closeDispatcher(t *testing.T, dispatcher *Dispatcher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := dispatcher.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestDispatcherDeliversToEveryChannel(t *testing.T) {
	webhook := &recordingNotifier{name: "webhook"}
	mailer := &recordingNotifier{name: "mail"}
	dispatcher := NewDispatcher(&fakeLookup{devices: map[int64]*model.Device{7: testDevice()}},
		[]Channel{
			{Notifier: webhook, Defaults: []string{"https://example.com/hook"}, Attribute: AttrNotifyWebhook},
			{Notifier: mailer, Defaults: []string{"ops@example.com"}, Attribute: AttrNotifyEmail},
		})

	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, map[string]any{"speed": 31.5, "limit": float64(30)}))
	closeDispatcher(t, dispatcher)

	for _, notifier := range []*recordingNotifier{webhook, mailer} {
		calls := notifier.snapshot()
		if len(calls) != 1 {
			t.Fatalf("%s calls = %d, want 1", notifier.name, len(calls))
		}
		if calls[0].message.Subject == "" || calls[0].message.Body == "" {
			t.Errorf("%s got an empty message", notifier.name)
		}
	}

	if got := webhook.snapshot()[0].recipients; len(got) != 1 || got[0] != "https://example.com/hook" {
		t.Errorf("webhook recipients = %v", got)
	}
	if got := mailer.snapshot()[0].recipients; len(got) != 1 || got[0] != "ops@example.com" {
		t.Errorf("mail recipients = %v", got)
	}
}

func TestDispatcherTypeFilter(t *testing.T) {
	notifier := &recordingNotifier{name: "webhook"}
	dispatcher := NewDispatcher(&fakeLookup{devices: map[int64]*model.Device{7: testDevice()}},
		[]Channel{{Notifier: notifier, Defaults: []string{"https://example.com/hook"}, Attribute: AttrNotifyWebhook}},
		WithTypes([]string{model.EventPowerCut}))

	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, nil))
	dispatcher.Enqueue(testAlert(model.EventPowerCut, nil))
	closeDispatcher(t, dispatcher)

	calls := notifier.snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if got := calls[0].message.Alert.Type; got != model.EventPowerCut {
		t.Errorf("type = %q", got)
	}
}

func TestDispatcherPerDeviceRouting(t *testing.T) {
	device := testDevice()
	device.Attributes = map[string]any{
		AttrNotifyTypes:   "powerCut, geofenceEnter",
		AttrNotifyWebhook: "https://device.example/hook",
		AttrNotifyEmail:   false,
	}

	webhook := &recordingNotifier{name: "webhook"}
	mailer := &recordingNotifier{name: "mail"}
	dispatcher := NewDispatcher(&fakeLookup{devices: map[int64]*model.Device{7: device}},
		[]Channel{
			{Notifier: webhook, Defaults: []string{"https://global.example/hook"}, Attribute: AttrNotifyWebhook},
			{Notifier: mailer, Defaults: []string{"ops@example.com"}, Attribute: AttrNotifyEmail},
		})

	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, nil))
	dispatcher.Enqueue(testAlert(model.EventPowerCut, nil))
	closeDispatcher(t, dispatcher)

	webhookCalls := webhook.snapshot()
	if len(webhookCalls) != 1 {
		t.Fatalf("webhook calls = %d, want 1", len(webhookCalls))
	}
	if got := webhookCalls[0].message.Alert.Type; got != model.EventPowerCut {
		t.Errorf("webhook type = %q", got)
	}
	if got := webhookCalls[0].recipients; len(got) != 1 || got[0] != "https://device.example/hook" {
		t.Errorf("webhook recipients = %v", got)
	}
	if calls := mailer.snapshot(); len(calls) != 0 {
		t.Errorf("mail calls = %d, want 0", len(calls))
	}
}

func TestDispatcherNotifyTypesNone(t *testing.T) {
	device := testDevice()
	device.Attributes = map[string]any{AttrNotifyTypes: "none"}

	notifier := &recordingNotifier{name: "webhook"}
	dispatcher := NewDispatcher(&fakeLookup{devices: map[int64]*model.Device{7: device}},
		[]Channel{{Notifier: notifier, Defaults: []string{"https://example.com/hook"}, Attribute: AttrNotifyWebhook}})

	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, nil))
	closeDispatcher(t, dispatcher)

	if calls := notifier.snapshot(); len(calls) != 0 {
		t.Errorf("calls = %d, want 0", len(calls))
	}
}

func TestDispatcherNotifyTypesArray(t *testing.T) {
	device := testDevice()
	device.Attributes = map[string]any{AttrNotifyTypes: []any{"geofenceEnter", "geofenceExit"}}

	notifier := &recordingNotifier{name: "webhook"}
	dispatcher := NewDispatcher(&fakeLookup{devices: map[int64]*model.Device{7: device}},
		[]Channel{{Notifier: notifier, Defaults: []string{"https://example.com/hook"}, Attribute: AttrNotifyWebhook}})

	dispatcher.Enqueue(testAlert(model.EventGeofenceExit, map[string]any{"geofence": "depot"}))
	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, nil))
	closeDispatcher(t, dispatcher)

	calls := notifier.snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if got := calls[0].message.Alert.Type; got != model.EventGeofenceExit {
		t.Errorf("type = %q", got)
	}
}

func TestDispatcherLookupFailureStillDelivers(t *testing.T) {
	notifier := &recordingNotifier{name: "webhook"}
	var logged bool
	dispatcher := NewDispatcher(&fakeLookup{err: errors.New("database down")},
		[]Channel{{Notifier: notifier, Defaults: []string{"https://example.com/hook"}, Attribute: AttrNotifyWebhook}},
		WithLogger(func(string, ...any) { logged = true }))

	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, nil))
	closeDispatcher(t, dispatcher)

	if calls := notifier.snapshot(); len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if !logged {
		t.Error("expected the lookup failure to be logged")
	}
}

func TestDispatcherMetrics(t *testing.T) {
	meters := metrics.New()
	webhook := &recordingNotifier{name: "webhook"}
	mailer := &recordingNotifier{name: "mail", err: errors.New("smtp down")}

	dispatcher := NewDispatcher(&fakeLookup{devices: map[int64]*model.Device{7: testDevice()}},
		[]Channel{
			{Notifier: webhook, Defaults: []string{"https://example.com/hook"}, Attribute: AttrNotifyWebhook},
			{Notifier: mailer, Defaults: []string{"ops@example.com"}, Attribute: AttrNotifyEmail},
		}, WithMetrics(meters), WithLogger(func(string, ...any) {}))

	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, nil))
	closeDispatcher(t, dispatcher)

	if got := meters.Notifications.With("webhook").Value(); got != 1 {
		t.Errorf("webhook deliveries = %d, want 1", got)
	}
	if got := meters.NotificationErrors.With("mail").Value(); got != 1 {
		t.Errorf("mail errors = %d, want 1", got)
	}
}

type blockingNotifier struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

func newBlockingNotifier() *blockingNotifier {
	return &blockingNotifier{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (n *blockingNotifier) Name() string { return "blocking" }

func (n *blockingNotifier) Send(ctx context.Context, _ Message, _ []string) error {
	n.mu.Lock()
	n.calls++
	n.mu.Unlock()

	n.once.Do(func() { close(n.started) })

	select {
	case <-n.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestDispatcherDropsWhenQueueFull(t *testing.T) {
	notifier := newBlockingNotifier()
	meters := metrics.New()
	dispatcher := NewDispatcher(nil,
		[]Channel{{Notifier: notifier, Defaults: []string{"https://example.com/hook"}, Attribute: AttrNotifyWebhook}},
		WithQueueSize(1),
		WithTimeout(5*time.Second),
		WithMetrics(meters),
		WithLogger(func(string, ...any) {}))

	alert := testAlert(model.EventDeviceOverspeed, nil)
	dispatcher.Enqueue(alert)

	select {
	case <-notifier.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first alert was never delivered")
	}

	dispatcher.Enqueue(alert)
	dispatcher.Enqueue(alert)

	if got := dispatcher.Dropped(); got != 1 {
		t.Errorf("dropped = %d, want 1", got)
	}
	if got := meters.NotificationsDropped.Value(); got != 1 {
		t.Errorf("dropped metric = %d, want 1", got)
	}

	close(notifier.release)
	closeDispatcher(t, dispatcher)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if notifier.calls != 2 {
		t.Errorf("calls = %d, want 2", notifier.calls)
	}
}

func TestDispatcherIgnoresEnqueueAfterClose(t *testing.T) {
	notifier := &recordingNotifier{name: "webhook"}
	dispatcher := NewDispatcher(nil,
		[]Channel{{Notifier: notifier, Defaults: []string{"https://example.com/hook"}, Attribute: AttrNotifyWebhook}})

	closeDispatcher(t, dispatcher)
	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, nil))

	if got := dispatcher.Dropped(); got != 1 {
		t.Errorf("dropped = %d, want 1", got)
	}
	if calls := notifier.snapshot(); len(calls) != 0 {
		t.Errorf("calls = %d, want 0", len(calls))
	}
}

func TestDispatcherWithoutChannelsDoesNothing(t *testing.T) {
	dispatcher := NewDispatcher(nil, nil)
	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, nil))
	closeDispatcher(t, dispatcher)

	if got := dispatcher.Dropped(); got != 0 {
		t.Errorf("dropped = %d, want 0", got)
	}
}

type fakeSubscriptions struct {
	mu            sync.Mutex
	subscriptions []model.NotificationSubscription
	err           error
	calls         int
}

func (f *fakeSubscriptions) DeviceSubscriptions(_ context.Context, _ int64, _ string) ([]model.NotificationSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return append([]model.NotificationSubscription(nil), f.subscriptions...), nil
}

func TestDispatcherSubscriptionRouting(t *testing.T) {
	webhook := &recordingNotifier{name: "webhook"}
	mailer := &recordingNotifier{name: "mail"}
	subscriptions := &fakeSubscriptions{subscriptions: []model.NotificationSubscription{
		{UserID: 1, Email: "alice@example.test", Notificators: []string{"mail"}},
		{UserID: 2, Email: "bob@example.test", Notificators: []string{"webhook", "mail"}},
	}}

	dispatcher := NewDispatcher(&fakeLookup{devices: map[int64]*model.Device{7: testDevice()}},
		[]Channel{
			{Notifier: webhook, Defaults: []string{"https://global.example/hook"}, Attribute: AttrNotifyWebhook},
			{Notifier: mailer, Defaults: []string{"ops@example.com"}, Attribute: AttrNotifyEmail},
		}, WithSubscriptions(subscriptions))

	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, nil))
	closeDispatcher(t, dispatcher)

	mailCalls := mailer.snapshot()
	if len(mailCalls) != 1 {
		t.Fatalf("mail calls = %d, want 1", len(mailCalls))
	}
	// The subscription set replaces the server default "ops@example.com".
	if got := mailCalls[0].recipients; strings.Join(got, ",") != "alice@example.test,bob@example.test" {
		t.Errorf("mail recipients = %v", got)
	}

	webhookCalls := webhook.snapshot()
	if len(webhookCalls) != 1 {
		t.Fatalf("webhook calls = %d, want 1", len(webhookCalls))
	}
	if got := webhookCalls[0].recipients; len(got) != 1 || got[0] != "https://global.example/hook" {
		t.Errorf("webhook recipients = %v", got)
	}
}

func TestDispatcherSubscriptionsReplaceDefaults(t *testing.T) {
	webhook := &recordingNotifier{name: "webhook"}
	mailer := &recordingNotifier{name: "mail"}
	subscriptions := &fakeSubscriptions{subscriptions: []model.NotificationSubscription{
		{UserID: 1, Email: "alice@example.test", Notificators: []string{"mail"}},
	}}

	dispatcher := NewDispatcher(&fakeLookup{devices: map[int64]*model.Device{7: testDevice()}},
		[]Channel{
			{Notifier: webhook, Defaults: []string{"https://global.example/hook"}, Attribute: AttrNotifyWebhook},
			{Notifier: mailer, Defaults: []string{"ops@example.com"}, Attribute: AttrNotifyEmail},
		}, WithSubscriptions(subscriptions))

	dispatcher.Enqueue(testAlert(model.EventPowerCut, nil))
	closeDispatcher(t, dispatcher)

	if calls := webhook.snapshot(); len(calls) != 0 {
		t.Errorf("webhook calls = %d, want 0: nobody subscribed to webhook", len(calls))
	}
	mailCalls := mailer.snapshot()
	if len(mailCalls) != 1 || len(mailCalls[0].recipients) != 1 ||
		mailCalls[0].recipients[0] != "alice@example.test" {
		t.Fatalf("mail calls = %+v", mailCalls)
	}
}

func TestDispatcherDeviceAttributeBeatsSubscriptions(t *testing.T) {
	device := testDevice()
	device.Attributes = map[string]any{AttrNotifyEmail: "device@example.test"}

	webhook := &recordingNotifier{name: "webhook"}
	mailer := &recordingNotifier{name: "mail"}
	subscriptions := &fakeSubscriptions{subscriptions: []model.NotificationSubscription{
		{UserID: 1, Email: "alice@example.test", Notificators: []string{"mail", "webhook"}},
	}}

	dispatcher := NewDispatcher(&fakeLookup{devices: map[int64]*model.Device{7: device}},
		[]Channel{
			{Notifier: webhook, Defaults: []string{"https://global.example/hook"}, Attribute: AttrNotifyWebhook},
			{Notifier: mailer, Defaults: []string{"ops@example.com"}, Attribute: AttrNotifyEmail},
		}, WithSubscriptions(subscriptions))

	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, nil))
	closeDispatcher(t, dispatcher)

	mailCalls := mailer.snapshot()
	if len(mailCalls) != 1 || len(mailCalls[0].recipients) != 1 ||
		mailCalls[0].recipients[0] != "device@example.test" {
		t.Errorf("mail calls = %+v", mailCalls)
	}
	// The webhook attribute is absent, but the subscription set controls it;
	// the subscriber did choose webhook, so the server default is used once.
	webhookCalls := webhook.snapshot()
	if len(webhookCalls) != 1 {
		t.Errorf("webhook calls = %d, want 1", len(webhookCalls))
	}
}

func TestDispatcherSubscriptionLookupFailureFallsBack(t *testing.T) {
	notifier := &recordingNotifier{name: "webhook"}
	var logged bool
	dispatcher := NewDispatcher(&fakeLookup{devices: map[int64]*model.Device{7: testDevice()}},
		[]Channel{{Notifier: notifier, Defaults: []string{"https://global.example/hook"}, Attribute: AttrNotifyWebhook}},
		WithSubscriptions(&fakeSubscriptions{err: errors.New("database down")}),
		WithLogger(func(string, ...any) { logged = true }))

	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, nil))
	closeDispatcher(t, dispatcher)

	calls := notifier.snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if got := calls[0].recipients; len(got) != 1 || got[0] != "https://global.example/hook" {
		t.Errorf("recipients = %v", got)
	}
	if !logged {
		t.Error("expected the subscription lookup failure to be logged")
	}
}
