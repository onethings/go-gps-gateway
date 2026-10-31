package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

func getWith(t *testing.T, handler http.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestAPIKeyRequired(t *testing.T) {
	querier := &fakePositions{}
	handler := NewServer(nil, WithPositions(querier), WithAPIKey("secret")).Handler()

	recorder := get(t, handler, "/api/positions")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", recorder.Code, recorder.Body)
	}
	if header := recorder.Header().Get("WWW-Authenticate"); !strings.Contains(header, "Bearer") {
		t.Errorf("WWW-Authenticate = %q", header)
	}
	if !strings.Contains(recorder.Body.String(), "invalid API key") {
		t.Errorf("body = %s", recorder.Body)
	}

	querier.mu.Lock()
	defer querier.mu.Unlock()
	if len(querier.filters) != 0 {
		t.Errorf("query ran %d times without a key", len(querier.filters))
	}
}

func TestAPIKeyAccepted(t *testing.T) {
	cases := map[string]map[string]string{
		"X-Api-Key": {"X-Api-Key": "secret"},
		"bearer":    {"Authorization": "Bearer secret"},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			handler := NewServer(nil, WithPositions(&fakePositions{}), WithAPIKey("secret")).Handler()
			if recorder := getWith(t, handler, "/api/positions", headers); recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
			}
		})
	}
}

func TestAPIKeyRejected(t *testing.T) {
	cases := map[string]map[string]string{
		"wrong key":       {"X-Api-Key": "wrong"},
		"wrong bearer":    {"Authorization": "Bearer wrong"},
		"empty bearer":    {"Authorization": "Bearer "},
		"basic auth":      {"Authorization": "Basic c2VjcmV0"},
		"prefix mismatch": {"X-Api-Key": "secret "},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			handler := NewServer(nil, WithPositions(&fakePositions{}), WithAPIKey("secret")).Handler()
			if recorder := getWith(t, handler, "/api/positions", headers); recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", recorder.Code)
			}
		})
	}
}

func TestAPIKeyHealthIsExempt(t *testing.T) {
	handler := NewServer(nil, WithAPIKey("secret")).Handler()

	recorder := get(t, handler, "/api/health")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestAPIKeyProtectsCommandRoute(t *testing.T) {
	sender := &fakeSender{}
	handler := NewServer(nil,
		WithCommands(sender, fakeDevices{protocol: "gt06", found: true}),
		WithAPIKey("secret"),
	).Handler()

	recorder := post(t, handler, "/api/commands", `{"deviceId":7,"type":"engineStop"}`)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.sent != 0 {
		t.Errorf("command was sent without a key")
	}
}

func TestAPIKeyDisabledWithoutOption(t *testing.T) {
	handler := NewServer(nil, WithPositions(&fakePositions{})).Handler()

	if recorder := get(t, handler, "/api/positions"); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
}

// loginHandler builds a handler whose fake store knows one account carrying the
// shared test password, plus a token lookup for the plaintext the fake store
// hands out. It is wired like the real server, where login is exempt and every
// other route needs a credential.
func loginHandler(t *testing.T, users UserStore, devices DeviceStore) http.Handler {
	t.Helper()
	options := []Option{
		WithUsers(users),
		WithAPIKey("shared-key"),
		withTestToken(auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeAll}, []int64{7})),
	}
	if devices != nil {
		options = append(options, WithDevices(devices))
	}
	return NewServer(nil, options...).Handler()
}

