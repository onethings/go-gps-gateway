package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
