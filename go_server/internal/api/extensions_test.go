package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/ldap"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/totp"
)

type fakeQueryPositions struct {
	positions []model.Position
}

func (f fakeQueryPositions) QueryPositions(
	context.Context, model.PositionFilter,
) ([]model.Position, error) {
	return f.positions, nil
}

type fakeAttributes struct {
	attributes []*model.Attribute
}

func (f fakeAttributes) Attributes(context.Context, model.ResourceFilter) ([]*model.Attribute, error) {
	return f.attributes, nil
}

func decodeExtensionBody(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(recorder.Body).Decode(target); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
}

func TestComputedAttributeTestEvaluatesAgainstLatestPosition(t *testing.T) {
	position := model.NewPosition("gt06")
	position.DeviceID = 4
	position.Set(model.KeyFuel, 5.0)

	server := NewServer(nil,
		WithComputedAttributes(fakeAttributes{}),
		WithPositions(fakeQueryPositions{positions: []model.Position{*position}}))

	body := `{"deviceId":4,"expression":"fuel * 2","type":"number"}`
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/api/attributes/computed/test", bytes.NewBufferString(body)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	var value float64
	decodeExtensionBody(t, recorder, &value)
	if value != 10 {
		t.Fatalf("value = %v, want 10", value)
	}
}

