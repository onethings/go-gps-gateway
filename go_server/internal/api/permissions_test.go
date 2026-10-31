package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"traccar-server_go/go_server/internal/auth"
)

const testToken = "trc_test_token"

var errLookup = errors.New("lookup failed")

// fakeTokens resolves the one test token to a principal.
type fakeTokens struct {
	tokens map[string]*auth.Principal
	err    error
}

func (f fakeTokens) LookupToken(_ context.Context, tokenHash string) (*auth.Principal, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tokens[tokenHash], nil
}

func withTestToken(principal *auth.Principal) Option {
	return WithTokens(fakeTokens{tokens: map[string]*auth.Principal{
		auth.HashToken(testToken): principal,
	}})
}

func withToken(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testToken)
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestTokenRequired(t *testing.T) {
	querier := &fakePositions{}
	handler := NewServer(nil,
		WithPositions(querier),
		withTestToken(auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeReadPositions}, nil)),
	).Handler()

	recorder := get(t, handler, "/api/positions")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", recorder.Code, recorder.Body)
	}
	if len(querier.filters) != 0 {
		t.Error("query ran without a token")
	}
}

func TestTokenScopeIsEnforced(t *testing.T) {
	alerts := &fakeQuerier{}
	positions := &fakePositions{}
	principal := auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeReadPositions}, []int64{7})
	handler := NewServer(alerts, WithPositions(positions), withTestToken(principal)).Handler()

	if recorder := withToken(t, handler, http.MethodGet, "/api/positions", ""); recorder.Code != http.StatusOK {
		t.Fatalf("positions status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if recorder := withToken(t, handler, http.MethodGet, "/api/alerts", ""); recorder.Code != http.StatusForbidden {
		t.Fatalf("alerts status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
	if len(alerts.filters) != 0 {
		t.Error("alerts query ran without read:alerts")
	}
}

func TestTokenFiltersQueriesToOwnedDevices(t *testing.T) {
	alerts := &fakeQuerier{}
	positions := &fakePositions{}
	devices := &fakeDeviceList{}
	principal := auth.NewPrincipal(1, false, []auth.Scope{
		auth.ScopeReadPositions, auth.ScopeReadAlerts, auth.ScopeReadDevices,
	}, []int64{7, 9})
	handler := NewServer(alerts,
		WithPositions(positions), WithDevices(devices), withTestToken(principal)).Handler()

	if recorder := withToken(t, handler, http.MethodGet, "/api/positions", ""); recorder.Code != http.StatusOK {
		t.Fatalf("positions status = %d (body %s)", recorder.Code, recorder.Body)
	}
	if recorder := withToken(t, handler, http.MethodGet, "/api/alerts", ""); recorder.Code != http.StatusOK {
		t.Fatalf("alerts status = %d (body %s)", recorder.Code, recorder.Body)
	}
	if recorder := withToken(t, handler, http.MethodGet, "/api/devices", ""); recorder.Code != http.StatusOK {
		t.Fatalf("devices status = %d (body %s)", recorder.Code, recorder.Body)
	}

	want := []int64{7, 9}
	if got := positions.lastFilter(t).DeviceIDs; !reflect.DeepEqual(got, want) {
		t.Errorf("positions device ids = %v, want %v", got, want)
	}
	if got := alerts.lastFilter(t).DeviceIDs; !reflect.DeepEqual(got, want) {
		t.Errorf("alerts device ids = %v, want %v", got, want)
	}
	if got := devices.lastFilter(t).DeviceIDs; !reflect.DeepEqual(got, want) {
		t.Errorf("devices device ids = %v, want %v", got, want)
	}
}

func TestTokenWithoutDevicesSeesNothing(t *testing.T) {
	positions := &fakePositions{}
	principal := auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeReadPositions}, nil)
	handler := NewServer(nil, WithPositions(positions), withTestToken(principal)).Handler()

	if recorder := withToken(t, handler, http.MethodGet, "/api/positions", ""); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}

	devices := positions.lastFilter(t).DeviceIDs
	if devices == nil || len(devices) != 0 {
		t.Errorf("device ids = %#v, want a non-nil empty slice", devices)
	}
}

func TestAdminTokenIsUnrestricted(t *testing.T) {
	positions := &fakePositions{}
	handler := NewServer(nil, WithPositions(positions), withTestToken(auth.AdminPrincipal())).Handler()

	if recorder := withToken(t, handler, http.MethodGet, "/api/positions", ""); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}
	if devices := positions.lastFilter(t).DeviceIDs; devices != nil {
		t.Errorf("device ids = %v, want nil", devices)
	}
}

func TestLegacyAPIKeyIsAdmin(t *testing.T) {
	positions := &fakePositions{}
	handler := NewServer(nil, WithPositions(positions), WithAPIKey("secret")).Handler()

	recorder := getWith(t, handler, "/api/positions", map[string]string{"X-Api-Key": "secret"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}
	if devices := positions.lastFilter(t).DeviceIDs; devices != nil {
		t.Errorf("device ids = %v, want nil", devices)
	}
}

func TestTokenWorksAlongsideAPIKey(t *testing.T) {
	positions := &fakePositions{}
	principal := auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeReadPositions}, []int64{7})
	handler := NewServer(nil, WithPositions(positions),
		WithAPIKey("secret"), withTestToken(principal)).Handler()

	if recorder := withToken(t, handler, http.MethodGet, "/api/positions", ""); recorder.Code != http.StatusOK {
		t.Fatalf("token status = %d (body %s)", recorder.Code, recorder.Body)
	}
	if got := positions.lastFilter(t).DeviceIDs; !reflect.DeepEqual(got, []int64{7}) {
		t.Errorf("device ids = %v, want [7]", got)
	}
	if recorder := getWith(t, handler, "/api/positions", map[string]string{"X-Api-Key": "wrong"}); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key status = %d, want 401", recorder.Code)
	}
}

