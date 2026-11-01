package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// TelegramConfig configures the Telegram channel. Token is the bot token and
// BaseURL defaults to the public bot API, so a self-hosted gateway or a test
// server can be pointed at.
type TelegramConfig struct {
	Token   string
	BaseURL string
	Client  *http.Client
}

// Telegram sends the alert body to each chat id through the bot API.
type Telegram struct {
	config TelegramConfig
}

// NewTelegram creates the channel. A missing token is an error: a subscription
// that could never deliver should fail at startup, not per alert.
func NewTelegram(config TelegramConfig) (*Telegram, error) {
	if strings.TrimSpace(config.Token) == "" {
		return nil, fmt.Errorf("telegram: empty bot token")
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://api.telegram.org/bot"
	}
	// The API path is /bot<token>/sendMessage, so the base is a prefix and must
	// not end in a slash.
	config.BaseURL = strings.TrimSuffix(config.BaseURL, "/")
	return &Telegram{config: config}, nil
}

// Name implements Notifier.
func (t *Telegram) Name() string { return NotificatorTelegram }

type telegramMessage struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

// Send implements Notifier.
func (t *Telegram) Send(ctx context.Context, message Message, recipients []string) error {
	target := t.config.BaseURL + t.config.Token + "/sendMessage"
	var failures []error
	for _, chatID := range recipients {
		if chatID == "" {
			continue
		}
		err := postJSON(ctx, t.config.Client, "telegram", target,
			telegramMessage{ChatID: chatID, Text: messageBody(message)}, nil)
		if err != nil {
			failures = append(failures, err)
		}
	}
	return joinErrors("telegram", failures)
}

// messageBody is what the chat-based channels send: the formatted body, with
// the subject as a fallback for a test message without one.
func messageBody(message Message) string {
	if message.Body != "" {
		return message.Body
	}
	return message.Subject
}
