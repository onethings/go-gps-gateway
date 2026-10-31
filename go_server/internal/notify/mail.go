package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// TLS modes accepted by MailConfig.TLS.
const (
	// MailTLSAuto uses implicit TLS on port 465 and STARTTLS when the server
	// advertises it, which is what most relays expect.
	MailTLSAuto = "auto"
	// MailTLSStartTLS requires STARTTLS and fails when the server does not
	// offer it.
	MailTLSStartTLS = "starttls"
	// MailTLSImplicit wraps the connection in TLS from the first byte (SMTPS,
	// normally port 465).
	MailTLSImplicit = "implicit"
	// MailTLSNone sends plaintext. Only useful for a local relay.
	MailTLSNone = "none"
)

// MailConfig configures the SMTP channel. Recipients come from the channel
// defaults and the devices.attributes override.
type MailConfig struct {
	// Address is the SMTP host:port, for example smtp.example.com:587.
	Address string
	// From is the envelope and header sender.
	From string
	// Username and Password are optional; empty disables SMTP AUTH.
	Username string
	Password string
	// TLS is one of the MailTLS* values; empty means MailTLSAuto.
	TLS string
}

// Mail sends one text/plain UTF-8 message per alert through SMTP.
type Mail struct {
	config MailConfig
	host   string
	port   string
	dialer *net.Dialer
}

// NewMail validates the configuration. TLS modes other than auto and implicit
// are normalised here so Send does not have to.
func NewMail(config MailConfig) (*Mail, error) {
	if config.Address == "" {
		return nil, errors.New("mail: empty SMTP address")
	}
	host, port, err := net.SplitHostPort(config.Address)
	if err != nil {
		return nil, fmt.Errorf("mail: address %q: %w", config.Address, err)
	}
	if host == "" || port == "" {
		return nil, fmt.Errorf("mail: address %q needs host and port", config.Address)
	}

	switch config.TLS {
	case "":
		config.TLS = MailTLSAuto
	case MailTLSAuto, MailTLSStartTLS, MailTLSImplicit, MailTLSNone:
	default:
		return nil, fmt.Errorf("mail: unknown TLS mode %q (want auto, starttls, implicit or none)", config.TLS)
	}

	if config.From == "" {
		return nil, errors.New("mail: empty sender address")
	}
	from, err := mail.ParseAddress(config.From)
	if err != nil {
		return nil, fmt.Errorf("mail: sender %q: %w", config.From, err)
	}
	config.From = from.Address

	return &Mail{
		config: config,
		host:   host,
		port:   port,
		dialer: &net.Dialer{Timeout: 10 * time.Second},
	}, nil
}

// Name implements Notifier.
func (m *Mail) Name() string { return "mail" }

// Send implements Notifier. Every recipient is part of the same SMTP
// transaction, so either the whole message is accepted or the call fails.
func (m *Mail) Send(ctx context.Context, message Message, recipients []string) error {
	addresses, err := validRecipients(recipients)
	if err != nil {
		return err
	}
	if len(addresses) == 0 {
		return nil
	}

	conn, err := m.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		return fmt.Errorf("mail: smtp: %w", err)
	}
	defer client.Close()

	if err := m.upgrade(client); err != nil {
		return err
	}
	if m.config.Username != "" {
		auth := smtp.PlainAuth("", m.config.Username, m.config.Password, m.host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}

	if err := client.Mail(m.config.From); err != nil {
		return fmt.Errorf("mail: sender: %w", err)
	}
	for _, address := range addresses {
		if err := client.Rcpt(address); err != nil {
			return fmt.Errorf("mail: recipient %s: %w", address, err)
		}
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if _, err := writer.Write(m.encode(message, addresses)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("mail: write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("mail: quit: %w", err)
	}
	return nil
}

func (m *Mail) implicitTLS() bool {
	return m.config.TLS == MailTLSImplicit ||
		(m.config.TLS == MailTLSAuto && m.port == "465")
}

func (m *Mail) dial(ctx context.Context) (net.Conn, error) {
	conn, err := m.dialer.DialContext(ctx, "tcp", m.config.Address)
	if err != nil {
		return nil, fmt.Errorf("mail: dial %s: %w", m.config.Address, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	if !m.implicitTLS() {
		return conn, nil
	}

	secured := tls.Client(conn, &tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12})
	if err := secured.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("mail: tls handshake: %w", err)
	}
	return secured, nil
}

func (m *Mail) upgrade(client *smtp.Client) error {
	if m.implicitTLS() || m.config.TLS == MailTLSNone {
		return nil
	}

	config := &tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12}
	if m.config.TLS == MailTLSStartTLS {
		if err := client.StartTLS(config); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
		return nil
	}

	// auto: upgrade when the server offers it, otherwise continue in plaintext
	// like net/smtp.SendMail does.
	if supported, _ := client.Extension("STARTTLS"); supported {
		if err := client.StartTLS(config); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
	}
	return nil
}

// encode renders the message with a base64 body so that UTF-8 and long lines
// survive every relay.
func (m *Mail) encode(message Message, recipients []string) []byte {
	var buffer bytes.Buffer
	writeMailHeader(&buffer, "From", m.config.From)
	writeMailHeader(&buffer, "To", strings.Join(recipients, ", "))
	writeMailHeader(&buffer, "Subject", mime.QEncoding.Encode("utf-8", sanitizeHeader(message.Subject)))
	writeMailHeader(&buffer, "Date", time.Now().Format(time.RFC1123Z))
	writeMailHeader(&buffer, "Message-ID", m.messageID())
	writeMailHeader(&buffer, "MIME-Version", "1.0")
	writeMailHeader(&buffer, "Content-Type", "text/plain; charset=utf-8")
	writeMailHeader(&buffer, "Content-Transfer-Encoding", "base64")
	buffer.WriteString("\r\n")

	encoded := base64.StdEncoding.EncodeToString([]byte(message.Body))
	for len(encoded) > 76 {
		buffer.WriteString(encoded[:76])
		buffer.WriteString("\r\n")
		encoded = encoded[76:]
	}
	if encoded != "" {
		buffer.WriteString(encoded)
		buffer.WriteString("\r\n")
	}
	return buffer.Bytes()
}

func (m *Mail) messageID() string {
	var random [8]byte
	_, _ = rand.Read(random[:])
	return fmt.Sprintf("<%d.%x@%s>", time.Now().UnixNano(), random, m.host)
}

func writeMailHeader(buffer *bytes.Buffer, name, value string) {
	buffer.WriteString(name)
	buffer.WriteString(": ")
	buffer.WriteString(sanitizeHeader(value))
	buffer.WriteString("\r\n")
}

// sanitizeHeader keeps CR/LF out of a header value so that an alert attribute
// cannot inject extra headers.
func sanitizeHeader(value string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
}

func validRecipients(recipients []string) ([]string, error) {
	var addresses []string
	var failures []error
	for _, recipient := range recipients {
		parsed, err := mail.ParseAddress(strings.TrimSpace(recipient))
		if err != nil {
			failures = append(failures, fmt.Errorf("mail: recipient %q: %w", recipient, err))
			continue
		}
		addresses = append(addresses, parsed.Address)
	}
	return addresses, errors.Join(failures...)
}