func TestTokenLookupFailureIsServerError(t *testing.T) {
	positions := &fakePositions{}
	lookup := fakeTokens{err: errLookup}
	handler := NewServer(nil, WithPositions(positions), WithTokens(lookup)).Handler()

	recorder := getWith(t, handler, "/api/positions",
		map[string]string{"Authorization": "Bearer " + testToken})
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", recorder.Code, recorder.Body)
	}
	if len(positions.filters) != 0 {
		t.Error("query ran despite the lookup failure")
	}
}

func TestTokenCommandsNeedScopeAndOwnership(t *testing.T) {
	cases := []struct {
		name      string
		principal *auth.Principal
		deviceID  int64
		want      int
	}{
		{
			name:      "owned",
			principal: auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeWriteCommands}, []int64{7}),
			deviceID:  7,
			want:      http.StatusAccepted,
		},
		{
			name:      "unowned",
			principal: auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeWriteCommands}, []int64{7}),
			deviceID:  8,
			want:      http.StatusForbidden,
		},
		{
			name:      "missing scope",
			principal: auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeReadPositions}, []int64{7}),
			deviceID:  7,
			want:      http.StatusForbidden,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			sender := &fakeSender{}
			handler := NewServer(nil,
				WithCommands(sender, fakeDevices{protocol: "gt06", found: true}),
				withTestToken(test.principal),
			).Handler()

			body := `{"deviceId":` + strconv.FormatInt(test.deviceID, 10) + `,"type":"engineStop"}`
			recorder := withToken(t, handler, http.MethodPost, "/api/commands", body)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, test.want, recorder.Body)
			}
			if test.want != http.StatusAccepted && sender.sent != 0 {
				t.Error("command was sent despite the denial")
			}
		})
	}
}

// fakeOwnerDevices adds the optional ownership capability to the device store.
type fakeOwnerDevices struct {
	*fakeDeviceList

	mu        sync.Mutex
	assigned  [][2]int64
	assignErr error
}

func (f *fakeOwnerDevices) AssignDevice(_ context.Context, userID, deviceID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.assignErr != nil {
		return f.assignErr
	}
	f.assigned = append(f.assigned, [2]int64{userID, deviceID})
	return nil
}

func TestTokenCreateDeviceAssignsOwner(t *testing.T) {
	devices := &fakeOwnerDevices{fakeDeviceList: &fakeDeviceList{}}
	principal := auth.NewPrincipal(42, false, []auth.Scope{auth.ScopeWriteDevices}, nil)
	handler := NewServer(nil, WithDevices(devices), withTestToken(principal)).Handler()

	recorder := withToken(t, handler, http.MethodPost, "/api/devices",
		`{"uniqueId":"864717003283581"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body)
	}

	devices.mu.Lock()
	defer devices.mu.Unlock()
	if len(devices.assigned) != 1 || devices.assigned[0] != [2]int64{42, 1} {
		t.Fatalf("assignments = %v, want one (42, 1)", devices.assigned)
	}
}

func TestTokenCreateDeviceWithoutOwnershipSupportIsForbidden(t *testing.T) {
	devices := &fakeDeviceList{}
	principal := auth.NewPrincipal(42, false, []auth.Scope{auth.ScopeWriteDevices}, nil)
	handler := NewServer(nil, WithDevices(devices), withTestToken(principal)).Handler()

	recorder := withToken(t, handler, http.MethodPost, "/api/devices",
		`{"uniqueId":"864717003283581"}`)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
	if len(devices.created) != 0 {
		t.Error("device was created without an ownership row")
	}
}

func TestTokenCannotManageUnownedDevice(t *testing.T) {
	devices := &fakeDeviceList{}
	principal := auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeWriteDevices}, []int64{7})
	handler := NewServer(nil, WithDevices(devices), withTestToken(principal)).Handler()

	if recorder := withToken(t, handler, http.MethodPut, "/api/devices/8", `{"name":"x"}`); recorder.Code != http.StatusForbidden {
		t.Fatalf("update status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
	if recorder := withToken(t, handler, http.MethodDelete, "/api/devices/8", ""); recorder.Code != http.StatusForbidden {
		t.Fatalf("delete status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
	if len(devices.updated) != 0 || len(devices.deleted) != 0 {
		t.Error("unowned device was modified")
	}

	if recorder := withToken(t, handler, http.MethodPut, "/api/devices/7", `{"name":"x"}`); recorder.Code != http.StatusOK {
		t.Fatalf("owned update status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if recorder := withToken(t, handler, http.MethodDelete, "/api/devices/7", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("owned delete status = %d, want 204 (body %s)", recorder.Code, recorder.Body)
	}
}
