package notify

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SmsConfig configures the SMS channel. Either the HTTP gateway or AWS SNS is
// used; when both are configured, the HTTP gateway wins and SNS is the fallback.
type SmsConfig struct {
	// HTTPURL is the gateway endpoint; {phone} and {message} are substituted
	// into HTTPTemplate.
	HTTPURL string
	// HTTPTemplate defaults to "phone={phone}&message={message}".
	HTTPTemplate string
	// AuthorizationHeader defaults to "Authorization".
	AuthorizationHeader string
	// Authorization is sent verbatim when set; otherwise User and Password
	// become a Basic header.
	Authorization string
	User          string
	Password      string
	// SNS credentials.
	AWSAccess string
	AWSSecret string
	AWSRegion string
	// AWSEndpoint overrides the SNS endpoint for tests.
	AWSEndpoint string
	Client      *http.Client
}

// Sms delivers one text message per recipient.
type Sms struct {
	config SmsConfig
}

// NewSms creates the channel. A configuration without a gateway or SNS
// credentials is rejected, because a subscription that can never deliver is an
// operator mistake worth failing at startup.
func NewSms(config SmsConfig) (*Sms, error) {
	if config.HTTPURL == "" && (config.AWSAccess == "" || config.AWSSecret == "" || config.AWSRegion == "") {
		return nil, fmt.Errorf("sms: configure sms.http.url or the AWS SNS credentials")
	}
	if config.HTTPTemplate == "" {
		config.HTTPTemplate = "phone={phone}&message={message}"
	}
	if config.AuthorizationHeader == "" {
		config.AuthorizationHeader = "Authorization"
	}
	return &Sms{config: config}, nil
}

// Name implements Notifier.
func (s *Sms) Name() string { return NotificatorSms }

// Send implements Notifier.
func (s *Sms) Send(ctx context.Context, message Message, recipients []string) error {
	var failures []error
	for _, recipient := range recipients {
		phone := strings.TrimSpace(recipient)
		if phone == "" {
			continue
		}
		if s.config.HTTPURL != "" {
			if err := s.sendHTTP(ctx, phone, messageBody(message)); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		if err := s.sendSNS(ctx, phone, messageBody(message)); err != nil {
			failures = append(failures, err)
		}
	}
	return joinErrors("sms", failures)
}

// sendHTTP renders the configured template. A template that starts with "<" is
// sent as XML, with "{" or "[" as JSON, and everything else as a form body,
// which is the rule the Java HttpSmsClient uses.
func (s *Sms) sendHTTP(ctx context.Context, phone, message string) error {
	contentType := "application/x-www-form-urlencoded"
	trimmed := strings.TrimSpace(s.config.HTTPTemplate)
	switch {
	case strings.HasPrefix(trimmed, "<"):
		contentType = "application/xml"
	case strings.HasPrefix(trimmed, "{"), strings.HasPrefix(trimmed, "["):
		contentType = "application/json"
	}

	phoneValue, messageValue := phone, message
	if contentType == "application/x-www-form-urlencoded" {
		phoneValue = url.QueryEscape(phone)
		messageValue = url.QueryEscape(message)
	}
	body := strings.NewReplacer("{phone}", phoneValue, "{message}", messageValue).
		Replace(s.config.HTTPTemplate)

	headers := map[string]string{}
	authorization := s.config.Authorization
	if authorization == "" && s.config.User != "" && s.config.Password != "" {
		authorization = "Basic " + basicAuth(s.config.User, s.config.Password)
	}
	if authorization != "" {
		headers[s.config.AuthorizationHeader] = authorization
	}
	return postBody(ctx, s.config.Client, "sms", s.config.HTTPURL, contentType+"; charset=utf-8",
		[]byte(body), headers)
}

// basicAuth encodes user:password for the Authorization header.
func basicAuth(user, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
}
