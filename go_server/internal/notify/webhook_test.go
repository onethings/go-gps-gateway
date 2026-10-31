package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"traccar-server_go/go_server/internal/model"
)

// recordingTransport stands in for the network: the sandbox used by the test
// suite does not allow listening sockets, and the webhook only needs to be
// checked at the HTTP boundary.
type recordingTransport struct {
	status int
	body   string
	err    error

	mu       sync.Mutex
	requests []*http.Request
	payloads [][]byte
}

func (r *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.requests = append(r.requests, request.Clone(context.Background()))
	r.payloads = append(r.payloads, payload)
	status := r.status
	body := r.body
	r.mu.Unlock()

	if r.err != nil {
		return nil, r.err
	}
	if status == 0 {
		status = http.StatusOK
	}

	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}, nil
}

func (r *recordingTransport) captured(t *testing.T) ([]*http.Request, [][]byte) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*http.Request(nil), r.requests...), append([][]byte(nil), r.payloads...)
}

func webhookWith(t *testing.T, transport *recordingTransport, config WebhookConfig) *Webhook {
	t.Helper()
	config.Client = &http.Client{Transport: transport}
	return NewWebhook(config)
}

func TestWebhookSendsPayload(t *testing.T) {
	transport := &recordingTransport{}
	webhook := webhookWith(t, transport, WebhookConfig{Authorization: "Bearer secret"})

	alert := testAlert(model.AlertOverspeed, map[string]any{
		"protocol": "gt06",
		"speed":    31.5,
		"limit":    float64(30),
	})

	if err := webhook.Send(context.Background(), Format(alert, testDevice()), []string{"https://example.com/hook"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	requests, payloads := transport.captured(t)
	if len(requests) != 1 || len(payloads) != 1 {
		t.Fatalf("requests = %d, payloads = %d", len(requests), len(payloads))
	}

	request := requests[0]
	if got := request.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content type = %q", got)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("authorization = %q", got)
	}
	if got := request.Header.Get("User-Agent"); got != "traccar-gateway" {
		t.Errorf("user agent = %q", got)
	}

	var received WebhookPayload
	if err := json.Unmarshal(payloads[0], &received); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	if received.Type != model.AlertOverspeed {
		t.Errorf("type = %q", received.Type)
	}
	if received.DeviceID != 7 {
		t.Errorf("device id = %d", received.DeviceID)
	}
	if received.UniqueID != "864717003283581" || received.DeviceName != "van-7" {
		t.Errorf("device = %q / %q", received.DeviceName, received.UniqueID)
	}
	if received.Protocol != "gt06" {
		t.Errorf("protocol = %q", received.Protocol)
	}
	if received.Subject != "[traccar] deviceOverspeed: van-7 (864717003283581)" {
		t.Errorf("subject = %q", received.Subject)
	}
	if received.Message == "" {
		t.Error("message is empty")
	}
	if received.Attributes["speed"] != 31.5 {
		t.Errorf("attributes = %v", received.Attributes)
	}
}

func TestWebhookReportsNon2xx(t *testing.T) {
	transport := &recordingTransport{status: http.StatusInternalServerError, body: "boom"}
	webhook := webhookWith(t, transport, WebhookConfig{})

	err := webhook.Send(context.Background(), Format(testAlert(model.AlertPowerCut, nil), testDevice()),
		[]string{"https://example.com/hook"})
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v", err)
	}
}

func TestWebhookReportsTransportError(t *testing.T) {
	transport := &recordingTransport{err: errors.New("connection refused")}
	webhook := webhookWith(t, transport, WebhookConfig{})

	err := webhook.Send(context.Background(), Message{}, []string{"https://example.com/hook"})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error = %v", err)
	}
}

func TestWebhookTriesEveryRecipient(t *testing.T) {
	transport := &recordingTransport{status: http.StatusOK}
	webhook := webhookWith(t, transport, WebhookConfig{})
	message := Format(testAlert(model.AlertGeofenceEnter, map[string]any{"geofence": "depot"}), testDevice())

	if err := webhook.Send(context.Background(), message,
		[]string{"https://one.example/hook", "https://two.example/hook"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	requests, _ := transport.captured(t)
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if requests[0].URL.Host != "one.example" || requests[1].URL.Host != "two.example" {
		t.Errorf("hosts = %s, %s", requests[0].URL.Host, requests[1].URL.Host)
	}
}

func TestWebhookKeepsEachFailure(t *testing.T) {
	transport := &recordingTransport{status: http.StatusBadGateway}
	webhook := webhookWith(t, transport, WebhookConfig{})

	err := webhook.Send(context.Background(), Message{},
		[]string{"https://one.example/hook", "https://two.example/hook"})
	if err == nil {
		t.Fatal("expected failures to be reported")
	}
	if !strings.Contains(err.Error(), "one.example") || !strings.Contains(err.Error(), "two.example") {
		t.Errorf("error should list every target: %v", err)
	}
}

func TestWebhookIgnoresEmptyRecipients(t *testing.T) {
	transport := &recordingTransport{}
	webhook := webhookWith(t, transport, WebhookConfig{})

	if err := webhook.Send(context.Background(), Message{}, nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if requests, _ := transport.captured(t); len(requests) != 0 {
		t.Errorf("requests = %d, want 0", len(requests))
	}
}

func TestRedactURL(t *testing.T) {
	got := redactURL("https://user:pass@example.com/hook?token=secret")
	if got != "https://example.com/hook?redacted" {
		t.Errorf("redacted = %q", got)
	}
	if got := redactURL("://bad url"); got != "webhook" {
		t.Errorf("invalid url = %q", got)
	}
}
