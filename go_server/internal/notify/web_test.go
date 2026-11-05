package notify

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"traccar-server_go/go_server/internal/model"
)

type push struct {
	userID int64
	event  *model.Event
}

type recordingPusher struct {
	mu     sync.Mutex
	pushes []push
}

func (p *recordingPusher) PushEventToUser(userID int64, event *model.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pushes = append(p.pushes, push{userID: userID, event: event})
}

func (p *recordingPusher) snapshot() []push {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]push(nil), p.pushes...)
}

func TestWebSendPushesTheRenderedMessage(t *testing.T) {
	pusher := &recordingPusher{}
	web := NewWeb(pusher)

	alert := testAlert(model.EventDeviceOverspeed, nil)

	err := web.Send(context.Background(), Message{
		Alert:   alert,
		Subject: "[traccar] deviceOverspeed",
		Body:    "Device: truck",
	}, []string{"1", " 2 "})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	pushes := pusher.snapshot()
	if len(pushes) != 2 {
		t.Fatalf("pushes = %d, want 2", len(pushes))
	}
	for index, want := range []int64{1, 2} {
		if pushes[index].userID != want {
			t.Errorf("push %d user = %d, want %d", index, pushes[index].userID, want)
		}
		if got := pushes[index].event.Attributes.GetString(AttributeMessage, ""); got != "Device: truck" {
			t.Errorf("push %d message = %q", index, got)
		}
	}

	// The alert is shared with the other channels, so the rendered text must
	// not leak into it.
	if alert.Attributes.Has(AttributeMessage) {
		t.Error("Send mutated the shared alert attributes")
	}
}

func TestWebSendSkipsUnusableRecipients(t *testing.T) {
	pusher := &recordingPusher{}
	web := NewWeb(pusher)

	if err := web.Send(context.Background(), Message{
		Alert: testAlert(model.EventDeviceOverspeed, nil), Body: "text",
	}, []string{"", "not-a-number", "0", "-3"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := len(pusher.snapshot()); got != 0 {
		t.Errorf("pushes = %d, want 0", got)
	}

	if err := web.Send(context.Background(), Message{Subject: "test", Body: "text"}, []string{"1"}); err != nil {
		t.Fatalf("Send without an alert: %v", err)
	}
	if got := len(pusher.snapshot()); got != 0 {
		t.Errorf("pushes without an alert = %d, want 0", got)
	}
}

func TestWebSendWithoutPusherIsNoop(t *testing.T) {
	if err := NewWeb(nil).Send(context.Background(), Message{
		Alert: testAlert(model.EventDeviceOverspeed, nil), Body: "text",
	}, []string{"1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// TestDispatcherWebChannelNeedsSubscription pins what the channel means: it has
// no server-wide target, so only an account whose preset names "web" receives
// the popup.
func TestDispatcherWebChannelNeedsSubscription(t *testing.T) {
	pusher := &recordingPusher{}
	mailer := &recordingNotifier{name: NotificatorMail}
	channel := Channel{
		Notifier:  NewWeb(pusher),
		Attribute: "notifyWeb",
		UserTarget: func(subscription model.NotificationSubscription) []string {
			return []string{strconv.FormatInt(subscription.UserID, 10)}
		},
		SubscriptionOnly: true,
	}

	subscribed := &fakeSubscriptions{subscriptions: []model.NotificationSubscription{
		{UserID: 4, Email: "alice@example.test", Notificators: []string{NotificatorWeb}},
	}}
	dispatcher := NewDispatcher(&fakeLookup{devices: map[int64]*model.Device{7: testDevice()}},
		[]Channel{channel, {Notifier: mailer, Defaults: []string{"ops@example.com"}, Attribute: AttrNotifyEmail}},
		WithSubscriptions(subscribed))
	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, nil))
	closeDispatcher(t, dispatcher)

	pushes := pusher.snapshot()
	if len(pushes) != 1 || pushes[0].userID != 4 {
		t.Fatalf("pushes = %+v, want one for user 4", pushes)
	}

	empty := &recordingPusher{}
	unsubscribed := NewDispatcher(&fakeLookup{devices: map[int64]*model.Device{7: testDevice()}},
		[]Channel{{
			Notifier:  NewWeb(empty),
			Attribute: "notifyWeb",
			UserTarget: func(subscription model.NotificationSubscription) []string {
				return []string{strconv.FormatInt(subscription.UserID, 10)}
			},
			SubscriptionOnly: true,
		}}, WithSubscriptions(&fakeSubscriptions{}))
	unsubscribed.Enqueue(testAlert(model.EventDeviceOverspeed, nil))
	closeDispatcher(t, unsubscribed)

	if got := len(empty.snapshot()); got != 0 {
		t.Errorf("pushes without a subscription = %d, want 0", got)
	}
}

// TestDispatcherWebChannelRespectsDeviceAttribute keeps the per-device switch
// working: notifyWeb=false silences the popup for that device.
func TestDispatcherWebChannelRespectsDeviceAttribute(t *testing.T) {
	device := testDevice()
	device.Attributes = map[string]any{"notifyWeb": false}

	pusher := &recordingPusher{}
	dispatcher := NewDispatcher(&fakeLookup{devices: map[int64]*model.Device{7: device}},
		[]Channel{{
			Notifier:  NewWeb(pusher),
			Attribute: "notifyWeb",
			UserTarget: func(subscription model.NotificationSubscription) []string {
				return []string{strconv.FormatInt(subscription.UserID, 10)}
			},
			SubscriptionOnly: true,
		}}, WithSubscriptions(&fakeSubscriptions{subscriptions: []model.NotificationSubscription{
			{UserID: 4, Notificators: []string{NotificatorWeb}},
		}}))
	dispatcher.Enqueue(testAlert(model.EventDeviceOverspeed, nil))
	closeDispatcher(t, dispatcher)

	if got := len(pusher.snapshot()); got != 0 {
		t.Errorf("pushes = %d, want 0 for a disabled device", got)
	}
}
