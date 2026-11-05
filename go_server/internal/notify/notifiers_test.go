package notify

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"

	"traccar-server_go/go_server/internal/model"
)

func message(subject, body string) Message {
	return Message{Subject: subject, Body: body}
}

// roundTripFunc lets a test answer an HTTP request without opening a socket,
// which the sandbox does not allow.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func testClient(handler roundTripFunc) *http.Client {
	return &http.Client{Transport: handler}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type recordedRequest struct {
	Path          string
	Method        string
	ContentType   string
	Authorization string
	AmzDate       string
	Body          string
}

// record captures the request and answers 200.
func record(target *recordedRequest) roundTripFunc {
	return func(request *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(request.Body)
		*target = recordedRequest{
			Path:          request.URL.Path,
			Method:        request.Method,
			ContentType:   request.Header.Get("Content-Type"),
			Authorization: request.Header.Get("Authorization"),
			AmzDate:       request.Header.Get("X-Amz-Date"),
			Body:          string(raw),
		}
		return jsonResponse(http.StatusOK, "{}"), nil
	}
}

func decodeJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return decoded
}

func TestTelegramSend(t *testing.T) {
	var request recordedRequest
	channel, err := NewTelegram(TelegramConfig{
		Token:  "BOT",
		Client: testClient(record(&request)),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := channel.Send(context.Background(), message("Alert", "Overspeed"), []string{"42"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if request.Path != "/botBOT/sendMessage" {
		t.Errorf("path = %q", request.Path)
	}
	body := decodeJSON(t, request.Body)
	if body["chat_id"] != "42" || body["text"] != "Overspeed" {
		t.Errorf("body = %v", body)
	}
}

func TestPushoverSend(t *testing.T) {
	var request recordedRequest
	channel, err := NewPushover(PushoverConfig{
		Token:  "APP",
		User:   "fallback",
		Client: testClient(record(&request)),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := channel.Send(context.Background(), message("Alert", "Overspeed"), []string{"user@phone"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	body := decodeJSON(t, request.Body)
	if body["token"] != "APP" || body["user"] != "user" || body["device"] != "phone" {
		t.Errorf("body = %v", body)
	}
	if body["title"] != "Alert" || body["message"] != "Overspeed" {
		t.Errorf("body = %v", body)
	}
}

func TestSmsHTTPTemplate(t *testing.T) {
	var request recordedRequest
	channel, err := NewSms(SmsConfig{
		HTTPURL:      "http://sms.example/send",
		HTTPTemplate: `{"to":"{phone}","text":"{message}"}`,
		Client:       testClient(record(&request)),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := channel.Send(context.Background(), message("", "Engine stopped"), []string{"+370123"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !strings.HasPrefix(request.ContentType, "application/json") {
		t.Errorf("content type = %q", request.ContentType)
	}
	if request.Body != `{"to":"+370123","text":"Engine stopped"}` {
		t.Errorf("body = %q", request.Body)
	}
}

func TestSmsSNSRequestIsSigned(t *testing.T) {
	var request recordedRequest
	channel, err := NewSms(SmsConfig{
		AWSAccess:   "AKIDEXAMPLE",
		AWSSecret:   "secret",
		AWSRegion:   "eu-west-1",
		AWSEndpoint: "https://sns.eu-west-1.example/",
		Client:      testClient(record(&request)),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := channel.Send(context.Background(), message("", "Hello"), []string{"+370123"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !strings.HasPrefix(request.Authorization, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/") ||
		!strings.Contains(request.Authorization, "/eu-west-1/sns/aws4_request") ||
		!strings.Contains(request.Authorization, "Signature=") {
		t.Errorf("authorization = %q", request.Authorization)
	}
	if request.AmzDate == "" {
		t.Error("missing X-Amz-Date")
	}
	if !strings.Contains(request.Body, "Action=Publish") ||
		!strings.Contains(request.Body, "PhoneNumber=%2B370123") {
		t.Errorf("body = %q", request.Body)
	}
}

func TestWhatsappSend(t *testing.T) {
	var request recordedRequest
	channel, err := NewWhatsapp(WhatsappConfig{
		Token:         "TOKEN",
		PhoneNumberID: "1234",
		TemplateName:  "alert",
		URL:           "https://graph.example/v18.0",
		Client:        testClient(record(&request)),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := channel.Send(context.Background(), message("", "Overspeed"), []string{"+370123"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if request.Path != "/v18.0/1234/messages" || request.Authorization != "Bearer TOKEN" {
		t.Errorf("path = %q, authorization = %q", request.Path, request.Authorization)
	}
	body := decodeJSON(t, request.Body)
	if body["messaging_product"] != "whatsapp" || body["to"] != "+370123" {
		t.Errorf("body = %v", body)
	}
	template, ok := body["template"].(map[string]any)
	if !ok || template["name"] != "alert" {
		t.Errorf("template = %v", body["template"])
	}
}

func TestTraccarPushSend(t *testing.T) {
	var request recordedRequest
	channel, err := NewTraccarPush(TraccarPushConfig{
		Key:    "KEY",
		URL:    "https://push.example/",
		Client: testClient(record(&request)),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := channel.Send(context.Background(), message("Alert", "Overspeed"), []string{"token1", "token2"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if request.Authorization != "key=KEY" {
		t.Errorf("authorization = %q", request.Authorization)
	}
	body := decodeJSON(t, request.Body)
	tokens, _ := body["registration_ids"].([]any)
	if len(tokens) != 2 {
		t.Errorf("body = %v", body)
	}
	notification, _ := body["notification"].(map[string]any)
	if notification["title"] != "Alert" || notification["body"] != "Overspeed" {
		t.Errorf("notification = %v", notification)
	}
}

func TestFirebaseSend(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
	account := map[string]string{
		"project_id":   "test-project",
		"client_email": "test@example.com",
		"private_key":  privateKey,
	}
	encodedAccount, _ := json.Marshal(account)

	var (
		tokenRequest recordedRequest
		sendRequest  recordedRequest
	)
	client := testClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/token" {
			raw, _ := io.ReadAll(request.Body)
			tokenRequest = recordedRequest{Path: request.URL.Path, Body: string(raw)}
			return jsonResponse(http.StatusOK, `{"access_token":"ya29.test","expires_in":3600}`), nil
		}
		raw, _ := io.ReadAll(request.Body)
		sendRequest = recordedRequest{
			Path:          request.URL.Path,
			Authorization: request.Header.Get("Authorization"),
			Body:          string(raw),
		}
		return jsonResponse(http.StatusOK, "{}"), nil
	})

	channel, err := NewFirebase(FirebaseConfig{
		ServiceAccount: string(encodedAccount),
		TokenURL:       "https://oauth.example/token",
		BaseURL:        "https://fcm.example",
		Client:         client,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := channel.Send(context.Background(), message("Alert", "Overspeed"), []string{"device-token"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if !strings.Contains(tokenRequest.Body, "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Ajwt-bearer") {
		t.Errorf("token request = %q", tokenRequest.Body)
	}
	if sendRequest.Path != "/v1/projects/test-project/messages:send" ||
		sendRequest.Authorization != "Bearer ya29.test" {
		t.Errorf("send request = %+v", sendRequest)
	}
	body := decodeJSON(t, sendRequest.Body)
	messageBody, _ := body["message"].(map[string]any)
	if messageBody["token"] != "device-token" {
		t.Errorf("body = %v", body)
	}
}

func TestNotifierFailureSurfacesStatus(t *testing.T) {
	client := testClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, "bad token"), nil
	})
	channel, err := NewTelegram(TelegramConfig{Token: "BOT", Client: client})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	err = channel.Send(context.Background(), message("", "x"), []string{"42"})
	if err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseNotificatorsAcceptsEveryChannel(t *testing.T) {
	channels, err := ParseNotificators([]string{
		"email, web, telegram", "pushover", "sms,firebase,traccar,whatsapp,command",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{
		NotificatorMail, NotificatorWeb, NotificatorTelegram, NotificatorPushover,
		NotificatorSms, NotificatorFirebase, NotificatorTraccar, NotificatorWhatsapp,
		NotificatorCommand,
	}
	if len(channels) != len(want) {
		t.Fatalf("channels = %v", channels)
	}
	for index := range want {
		if channels[index] != want[index] {
			t.Errorf("channel %d = %q, want %q", index, channels[index], want[index])
		}
	}
	if _, err := ParseNotificators([]string{"carrier-pigeon"}); err == nil {
		t.Error("an unknown channel was accepted")
	}
}

func TestSubscriptionTargetsUseUserAttributes(t *testing.T) {
	channel := Channel{
		Notifier: NamedNotifier{Label: NotificatorTelegram},
		UserTarget: func(subscription model.NotificationSubscription) []string {
			return SplitList(subscription.Attributes.GetString("telegramChatId", ""))
		},
	}
	subscriptions := []model.NotificationSubscription{
		{UserID: 1, Notificators: []string{NotificatorTelegram},
			Attributes: model.Attributes{"telegramChatId": "111"}},
		{UserID: 2, Notificators: []string{NotificatorMail},
			Attributes: model.Attributes{"telegramChatId": "222"}},
		{UserID: 3, Notificators: []string{NotificatorTelegram},
			Attributes: model.Attributes{"telegramChatId": "333"}},
	}
	targets := subscriptionTargets(channel, subscriptions)
	if len(targets) != 2 || targets[0] != "111" || targets[1] != "333" {
		t.Fatalf("targets = %v", targets)
	}
}

// NamedNotifier is a Notifier that only carries a name, for the routing tests.
type NamedNotifier struct{ Label string }

// Name implements Notifier.
func (n NamedNotifier) Name() string { return n.Label }

// Send implements Notifier.
func (n NamedNotifier) Send(context.Context, Message, []string) error { return nil }
