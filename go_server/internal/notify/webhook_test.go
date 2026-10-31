package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"traccar-server_go/go_server/internal/model"
)

func TestWebhookSendsPayload(t *testing.T) {
	var (
		received WebhookPayload
		header   http.Header
	)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		header = request.Header.Clone()

		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &received); err != nil {
			t.Errorf("decode body: %v", err)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	webhook := NewWebhook(WebhookConfig{Authorization: "Bearer secret"})
	alert := testAlert(model.AlertOverspeed, map[string]any{
		"protocol": "gt06",
		"speed":    31.5,
		"limit":    float64(30),
	})

	if err := webhook.Send(context.Background(), Format(alert, testDevice()), []string{server.URL}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if got := header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content type = %q", got)
	}
	if got := header.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("authorization = %q", got)
	}
	if got := header.Get("User-Agent"); got != "traccar-gateway" {
		t.Errorf("user agent = %q", got)
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
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	webhook := NewWebhook(WebhookConfig{})
	message := Format(testAlert(model.AlertPowerCut, nil), testDevice())

	err := webhook.Send(context.Background(), message, []string{server.URL})
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
}

func TestWebhookTriesEveryRecipient(t *testing.T) {
	var delivered atomic.Int32

	failing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "nope", http.StatusBadGateway)
	}))
	defer failing.Close()

	working := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		delivered.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer working.Close()

	webhook := NewWebhook(WebhookConfig{})
	message := Format(testAlert(model.AlertGeofenceEnter, map[string]any{"geofence": "depot"}), testDevice())

	err := webhook.Send(context.Background(), message, []string{failing.URL, working.URL})
	if err == nil {
		t.Fatal("expected the failing recipient to be reported")
	}
	if delivered.Load() != 1 {
		t.Errorf("working recipient calls = %d, want 1", delivered.Load())
	}
}

func TestWebhookIgnoresEmptyRecipients(t *testing.T) {
	webhook := NewWebhook(WebhookConfig{})
	if err := webhook.Send(context.Background(), Message{}, nil); err != nil {
		t.Fatalf("send: %v", err)
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
