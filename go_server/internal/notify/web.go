package notify

import (
	"context"
	"strconv"
	"strings"

	"traccar-server_go/go_server/internal/model"
)

// WebPusher delivers an alert to the web sessions of one account. The API
// socket hub implements it, which keeps this package independent of the HTTP
// layer.
type WebPusher interface {
	PushEventToUser(userID int64, event *model.Event)
}

// Web is the "web" channel: the alert goes to the browser sessions the account
// has open, carrying the same formatted text the other channels send. It ports
// org.traccar.notificators.NotificatorWeb.
type Web struct {
	pusher WebPusher
}

// NewWeb creates the channel. A nil pusher makes Send a no-op, which is what
// the gateway uses when the API server is not running.
func NewWeb(pusher WebPusher) *Web {
	return &Web{pusher: pusher}
}

// Name implements Notifier.
func (w *Web) Name() string { return NotificatorWeb }

// Send pushes the alert to every recipient's sessions. Recipients are account
// ids, which is what the channel's UserTarget produces. A message without an
// alert, which is what the test button and an announcement carry, has no event
// to show and is skipped the way the Java base class skips it.
func (w *Web) Send(_ context.Context, message Message, recipients []string) error {
	if w == nil || w.pusher == nil || message.Alert == nil {
		return nil
	}

	digest := message.Body
	if strings.TrimSpace(digest) == "" {
		digest = message.Subject
	}
	if digest == "" {
		return nil
	}

	for _, recipient := range recipients {
		userID, err := strconv.ParseInt(strings.TrimSpace(recipient), 10, 64)
		if err != nil || userID <= 0 {
			continue
		}
		event := *message.Alert
		event.Attributes = withMessage(message.Alert.Attributes, digest)
		w.pusher.PushEventToUser(userID, &event)
	}
	return nil
}

// withMessage copies the alert attributes and adds the rendered text, so the
// event shared with the other channels is never mutated.
func withMessage(attributes model.Attributes, digest string) model.Attributes {
	copied := make(model.Attributes, len(attributes)+1)
	for key, value := range attributes {
		copied[key] = value
	}
	copied.Set(AttributeMessage, digest)
	return copied
}
