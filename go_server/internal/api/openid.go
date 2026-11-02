package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// openIDStateCookie carries the anti forgery state of one login attempt.
const openIDStateCookie = "traccar_openid_state"

// OpenIDSettings configures the OpenID Connect login. The issuer is the base
// URL of the provider; its endpoints are read from the discovery document, so
// no provider specific path is compiled in.
type OpenIDSettings struct {
	// Enabled turns the two routes on.
	Enabled bool
	// IssuerURL is the provider base URL, for example
	// https://auth.example.com/application/o/traccar/.
	IssuerURL string
	ClientID  string
	// ClientSecret may be empty for a public client that uses PKCE; this
	// implementation sends the secret when it is set.
	ClientSecret string
	// RedirectURL is the absolute callback URL. Empty derives it from the
	// request, which works when the server sits behind a reverse proxy that
	// forwards the host.
	RedirectURL string
	// Scopes defaults to "openid email profile".
	Scopes []string
	// CreateMissing creates an account for an unknown email instead of
	// refusing the login.
	CreateMissing bool
	// SuccessURL is where the browser lands after a successful login. Empty
	// uses "/".
	SuccessURL string
	// ButtonName is the label the login page can show for this provider.
	ButtonName string
}

// WithOpenID enables GET /api/session/openid/auth and its callback. Without it
// both routes report 501.
func WithOpenID(settings OpenIDSettings) Option {
	return func(s *Server) {
		if settings.Enabled {
			s.openID = newOpenIDClient(settings)
		}
	}
}

// openIDClient caches the discovery document, which every login otherwise
// fetches again.
type openIDClient struct {
	settings OpenIDSettings
	client   *http.Client

	mu        sync.Mutex
	discovery *openIDDiscovery
	fetched   time.Time
	now       func() time.Time
}

type openIDDiscovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserInfoEndpoint      string `json:"userinfo_endpoint"`
	Issuer                string `json:"issuer"`
}

func newOpenIDClient(settings OpenIDSettings) *openIDClient {
	if len(settings.Scopes) == 0 {
		settings.Scopes = []string{"openid", "email", "profile"}
	}
	return &openIDClient{
		settings: settings,
		client:   &http.Client{Timeout: 10 * time.Second},
		now:      time.Now,
	}
}

// discover reads and caches the provider document for an hour.
func (c *openIDClient) discover(ctx context.Context) (*openIDDiscovery, error) {
	c.mu.Lock()
	if c.discovery != nil && c.now().Sub(c.fetched) < time.Hour {
		document := c.discovery
		c.mu.Unlock()
		return document, nil
	}
	c.mu.Unlock()

	address := strings.TrimSuffix(c.settings.IssuerURL, "/") + "/.well-known/openid-configuration"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("openid: %w", err)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("openid: discovery: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("openid: discovery answered %d", response.StatusCode)
	}
	var document openIDDiscovery
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&document); err != nil {
		return nil, fmt.Errorf("openid: discovery: %w", err)
	}
	if document.AuthorizationEndpoint == "" || document.TokenEndpoint == "" {
		return nil, errors.New("openid: discovery document is incomplete")
	}

	c.mu.Lock()
	c.discovery = &document
	c.fetched = c.now()
	c.mu.Unlock()
	return &document, nil
}

