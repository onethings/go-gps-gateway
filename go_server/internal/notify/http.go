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
)

// httpError is returned for a non-2xx answer of a notification provider. The
// body preview is kept short so a log line stays readable and no provider token
// leaks through an echoed request.
type httpError struct {
	Provider string
	Status   string
	Body     string
}

func (e *httpError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s: %s", e.Provider, e.Status)
	}
	return fmt.Sprintf("%s: %s: %s", e.Provider, e.Status, e.Body)
}

// clientOr returns the configured client or the shared default.
func clientOr(client *http.Client) *http.Client {
	if client == nil {
		return http.DefaultClient
	}
	return client
}

// postJSON sends one JSON document and reports a non-2xx answer.
func postJSON(
	ctx context.Context, client *http.Client, provider, target string,
	payload any, headers map[string]string,
) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%s: encode: %w", provider, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("%s: %w", provider, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "traccar-gateway")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return doRequest(clientOr(client), provider, request)
}

// postForm sends one URL-encoded body.
func postForm(
	ctx context.Context, client *http.Client, provider, target string,
	values url.Values, headers map[string]string,
) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target,
		strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("%s: %w", provider, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "traccar-gateway")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return doRequest(clientOr(client), provider, request)
}

// postBody sends a raw body with an explicit content type.
func postBody(
	ctx context.Context, client *http.Client, provider, target, contentType string,
	body []byte, headers map[string]string,
) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: %w", provider, err)
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("User-Agent", "traccar-gateway")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return doRequest(clientOr(client), provider, request)
}

func doRequest(client *http.Client, provider string, request *http.Request) error {
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%s: %w", provider, err)
	}
	defer response.Body.Close()

	if response.StatusCode >= 200 && response.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil
	}
	preview, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	return &httpError{
		Provider: provider,
		Status:   response.Status,
		Body:     strings.TrimSpace(string(preview)),
	}
}

// joinErrors keeps the per-recipient failures together, which lets one bad chat
// id fail without hiding the outcome of the others.
func joinErrors(provider string, errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	return fmt.Errorf("%s: %w", provider, errors.Join(errs...))
}
