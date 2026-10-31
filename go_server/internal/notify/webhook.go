package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WebhookPayload is the JSON body sent to every webhook URL. Field names match
// the REST responses so that a consumer can reuse the same decoding.
type WebhookPayload struct {
	Type       string         `json:"type"`
	DeviceID   int64          `json:"deviceId"`
	UniqueID   string         `json:"uniqueId,omitempty"`
	DeviceName string         `json:"deviceName,omitempty"`
	Protocol   string         `json:"protocol,omitempty"`
	Time       time.Time      `json:"time"`
	GeofenceID int64          `json:"geofenceId,omitempty"`
	Subject    string         `json:"subject"`
	Message    string         `json:"message"`
	Attributes map[string]any `json:"attributes"`
}

// WebhookConfig configures the HTTP channel. The URLs themselves come from the
// channel defaults and the devices.attributes override.
type WebhookConfig struct {
	// Authorization is sent as the Authorization header when non-empty.
	Authorization string
	// Client defaults to http.DefaultClient. The dispatcher bounds every call
	// with a context timeout, so the client does not need its own.
	Client *http.Client
	// UserAgent defaults to "traccar-gateway".
	UserAgent string
}

// Webhook posts one JSON document per URL and treats any non-2xx status as a
// failure.
type Webhook struct {
	config WebhookConfig
}

// NewWebhook creates the webhook channel.
func NewWebhook(config WebhookConfig) *Webhook {
	if config.Client == nil {
		config.Client = http.DefaultClient
	}
	if config.UserAgent == "" {
		config.UserAgent = "traccar-gateway"
	}
	return &Webhook{config: config}
}

// Name implements Notifier.
func (w *Webhook) Name() string { return "webhook" }

// Send implements Notifier.
func (w *Webhook) Send(ctx context.Context, message Message, recipients []string) error {
	if len(recipients) == 0 {
		return nil
	}

	encoded, err := json.Marshal(payload(message))
	if err != nil {
		return fmt.Errorf("webhook: encode: %w", err)
	}

	var failures []error
	for _, target := range recipients {
		if err := w.post(ctx, target, encoded); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (w *Webhook) post(ctx context.Context, target string, body []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: %s: %w", redactURL(target), err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", w.config.UserAgent)
	if w.config.Authorization != "" {
		request.Header.Set("Authorization", w.config.Authorization)
	}

	response, err := w.config.Client.Do(request)
	if err != nil {
		return fmt.Errorf("webhook: %s: %w", redactURL(target), err)
	}
	defer response.Body.Close()

	if response.StatusCode >= 200 && response.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil
	}

	preview, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	return fmt.Errorf("webhook: %s: %s: %s", redactURL(target),
		response.Status, strings.TrimSpace(string(preview)))
}

func payload(message Message) WebhookPayload {
	if message.Alert == nil {
		return WebhookPayload{Attributes: map[string]any{}}
	}

	attributes := message.Alert.Attributes
	if attributes == nil {
		attributes = map[string]any{}
	}

	body := WebhookPayload{
		Type:       message.Alert.Type,
		DeviceID:   message.Alert.DeviceID,
		Time:       message.Alert.Time,
		GeofenceID: message.Alert.GeofenceID,
		Subject:    message.Subject,
		Message:    message.Body,
		Attributes: attributes,
	}
	if message.Device != nil {
		body.UniqueID = message.Device.UniqueID
		body.DeviceName = message.Device.Name
	}
	if protocol, ok := attributes["protocol"].(string); ok {
		body.Protocol = protocol
	}
	return body
}

// redactURL keeps credentials and query strings out of the log. Webhook URLs
// often carry a secret token in the path, but the path is what identifies the
// endpoint, so only the user info and query are removed.
func redactURL(target string) string {
	parsed, err := url.Parse(target)
	if err != nil {
		return "webhook"
	}
	parsed.User = nil
	if parsed.RawQuery != "" {
		parsed.RawQuery = "redacted"
	}
	return parsed.String()
}
