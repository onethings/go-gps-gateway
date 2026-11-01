package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// PushoverConfig configures the Pushover channel. Token is the application
// token; User is the fallback user key when an account did not set
// pushoverUserKey.
type PushoverConfig struct {
	Token  string
	User   string
	URL    string
	Client *http.Client
}

// Pushover sends one push notification per recipient.
type Pushover struct {
	config PushoverConfig
}

// NewPushover creates the channel. The application token is required; the user
// key may come per recipient.
func NewPushover(config PushoverConfig) (*Pushover, error) {
	if strings.TrimSpace(config.Token) == "" {
		return nil, fmt.Errorf("pushover: empty application token")
	}
	if config.URL == "" {
		config.URL = "https://api.pushover.net/1/messages.json"
	}
	return &Pushover{config: config}, nil
}

// Name implements Notifier.
func (p *Pushover) Name() string { return NotificatorPushover }

type pushoverMessage struct {
	Token   string `json:"token"`
	User    string `json:"user"`
	Device  string `json:"device,omitempty"`
	Title   string `json:"title,omitempty"`
	Message string `json:"message"`
}

// Send implements Notifier.
func (p *Pushover) Send(ctx context.Context, message Message, recipients []string) error {
	if len(recipients) == 0 && p.config.User != "" {
		recipients = []string{p.config.User}
	}
	var failures []error
	for _, recipient := range recipients {
		user, device := splitPushoverTarget(recipient)
		if user == "" {
			user = p.config.User
		}
		if user == "" {
			continue
		}
		payload := pushoverMessage{
			Token:   p.config.Token,
			User:    user,
			Device:  device,
			Title:   message.Subject,
			Message: messageBody(message),
		}
		if err := postJSON(ctx, p.config.Client, "pushover", p.config.URL, payload, nil); err != nil {
			failures = append(failures, err)
		}
	}
	return joinErrors("pushover", failures)
}

// splitPushoverTarget accepts "user" and "user@device", which is how the Java
// server lets an account narrow the push to one device.
func splitPushoverTarget(target string) (string, string) {
	user, device, found := strings.Cut(target, "@")
	if !found {
		return strings.TrimSpace(target), ""
	}
	return strings.TrimSpace(user), strings.TrimSpace(device)
}