func TestComputedAttributeTestRejectsBrokenExpression(t *testing.T) {
	server := NewServer(nil,
		WithComputedAttributes(fakeAttributes{}),
		WithPositions(fakeQueryPositions{}))

	body := `{"deviceId":4,"expression":"1 +"}`
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/api/attributes/computed/test", bytes.NewBufferString(body)))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

func TestAccumulatorsReadAndReset(t *testing.T) {
	device := &model.Device{
		ID:         9,
		UniqueID:   "864717003283581",
		Name:       "van",
		Attributes: model.Attributes{AccumulatorDistance: 12.5, AccumulatorHours: 3.0},
	}
	devices := &fakeDeviceList{device: device}

	server := NewServer(nil, WithDevices(devices))

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/devices/9/accumulators", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	var current accumulatorsResponse
	decodeExtensionBody(t, recorder, &current)
	if current.TotalDistance != 12.5 || current.Hours != 3 {
		t.Fatalf("accumulators = %+v", current)
	}

	devices.device = nil // the response is built from the update then
	devices.results = []model.Device{*device}
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPut,
		"/api/devices/9/accumulators", bytes.NewBufferString(`{"totalDistance":0}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if len(devices.updated) != 1 {
		t.Fatalf("updates = %d, want 1", len(devices.updated))
	}
	attributes := *devices.updated[0].Attributes
	if value := attributes.GetDouble(AccumulatorDistance, -1); value != 0 {
		t.Fatalf("totalDistance = %v, want 0", value)
	}
	if value := attributes.GetDouble(AccumulatorHours, -1); value != 3 {
		t.Fatalf("hours = %v, want the untouched 3", value)
	}
	if devices.updated[0].Name != "van" {
		t.Fatalf("the update wiped the device name: %+v", devices.updated[0])
	}
}

func TestDeviceImageLifecycle(t *testing.T) {
	directory := t.TempDir()
	devices := &fakeDeviceList{device: &model.Device{ID: 5, UniqueID: "1"}}
	server := NewServer(nil, WithDevices(devices), WithDeviceImages(directory))

	png := []byte("\x89PNG\r\n\x1a\n-extra-bytes")

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/api/devices/5/image", bytes.NewReader(png)))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("upload status = %d, body %s", recorder.Code, recorder.Body)
	}

	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/devices/5/image", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("download status = %d, body %s", recorder.Code, recorder.Body)
	}
	if got := recorder.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content type = %q", got)
	}
	if !bytes.Equal(recorder.Body.Bytes(), png) {
		t.Fatalf("body = %q, want %q", recorder.Body.Bytes(), png)
	}

	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete,
		"/api/devices/5/image", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", recorder.Code)
	}
	if _, err := os.Stat(filepath.Join(directory, "5")); !os.IsNotExist(err) {
		t.Fatalf("image file still exists: %v", err)
	}
}

func TestServerCacheReportsSizes(t *testing.T) {
	server := NewServer(nil, WithGeoCaches(
		NamedCache{Name: "geocoder", Size: func() int { return 3 }},
		NamedCache{Name: "geolocation", Size: func() int { return 1 }},
	))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/server/cache", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	var counts map[string]int
	decodeExtensionBody(t, recorder, &counts)
	if counts["geocoder"] != 3 || counts["geolocation"] != 1 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestStreamInfoAndPlaylist(t *testing.T) {
	root := t.TempDir()
	deviceDir := filepath.Join(root, "8")
	if err := os.MkdirAll(deviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	playlist := "#EXTM3U\n#EXT-X-VERSION:3\n"
	if err := os.WriteFile(filepath.Join(deviceDir, "index.m3u8"), []byte(playlist), 0o600); err != nil {
		t.Fatal(err)
	}

	server := NewServer(nil, WithStreams(root))

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/stream/8", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("info status = %d, body %s", recorder.Code, recorder.Body)
	}
	var info map[string]string
	decodeExtensionBody(t, recorder, &info)
	if info["url"] != "/api/stream/8/index.m3u8" {
		t.Fatalf("url = %q", info["url"])
	}

	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/stream/8/index.m3u8", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("playlist status = %d, body %s", recorder.Code, recorder.Body)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/vnd.apple.mpegurl" {
		t.Fatalf("content type = %q", got)
	}
	if recorder.Body.String() != playlist {
		t.Fatalf("playlist = %q", recorder.Body.String())
	}
}

type fakeTOTP struct {
	secret  string
	enabled bool
}

func (f *fakeTOTP) UserTOTP(context.Context, int64) (string, bool, error) {
	return f.secret, f.enabled, nil
}

func (f *fakeTOTP) SetUserTOTP(_ context.Context, _ int64, secret string, enabled bool) error {
	f.secret = secret
	f.enabled = enabled && secret != ""
	return nil
}

func TestTOTPGenerateVerifyDisable(t *testing.T) {
	store := &fakeTOTP{}
	server := NewServer(nil, WithTOTP(store))
	server.now = func() time.Time { return time.Unix(1111111109, 0) }

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/api/users/1/totp", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("generate status = %d, body %s", recorder.Code, recorder.Body)
	}
	var generated map[string]string
	decodeExtensionBody(t, recorder, &generated)
	if generated["secret"] == "" || generated["url"] == "" {
		t.Fatalf("generated = %v", generated)
	}
	if store.enabled {
		t.Fatal("a fresh secret must not enable the account")
	}

	code, err := totp.Code(store.secret, server.now())
	if err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPut,
		"/api/users/1/totp", bytes.NewBufferString(`{"code":"`+code+`"}`)))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("enable status = %d, body %s", recorder.Code, recorder.Body)
	}
	if !store.enabled {
		t.Fatal("a valid code must enable the account")
	}

	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/users/1/totp", nil))
	var status map[string]bool
	decodeExtensionBody(t, recorder, &status)
	if !status["enabled"] {
		t.Fatalf("status = %v", status)
	}

	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete,
		"/api/users/1/totp", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("disable status = %d", recorder.Code)
	}
	if store.secret != "" || store.enabled {
		t.Fatalf("store = %+v", store)
	}
}

// roundTripFunc lets a test answer an HTTP request without binding a port,
// which the sandbox does not allow.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestOpenIDLoginCreatesSession(t *testing.T) {
	const issuer = "https://issuer.test"
	users := newFakeUsers(&model.User{ID: 3, Email: "driver@example.com", Name: "Driver"})
	server := NewServer(nil, WithUsers(users), WithOpenID(OpenIDSettings{
		Enabled:      true,
		IssuerURL:    issuer,
		ClientID:     "client",
		ClientSecret: "secret",
		RedirectURL:  "http://localhost/api/session/openid/callback",
		SuccessURL:   "/",
	}))

	server.openID.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			return jsonResponse(`{"issuer":"` + issuer + `",` +
				`"authorization_endpoint":"` + issuer + `/authorize",` +
				`"token_endpoint":"` + issuer + `/token",` +
				`"userinfo_endpoint":"` + issuer + `/userinfo"}`), nil
		case "/token":
			if err := request.ParseForm(); err != nil || request.PostForm.Get("code") != "code-1" {
				return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			return jsonResponse(`{"access_token":"token-1"}`), nil
		case "/userinfo":
			if request.Header.Get("Authorization") != "Bearer token-1" {
				return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			return jsonResponse(`{"email":"driver@example.com","name":"Driver"}`), nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/session/openid/auth", nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("auth status = %d, body %s", recorder.Code, recorder.Body)
	}
	location := recorder.Header().Get("Location")
	if location == "" || len(recorder.Result().Cookies()) == 0 {
		t.Fatalf("auth redirect = %q cookies %v", location, recorder.Result().Cookies())
	}
	state := recorder.Result().Cookies()[0].Value

	request := httptest.NewRequest(http.MethodGet,
		"/api/session/openid/callback?code=code-1&state="+state, nil)
	request.AddCookie(&http.Cookie{Name: openIDStateCookie, Value: state})
	request.Header.Set("Accept", "application/json")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("callback status = %d, body %s", recorder.Code, recorder.Body)
	}
	var issued tokenIssueResponse
	decodeExtensionBody(t, recorder, &issued)
	if issued.Token == "" {
		t.Fatalf("issued = %+v", issued)
	}
	if len(users.tokens[3]) != 1 {
		t.Fatalf("tokens = %+v", users.tokens)
	}
}

func TestOpenIDCallbackRejectsWrongState(t *testing.T) {
	server := NewServer(nil, WithOpenID(OpenIDSettings{
		Enabled:   true,
		IssuerURL: "http://127.0.0.1:1",
		ClientID:  "client",
	}))
	request := httptest.NewRequest(http.MethodGet,
		"/api/session/openid/callback?code=x&state=forged", nil)
	request.AddCookie(&http.Cookie{Name: openIDStateCookie, Value: "expected"})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

func TestPermissionsBulkRouteIsMounted(t *testing.T) {
	// The bulk route shares the permission handler; without a store it must
	// answer 501 rather than 404, which proves the route exists.
	server := NewServer(nil)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(method,
			"/api/permissions/bulk", bytes.NewBufferString(`[]`)))
		if recorder.Code != http.StatusNotImplemented {
			t.Fatalf("%s /api/permissions/bulk status = %d", method, recorder.Code)
		}
	}
}

func TestPasswordUpdateRouteIsMounted(t *testing.T) {
	server := NewServer(nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/api/password/update", bytes.NewBufferString(`{"password":"secret"}`)))
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}

type fakeSocketRemote struct {
	calls    int
	channel  string
	deviceID int64
	payload  []byte
}

func (f *fakeSocketRemote) Publish(channel string, deviceID int64, payload []byte) {
	f.calls++
	f.channel = channel
	f.deviceID = deviceID
	f.payload = payload
}

func TestSocketHubPublishesToTheRemoteNode(t *testing.T) {
	remote := &fakeSocketRemote{}
	hub := NewSocketHub()
	hub.SetRemote(remote)

	position := model.NewPosition("gt06")
	position.DeviceID = 42
	hub.PublishPosition(position)

	if remote.calls != 1 {
		t.Fatalf("calls = %d, want 1", remote.calls)
	}
	if remote.channel != "positions" || remote.deviceID != 42 {
		t.Fatalf("channel = %q device = %d", remote.channel, remote.deviceID)
	}
	if !strings.Contains(string(remote.payload), `"positions"`) {
		t.Fatalf("payload = %s", remote.payload)
	}
}

func TestSocketHubWithoutClientsOrRemoteSkipsEncoding(t *testing.T) {
	hub := NewSocketHub()
	hub.PublishPosition(model.NewPosition("gt06"))
	if hub.active() {
		t.Fatal("an empty hub without a remote must stay inactive")
	}
}

type fakeLDAP struct {
	identity *ldap.Identity
	err      error
}

func (f fakeLDAP) Authenticate(context.Context, string, string) (*ldap.Identity, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.identity, nil
}

func TestLDAPLoginCreatesTheAccount(t *testing.T) {
	users := newFakeUsers()
	server := NewServer(nil, WithUsers(users), WithLDAP(fakeLDAP{
		identity: &ldap.Identity{
			DN:    "cn=alice,dc=example,dc=com",
			Email: "alice@example.com",
			Name:  "Alice",
		},
	}))

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/api/login", bytes.NewBufferString(`{"email":"alice@example.com","password":"secret"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	var issued tokenIssueResponse
	decodeExtensionBody(t, recorder, &issued)
	if issued.Token == "" {
		t.Fatalf("issued = %+v", issued)
	}
	if len(users.users) != 1 {
		t.Fatalf("users = %d, want the directory account to be created", len(users.users))
	}
	for _, user := range users.users {
		if user.Email != "alice@example.com" || user.Name != "Alice" {
			t.Fatalf("user = %+v", user)
		}
	}
}

func TestLDAPLoginRejectsAWrongPassword(t *testing.T) {
	users := newFakeUsers()
	server := NewServer(nil, WithUsers(users), WithLDAP(fakeLDAP{err: ldap.ErrInvalidCredentials}))

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/api/login", bytes.NewBufferString(`{"email":"alice@example.com","password":"wrong"}`)))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
}
