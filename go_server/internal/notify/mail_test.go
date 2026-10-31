package notify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// smtpRecorder is a minimal SMTP server: just enough of the dialogue for
// net/smtp to deliver one message, recording the envelope and the payload.
type smtpRecorder struct {
	listener net.Listener
	done     chan struct{}

	mu         sync.Mutex
	from       string
	recipients []string
	data       []byte
}

func newSMTPRecorder(t *testing.T) *smtpRecorder {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	recorder := &smtpRecorder{listener: listener, done: make(chan struct{})}
	go recorder.serve()

	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-recorder.done:
		case <-time.After(2 * time.Second):
			t.Error("smtp recorder did not stop")
		}
	})
	return recorder
}

func (r *smtpRecorder) address() string { return r.listener.Addr().String() }

func (r *smtpRecorder) serve() {
	defer close(r.done)

	conn, err := r.listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	reply := func(line string) bool {
		if _, err := writer.WriteString(line + "\r\n"); err != nil {
			return false
		}
		return writer.Flush() == nil
	}

	if !reply("220 test ESMTP") {
		return
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			if !reply("250-test") || !reply("250 8BITMIME") {
				return
			}
		case strings.HasPrefix(line, "MAIL FROM:"):
			r.mu.Lock()
			r.from = envelopeAddress(line, "MAIL FROM:")
			r.mu.Unlock()
			if !reply("250 OK") {
				return
			}
		case strings.HasPrefix(line, "RCPT TO:"):
			r.mu.Lock()
			r.recipients = append(r.recipients, envelopeAddress(line, "RCPT TO:"))
			r.mu.Unlock()
			if !reply("250 OK") {
				return
			}
		case line == "DATA":
			if !reply("354 End data with <CR><LF>.<CR><LF>") {
				return
			}
			var data []byte
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
				data = append(data, dataLine...)
			}
			r.mu.Lock()
			r.data = data
			r.mu.Unlock()
			if !reply("250 OK") {
				return
			}
		case line == "QUIT":
			_ = reply("221 Bye")
			return
		default:
			if !reply("250 OK") {
				return
			}
		}
	}
}

func (r *smtpRecorder) envelope() (string, []string, []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.from, append([]string(nil), r.recipients...), append([]byte(nil), r.data...)
}

func envelopeAddress(line, prefix string) string {
	value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if index := strings.IndexByte(value, ' '); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSuffix(strings.TrimPrefix(value, "<"), ">")
}

func TestMailSend(t *testing.T) {
	recorder := newSMTPRecorder(t)

	mailer, err := NewMail(MailConfig{
		Address: recorder.address(),
		From:    "Traccar <alerts@example.com>",
		TLS:     MailTLSNone,
	})
	if err != nil {
		t.Fatalf("new mail: %v", err)
	}

	message := Format(testAlert(model.AlertOverspeed, map[string]any{
		"speed": 31.5,
		"limit": float64(30),
	}), testDevice())

	err = mailer.Send(context.Background(), message,
		[]string{"ops@example.com", "Ops Two <two@example.com>"})
	if err != nil {
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
	if len(recipients) != 2 || recipients[0] != "ops@example.com" || recipients[1] != "two@example.com" {
		t.Errorf("recipients = %v", recipients)
	}

	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	if got := parsed.Header.Get("Subject"); got != "[traccar] deviceOverspeed: van-7 (864717003283581)" {
		t.Errorf("subject = %q", got)
	}
	if got := parsed.Header.Get("To"); got != "ops@example.com, two@example.com" {
		t.Errorf("to = %q", got)
	}
	if got := parsed.Header.Get("Content-Transfer-Encoding"); got != "base64" {
		t.Errorf("encoding = %q", got)
	}

	body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, parsed.Body))
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if string(body) != message.Body {
		t.Errorf("body:\n%s\nwant:\n%s", body, message.Body)
	}
}

func TestMailRejectsInvalidRecipient(t *testing.T) {
	mailer, err := NewMail(MailConfig{Address: "127.0.0.1:1", From: "alerts@example.com"})
	if err != nil {
		t.Fatalf("new mail: %v", err)
	}

	err = mailer.Send(context.Background(), Message{Body: "hello"}, []string{"not-an-address"})
	if err == nil {
		t.Fatal("expected an invalid recipient error")
	}
	if !strings.Contains(err.Error(), "recipient") {
		t.Errorf("error = %v", err)
	}
}

func TestMailSanitizesHeaders(t *testing.T) {
	mailer, err := NewMail(MailConfig{Address: "smtp.example.com:25", From: "alerts@example.com"})
	if err != nil {
		t.Fatalf("new mail: %v", err)
	}

	encoded := mailer.encode(Message{
		Subject: "hello\r\nBcc: evil@example.com",
		Body:    "line",
	}, []string{"ops@example.com"})

	if strings.Contains(string(encoded), "Bcc:") {
		t.Errorf("header injection survived:\n%s", encoded)
	}

	parsed, err := mail.ReadMessage(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := parsed.Header.Get("Subject"); got != "hello  Bcc: evil@example.com" {
		t.Errorf("subject = %q", got)
	}
}

func TestNewMailValidation(t *testing.T) {
	tests := []struct {
		name   string
		config MailConfig
		want   string
	}{
		{"empty address", MailConfig{From: "a@example.com"}, "empty SMTP address"},
		{"missing port", MailConfig{Address: "smtp.example.com", From: "a@example.com"}, "host and port"},
		{"unknown tls", MailConfig{Address: "smtp.example.com:25", From: "a@example.com", TLS: "ssl"}, "unknown TLS mode"},
		{"empty sender", MailConfig{Address: "smtp.example.com:25"}, "empty sender"},
		{"bad sender", MailConfig{Address: "smtp.example.com:25", From: "not-an-address"}, "sender"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewMail(test.config)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMailImplicitTLSUsesPort465(t *testing.T) {
	mailer, err := NewMail(MailConfig{Address: "smtp.example.com:465", From: "a@example.com"})
	if err != nil {
		t.Fatalf("new mail: %v", err)
	}
	if !mailer.implicitTLS() {
		t.Error("auto TLS on port 465 should be implicit")
	}

	mailer, err = NewMail(MailConfig{Address: "smtp.example.com:587", From: "a@example.com"})
	if err != nil {
		t.Fatalf("new mail: %v", err)
	}
	if mailer.implicitTLS() {
		t.Error("auto TLS on port 587 should use STARTTLS")
	}
}
