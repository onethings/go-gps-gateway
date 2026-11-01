package forward

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"traccar-server_go/go_server/internal/model"
)

// HTTPConfig configures the JSON and URL forwarders. Header carries raw
// "Name: value" lines, the same shape the Java server reads from forward.header.
type HTTPConfig struct {
	URL    string
	Header string
	Client *http.Client
}

// parseHeaderLines turns the configured header block into a map.
func parseHeaderLines(raw string) map[string]string {
	headers := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		headers[name] = strings.TrimSpace(value)
	}
	return headers
}

func headerValue(headers map[string]string, name string) (string, bool) {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}

// JSONTransport posts the position or event document as JSON.
type JSONTransport struct {
	config HTTPConfig
	header map[string]string
	client *http.Client
}

// NewJSON creates the JSON forwarder.
func NewJSON(config HTTPConfig) (*JSONTransport, error) {
	if config.URL == "" {
		return nil, fmt.Errorf("forward json: empty URL")
	}
	client := config.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &JSONTransport{config: config, header: parseHeaderLines(config.Header), client: client}, nil
}

// Name implements Transport.
func (t *JSONTransport) Name() string { return "json" }

// Send implements Transport.
func (t *JSONTransport) Send(ctx context.Context, message Message) error {
	encoded, err := json.Marshal(payload(message))
	if err != nil {
		return fmt.Errorf("forward json: encode: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.config.URL, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("forward json: request: %w", err)
	}
	for name, value := range t.header {
		if strings.EqualFold(name, "Content-Type") {
			request.Header.Set("Content-Type", value)
			continue
		}
		request.Header.Set(name, value)
	}
	if _, ok := headerValue(t.header, "Content-Type"); !ok {
		request.Header.Set("Content-Type", "application/json")
	}
	return doHTTP(t.client, "forward json", request)
}

// Close implements Transport.
func (t *JSONTransport) Close() error { return nil }

// URLTransport renders the position into a GET URL and reads the answer.
type URLTransport struct {
	template string
	header   map[string]string
	client   *http.Client
}

// NewURL creates the URL forwarder.
func NewURL(config HTTPConfig) (*URLTransport, error) {
	if config.URL == "" {
		return nil, fmt.Errorf("forward url: empty URL")
	}
	client := config.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &URLTransport{
		template: config.URL,
		header:   parseHeaderLines(config.Header),
		client:   client,
	}, nil
}

// Name implements Transport.
func (t *URLTransport) Name() string { return "url" }

// Send implements Transport.
func (t *URLTransport) Send(ctx context.Context, message Message) error {
	if message.Position == nil {
		return fmt.Errorf("forward url: the url forwarder needs a position")
	}
	target, err := formatURL(t.template, message)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("forward url: request: %w", err)
	}
	for name, value := range t.header {
		request.Header.Set(name, value)
	}
	return doHTTP(t.client, "forward url", request)
}

// Close implements Transport.
func (t *URLTransport) Close() error { return nil }

// formatURL substitutes the Java forwarder placeholders.
func formatURL(template string, message Message) (string, error) {
	position := message.Position
	replacements := map[string]string{
		"{deviceId}":   strconv.FormatInt(position.DeviceID, 10),
		"{protocol}":   position.Protocol,
		"{deviceTime}": strconv.FormatInt(position.Time.UnixMilli(), 10),
		"{fixTime}":    strconv.FormatInt(position.Time.UnixMilli(), 10),
		"{valid}":      strconv.FormatBool(position.Valid),
		"{latitude}":   formatFloat(position.Latitude),
		"{longitude}":  formatFloat(position.Longitude),
		"{altitude}":   formatFloat(position.Altitude),
		"{speed}":      formatFloat(position.Speed),
		"{course}":     formatFloat(position.Course),
		"{accuracy}":   formatFloat(position.Accuracy),
		"{statusCode}": statusCode(position),
	}
	if message.Device != nil {
		replacements["{name}"] = url.QueryEscape(message.Device.Name)
		replacements["{uniqueId}"] = message.Device.UniqueID
		replacements["{status}"] = message.Device.Status
	}
	if address := position.Attributes.GetString("address", ""); address != "" {
		replacements["{address}"] = url.QueryEscape(address)
	}
	if strings.Contains(template, "{attributes}") {
		encoded, err := json.Marshal(attributesOf(position.Attributes))
		if err != nil {
			return "", fmt.Errorf("forward url: encode attributes: %w", err)
		}
		replacements["{attributes}"] = url.QueryEscape(string(encoded))
	}

	result := template
	for placeholder, value := range replacements {
		result = strings.ReplaceAll(result, placeholder, value)
	}
	return result, nil
}

// statusCode is the OpenGTS status the Java URL forwarder sends.
func statusCode(position *model.Position) string {
	switch {
	case position.Attributes.Has(model.KeyAlarm):
		return "0xF841"
	case position.Speed < 1:
		return "0xF020"
	default:
		return "0xF11C"
	}
}

func formatFloat(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

func doHTTP(client *http.Client, provider string, request *http.Request) error {
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%s: %w", provider, err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil
	}
	preview, _ := io.ReadAll(io.LimitReader(response.Body, 256))
	return fmt.Errorf("%s: %s: %s", provider, response.Status, strings.TrimSpace(string(preview)))
}