func TestLoginIssuesUsableToken(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, store, 1)
	devices := &fakeDeviceList{results: []model.Device{{ID: 7, Name: "van"}}}
	// The fake store always hands out this plaintext, and withTestToken maps the
	// test token instead, so the login token is looked up through its own entry.
	handler := NewServer(nil,
		WithUsers(store),
		WithDevices(devices),
		WithAPIKey("shared-key"),
		WithTokens(fakeTokens{tokens: map[string]*auth.Principal{
			auth.HashToken("trc_fake_plaintext"): auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeAll}, nil),
		}}),
	).Handler()

	recorder := apiRequest(t, handler, http.MethodPost, "/api/login",
		`{"email":"`+testEmail+`","password":"`+testPassword+`"}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	var issued struct {
		Token  string   `json:"token"`
		UserID int64    `json:"userId"`
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if !strings.HasPrefix(issued.Token, "trc_") {
		t.Errorf("token = %q, want the trc_ prefix", issued.Token)
	}
	if issued.UserID != 1 {
		t.Errorf("userId = %d, want 1", issued.UserID)
	}
	if strings.Join(issued.Scopes, ",") != string(auth.ScopeAll) {
		t.Errorf("scopes = %v, want [*]", issued.Scopes)
	}
	if strings.Contains(recorder.Body.String(), "password") {
		t.Errorf("login response carries a password field: %s", recorder.Body)
	}

	// The token a login hands out has to work on the routes a token reaches.
	request := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	request.Header.Set("Authorization", "Bearer "+issued.Token)
	tokenRecorder := httptest.NewRecorder()
	handler.ServeHTTP(tokenRecorder, request)
	if tokenRecorder.Code != http.StatusOK {
		t.Errorf("GET /api/devices with the login token = %d (body %s)",
			tokenRecorder.Code, tokenRecorder.Body)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, store, 1)
	handler := loginHandler(t, store, nil)

	recorder := apiRequest(t, handler, http.MethodPost, "/api/login",
		`{"email":"`+testEmail+`","password":"not the password"}`, "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", recorder.Code, recorder.Body)
	}
	if tokens := store.tokens[1]; len(tokens) != 0 {
		t.Errorf("a failed login minted %d tokens", len(tokens))
	}
}

func TestLoginRejectsUnknownEmail(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, store, 1)
	handler := loginHandler(t, store, nil)

	recorder := apiRequest(t, handler, http.MethodPost, "/api/login",
		`{"email":"nobody@example.test","password":"`+testPassword+`"}`, "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestLoginRejectsAccountWithoutPassword(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	handler := loginHandler(t, store, nil)

	recorder := apiRequest(t, handler, http.MethodPost, "/api/login",
		`{"email":"`+testEmail+`","password":"`+testPassword+`"}`, "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestLoginDisabledAccount(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, store, 1)
	disabled := true
	if _, err := store.UpdateUser(context.Background(), 1, model.UserUpdate{Disabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	handler := loginHandler(t, store, nil)

	recorder := apiRequest(t, handler, http.MethodPost, "/api/login",
		`{"email":"`+testEmail+`","password":"`+testPassword+`"}`, "")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}

	// A wrong password on the same account is still 401, so the status code
	// does not announce that the account exists but is disabled.
	recorder = apiRequest(t, handler, http.MethodPost, "/api/login",
		`{"email":"`+testEmail+`","password":"wrong"}`, "")
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("wrong password on a disabled account = %d, want 401", recorder.Code)
	}
	if tokens := store.tokens[1]; len(tokens) != 0 {
		t.Errorf("login on a disabled account minted %d tokens", len(tokens))
	}
}

func TestLoginRejectsMalformedRequests(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, store, 1)
	handler := loginHandler(t, store, nil)

	cases := map[string]string{
		"no body":        ``,
		"not json":       `{"email":`,
		"empty password": `{"email":"` + testEmail + `"}`,
		"bad email":      `{"email":"not-an-address","password":"` + testPassword + `"}`,
		"oversized body": `{"email":"` + testEmail + `","password":"` + strings.Repeat("a", 1<<17) + `"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			recorder := apiRequest(t, handler, http.MethodPost, "/api/login", body, "")
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body)
			}
		})
	}
	if tokens := store.tokens[1]; len(tokens) != 0 {
		t.Errorf("a rejected login minted %d tokens", len(tokens))
	}
}