// handleOpenIDAuth starts the code flow. The state is random per attempt and
// stored in a short lived cookie the callback compares against.
func (s *Server) handleOpenIDAuth(w http.ResponseWriter, r *http.Request) {
	if s.openID == nil {
		notConfigured(w, "OpenID login")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	document, err := s.openID.discover(ctx)
	if err != nil {
		s.logf("api: openid discovery: %v", err)
		writeError(w, http.StatusBadGateway, errors.New("identity provider is unavailable"))
		return
	}
	state, err := randomState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	redirectURL := s.openIDRedirectURL(r)

	http.SetCookie(w, &http.Cookie{
		Name:     openIDStateCookie,
		Value:    state,
		Path:     "/",
		MaxAge:   int((10 * time.Minute) / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsSecure(r),
	})

	target, err := url.Parse(document.AuthorizationEndpoint)
	if err != nil {
		writeError(w, http.StatusBadGateway, errors.New("identity provider is misconfigured"))
		return
	}
	query := target.Query()
	query.Set("response_type", "code")
	query.Set("client_id", s.openID.settings.ClientID)
	query.Set("redirect_uri", redirectURL)
	query.Set("scope", strings.Join(s.openID.settings.Scopes, " "))
	query.Set("state", state)
	target.RawQuery = query.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

// handleOpenIDCallback finishes the flow: the code is exchanged, the userinfo
// is read, and the account is logged in with a session cookie.
func (s *Server) handleOpenIDCallback(w http.ResponseWriter, r *http.Request) {
	if s.openID == nil {
		notConfigured(w, "OpenID login")
		return
	}
	query := r.URL.Query()
	if message := query.Get("error"); message != "" {
		writeError(w, http.StatusUnauthorized,
			fmt.Errorf("identity provider refused the login: %s", message))
		return
	}
	code := query.Get("code")
	state := query.Get("state")
	cookie, err := r.Cookie(openIDStateCookie)
	if err != nil || code == "" ||
		subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		writeError(w, http.StatusUnauthorized, errors.New("invalid OpenID state"))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: openIDStateCookie, Value: "", Path: "/", MaxAge: -1})

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	document, err := s.openID.discover(ctx)
	if err != nil {
		s.logf("api: openid discovery: %v", err)
		writeError(w, http.StatusBadGateway, errors.New("identity provider is unavailable"))
		return
	}
	accessToken, err := s.openID.exchange(ctx, document, code, s.openIDRedirectURL(r))
	if err != nil {
		s.logf("api: openid token exchange: %v", err)
		writeError(w, http.StatusUnauthorized, errors.New("identity provider rejected the login"))
		return
	}
	claims, err := s.openID.userInfo(ctx, document, accessToken)
	if err != nil {
		s.logf("api: openid userinfo: %v", err)
		writeError(w, http.StatusBadGateway, errors.New("identity provider is unavailable"))
		return
	}
	if claims.Email == "" {
		writeError(w, http.StatusUnauthorized, errors.New("the identity provider did not return an email"))
		return
	}

	user, err := s.openIDAccount(ctx, claims)
	if err != nil {
		if errors.Is(err, model.ErrUserNotFound) {
			writeError(w, http.StatusForbidden, errors.New("no account matches this identity"))
			return
		}
		s.logf("api: openid account %q: %v", claims.Email, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	token, plaintext, err := s.users.IssueTokenForUser(ctx, user.ID, []string{"*"}, nil)
	if err != nil {
		s.logf("api: openid issue token for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	setSessionCookie(w, plaintext, s.now().Add(sessionCookieTTL))

	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, tokenIssueResponse{
			tokenResponse: newTokenResponse(*token),
			Token:         plaintext,
		})
		return
	}
	target := s.openID.settings.SuccessURL
	if target == "" {
		target = "/"
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// openIDAccount finds the account of the verified email, creating it when the
// deployment allows that.
func (s *Server) openIDAccount(ctx context.Context, claims openIDClaims) (*model.User, error) {
	if s.users == nil {
		return nil, errors.New("users are not configured")
	}
	users, err := s.users.Users(ctx, model.UserFilter{Email: claims.Email, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(users) > 0 {
		return &users[0], nil
	}
	if !s.openID.settings.CreateMissing {
		return nil, model.ErrUserNotFound
	}
	name := claims.Name
	if name == "" {
		name = claims.Email
	}
	return s.users.CreateUser(ctx, model.UserCreate{Email: claims.Email, Name: name})
}

func (c *openIDClient) exchange(ctx context.Context, document *openIDDiscovery,
	code, redirectURL string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURL)
	form.Set("client_id", c.settings.ClientID)
	if c.settings.ClientSecret != "" {
		form.Set("client_secret", c.settings.ClientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, document.TokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("token endpoint answered %d", response.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if body.AccessToken == "" {
		return "", errors.New("no access token")
	}
	return body.AccessToken, nil
}

type openIDClaims struct {
	Email string `json:"email"`
	Name  string `json:"name"`
	Sub   string `json:"sub"`
}

func (c *openIDClient) userInfo(ctx context.Context, document *openIDDiscovery,
	accessToken string) (openIDClaims, error) {
	var claims openIDClaims
	if document.UserInfoEndpoint == "" {
		return claims, errors.New("provider has no userinfo endpoint")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, document.UserInfoEndpoint, nil)
	if err != nil {
		return claims, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		return claims, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return claims, fmt.Errorf("userinfo answered %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&claims); err != nil {
		return claims, err
	}
	return claims, nil
}

// openIDRedirectURL uses the configured callback, or derives it from the
// request. A reverse proxy sets X-Forwarded-Proto and X-Forwarded-Host.
func (s *Server) openIDRedirectURL(r *http.Request) string {
	if s.openID.settings.RedirectURL != "" {
		return s.openID.settings.RedirectURL
	}
	scheme := "http"
	if requestIsSecure(r) {
		scheme = "https"
	}
	host := r.Host
	if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host + "/api/session/openid/callback"
}

func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func randomState() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}
