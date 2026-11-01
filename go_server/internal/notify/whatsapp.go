package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// WhatsappConfig configures the WhatsApp Cloud API channel. A template is
// required because the API refuses free-form messages outside a 24 hour window.
type WhatsappConfig struct {
	Token            string
	PhoneNumberID    string
	TemplateName     string
	TemplateLanguage string
	URL              string
	Client           *http.Client
}

// Whatsapp sends one template message per phone number.
type Whatsapp struct {
	config WhatsappConfig
}

// NewWhatsapp creates the channel.
func NewWhatsapp(config WhatsappConfig) (*Whatsapp, error) {
	if strings.TrimSpace(config.Token) == "" || strings.TrimSpace(config.PhoneNumberID) == "" {
		return nil, fmt.Errorf("whatsapp: token and phone number id are required")
	}
	if strings.TrimSpace(config.TemplateName) == "" {
		return nil, fmt.Errorf("whatsapp: a template name is required")
	}
	if config.TemplateLanguage == "" {
		config.TemplateLanguage = "en"
	}
	if config.URL == "" {
		config.URL = "https://graph.facebook.com/v18.0"
	}
	return &Whatsapp{config: config}, nil
}

// Name implements Notifier.
func (w *Whatsapp) Name() string { return NotificatorWhatsapp }

type whatsappParameter struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type whatsappComponent struct {
	Type       string              `json:"type"`
	Parameters []whatsappParameter `json:"parameters"`
}

type whatsappTemplate struct {
	Name       string              `json:"name"`
	Language   map[string]string   `json:"language"`
	Components []whatsappComponent `json:"components"`
}

type whatsappMessage struct {
	MessagingProduct string           `json:"messaging_product"`
	To               string           `json:"to"`
	Type             string           `json:"type"`
	Template         whatsappTemplate `json:"template"`
}

// Send implements Notifier.
func (w *Whatsapp) Send(ctx context.Context, message Message, recipients []string) error {
	target := strings.TrimSuffix(w.config.URL, "/") + "/" +
		w.config.PhoneNumberID + "/messages"
	headers := map[string]string{"Authorization": "Bearer " + w.config.Token}

	var failures []error
	for _, recipient := range recipients {
		phone := strings.TrimSpace(recipient)
		if phone == "" {
			continue
		}
		payload := whatsappMessage{
			MessagingProduct: "whatsapp",
			To:               phone,
			Type:             "template",
			Template: whatsappTemplate{
				Name:     w.config.TemplateName,
				Language: map[string]string{"code": w.config.TemplateLanguage},
				Components: []whatsappComponent{{
					Type: "body",
					Parameters: []whatsappParameter{{
						Type: "text",
						Text: messageBody(message),
					}},
				}},
			},
		}
		if err := postJSON(ctx, w.config.Client, "whatsapp", target, payload, headers); err != nil {
			failures = append(failures, err)
		}
	}
	return joinErrors("whatsapp", failures)
}
