package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// TraccarPushConfig configures the Traccar push service channel, the manager
// push the official mobile client listens on.
type TraccarPushConfig struct {
	Key    string
	URL    string
	Client *http.Client
}

// TraccarPush sends one push per registration token.
type TraccarPush struct {
	config TraccarPushConfig
}

// NewTraccarPush creates the channel.
func NewTraccarPush(config TraccarPushConfig) (*TraccarPush, error) {
	if strings.TrimSpace(config.Key) == "" {
		return nil, fmt.Errorf("traccar push: empty key")
	}
	if config.URL == "" {
		config.URL = "https://www.traccar.org/push/"
	}
	return &TraccarPush{config: config}, nil
}

// Name implements Notifier.
func (t *TraccarPush) Name() string { return NotificatorTraccar }

type traccarNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Sound string `json:"sound"`
}

type traccarPushMessage struct {
	Type            string              `json:"type"`
	RegistrationIDs []string            `json:"registration_ids"`
	Notification    traccarNotification `json:"notification"`
	Priority        bool                `json:"priority"`
}

// Send implements Notifier.
func (t *TraccarPush) Send(ctx context.Context, message Message, recipients []string) error {
	var tokens []string
	for _, recipient := range recipients {
		if trimmed := strings.TrimSpace(recipient); trimmed != "" {
			tokens = append(tokens, trimmed)
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	payload := traccarPushMessage{
		Type:            "manager",
		RegistrationIDs: tokens,
		Notification: traccarNotification{
			Title: message.Subject,
			Body:  messageBody(message),
			Sound: "default",
		},
	}
	return postJSON(ctx, t.config.Client, "traccar push", t.config.URL, payload,
		map[string]string{"Authorization": "key=" + t.config.Key})
}
