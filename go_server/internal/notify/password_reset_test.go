package notify

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestPasswordResetMessageNamesTheTokenAndExpiry(t *testing.T) {
	message := PasswordResetMessage("prt_abc123", 30*time.Minute)

	if message.Subject == "" {
		t.Error("subject is empty")
	}
	if !strings.Contains(message.Body, "prt_abc123") {
		t.Errorf("body does not carry the code:\n%s", message.Body)
	}
	if !strings.Contains(message.Body, "30 minutes") {
		t.Errorf("body does not state the lifetime:\n%s", message.Body)
	}
}

func TestPasswordResetMailSendsOneMessage(t *testing.T) {
	recorder, dial := startSMTPRecorder(t)

	mailer, err := NewMail(MailConfig{
		Address: "smtp.example.com:25",
		From:    "Traccar <alerts@example.com>",
		TLS:     MailTLSNone,
	})
	if err != nil {
		t.Fatalf("new mail: %v", err)
	}
	mailer.dialTCP = dial

	reset := NewPasswordResetMail(mailer)
	if err := reset.SendPasswordReset(context.Background(), "alice@example.test", "prt_abc123", 30*time.Minute); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case <-recorder.done:
	case <-time.After(2 * time.Second):
		t.Fatal("smtp recorder did not finish")
	}

	from, recipients, raw := recorder.envelope()
	if from != "alerts@example.com" {
		t.Errorf("envelope from = %q", from)
	}
	if len(recipients) != 1 || recipients[0] != "alice@example.test" {
		t.Errorf("recipients = %v, want only the account address", recipients)
	}

	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, parsed.Body))
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !strings.Contains(string(body), "prt_abc123") {
		t.Errorf("mailed body does not carry the code:\n%s", body)
	}
}
