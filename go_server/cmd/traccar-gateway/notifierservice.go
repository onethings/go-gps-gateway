package main

import (
	"context"
	"fmt"

	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/notify"
)

// notifierService exposes the configured channels to the REST layer, which
// needs them for the test button and the announcement page.
type notifierService struct {
	channels []notify.Channel
}

// Channels implements api.NotificationSender.
func (n notifierService) Channels() []string {
	names := make([]string, 0, len(n.channels))
	for _, channel := range n.channels {
		names = append(names, channel.Notifier.Name())
	}
	return names
}

// SendTo implements api.NotificationSender. A channel with a per-user target
// sends to that target; a channel without one sends to its server-wide targets.
func (n notifierService) SendTo(
	ctx context.Context, name string, user model.User, subject, body string,
) error {
	for _, channel := range n.channels {
		if channel.Notifier.Name() != name {
			continue
		}
		targets := channel.Defaults
		if channel.UserTarget != nil {
			targets = channel.UserTarget(model.NotificationSubscription{
				UserID:       user.ID,
				Email:        user.Email,
				Attributes:   user.Attributes,
				Notificators: []string{name},
			})
		}
		if len(targets) == 0 {
			return nil
		}
		return channel.Notifier.Send(ctx, notify.Message{Subject: subject, Body: body}, targets)
	}
	return fmt.Errorf("unknown notificator %q", name)
}
