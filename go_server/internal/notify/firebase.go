package notify

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"crypto"
	"crypto/rand"
	"crypto/x509"
)

// FirebaseConfig configures Firebase Cloud Messaging through the HTTP v1 API.
// ServiceAccount is the JSON key file Google issues; it carries the project id,
// the client email and the RSA private key.
type FirebaseConfig struct {
	ServiceAccount string
	// Mode is "direct" to send only the event id, "data" to skip the visible
	// notification, or empty for the full notification.
	Mode string
	// TokenURL and BaseURL default to the Google endpoints and exist so a test
	// can point the channel at httptest.
	TokenURL string
	BaseURL  string
	Client   *http.Client
}

// Firebase sends one push per registration token.
type Firebase struct {
	config FirebaseConfig
	client *http.Client

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

type firebaseAccount struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

// NewFirebase parses the service account so a malformed key fails at startup.
func NewFirebase(config FirebaseConfig) (*Firebase, error) {
	if strings.TrimSpace(config.ServiceAccount) == "" {
		return nil, fmt.Errorf("firebase: empty service account")
	}
	if config.TokenURL == "" {
		config.TokenURL = "https://oauth2.googleapis.com/token"
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://fcm.googleapis.com"
	}
	var account firebaseAccount
	if err := json.Unmarshal([]byte(config.ServiceAccount), &account); err != nil {
		return nil, fmt.Errorf("firebase: service account: %w", err)
	}
	if account.ProjectID == "" || account.ClientEmail == "" || account.PrivateKey == "" {
		return nil, fmt.Errorf("firebase: service account needs project_id, client_email and private_key")
	}
	if _, err := parsePrivateKey(account.PrivateKey); err != nil {
		return nil, fmt.Errorf("firebase: %w", err)
	}
	return &Firebase{config: config, client: clientOr(config.Client)}, nil
}

// Name implements Notifier.
func (f *Firebase) Name() string { return NotificatorFirebase }

// Send implements Notifier.
func (f *Firebase) Send(ctx context.Context, message Message, recipients []string) error {
	if len(recipients) == 0 {
		return nil
	}
	account, err := f.account()
	if err != nil {
		return err
	}
	token, err := f.token(ctx, account)
	if err != nil {
		return err
	}

	target := strings.TrimSuffix(f.config.BaseURL, "/") +
		"/v1/projects/" + account.ProjectID + "/messages:send"
	headers := map[string]string{"Authorization": "Bearer " + token}

	var failures []error
	for _, recipient := range recipients {
		registrationToken := strings.TrimSpace(recipient)
		if registrationToken == "" {
			continue
		}
		payload := map[string]any{
			"message": map[string]any{
				"token": registrationToken,
				"notification": map[string]any{
					"title": message.Subject,
					"body":  messageBody(message),
				},
				"android": map[string]any{
					"priority":     "high",
					"notification": map[string]any{"sound": "default"},
				},
				"apns": map[string]any{
					"headers": map[string]string{"apns-priority": "10"},
					"payload": map[string]any{"aps": map[string]any{"sound": "default"}},
				},
			},
		}
		if f.config.Mode == "data" {
			delete(payload["message"].(map[string]any), "notification")
		}
		if err := postJSON(ctx, f.client, "firebase", target, payload, headers); err != nil {
			failures = append(failures, err)
		}
	}
	return joinErrors("firebase", failures)
}

// account re-parses the service account for one send. The JSON is small and a
// parse per alert keeps NewFirebase free of stored key material.
func (f *Firebase) account() (*firebaseAccount, error) {
	var account firebaseAccount
	if err := json.Unmarshal([]byte(f.config.ServiceAccount), &account); err != nil {
		return nil, fmt.Errorf("firebase: service account: %w", err)
	}
	return &account, nil
}

// token returns a cached OAuth2 access token, requesting a new one when the
// cached one is about to expire.
func (f *Firebase) token(ctx context.Context, account *firebaseAccount) (string, error) {
	f.mu.Lock()
	if f.accessToken != "" && time.Now().Before(f.expiresAt) {
		token := f.accessToken
		f.mu.Unlock()
		return token, nil
	}
	f.mu.Unlock()

	assertion, err := firebaseAssertion(account, f.config.TokenURL, time.Now())
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.config.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("firebase: token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := f.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("firebase: token: %w", err)
	}
	defer response.Body.Close()

	var decoded struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("firebase: token: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("firebase: token: %s: %s %s",
			response.Status, decoded.Error, decoded.Description)
	}
	if decoded.AccessToken == "" {
		return "", fmt.Errorf("firebase: token: empty access token")
	}

	lifetime := time.Duration(decoded.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	f.mu.Lock()
	f.accessToken = decoded.AccessToken
	// Refresh a minute early so a send never starts with a token that expires
	// in flight.
	f.expiresAt = time.Now().Add(lifetime - time.Minute)
	f.mu.Unlock()
	return decoded.AccessToken, nil
}

// firebaseAssertion builds and signs the RS256 JWT the token endpoint accepts.
func firebaseAssertion(account *firebaseAccount, audience string, now time.Time) (string, error) {
	key, err := parsePrivateKey(account.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("firebase: %w", err)
	}

	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iss":   account.ClientEmail,
		"scope": "https://www.googleapis.com/auth/firebase.messaging",
		"aud":   audience,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	encodedHeader, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("firebase: encode header: %w", err)
	}
	encodedClaims, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("firebase: encode claims: %w", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(encodedHeader) + "." +
		base64.RawURLEncoding.EncodeToString(encodedClaims)

	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("firebase: sign: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// parsePrivateKey accepts the PEM block a service account carries.
func parsePrivateKey(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, fmt.Errorf("private key is not PEM")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("private key is not RSA")
		}
		return rsaKey, nil
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return key, nil
}
