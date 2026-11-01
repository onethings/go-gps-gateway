package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

// fakeProtocolSettings is the in-memory version of protocol_settings.
type fakeProtocolSettings struct {
	mu      sync.Mutex
	rows    []model.ProtocolSettings
	err     error
	applied []model.ProtocolSettings
}

func (f *fakeProtocolSettings) ProtocolSettings(context.Context) ([]model.ProtocolSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.ProtocolSettings(nil), f.rows...), f.err
}

func (f *fakeProtocolSettings) UpdateProtocolSettings(
	_ context.Context, id int64, port int, transport string,
) (*model.ProtocolSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.rows {
		if f.rows[index].ID != id {
			continue
		}
		f.rows[index].Port = port
		f.rows[index].Transport = transport
		updated := f.rows[index]
		return &updated, nil
	}
	return nil, model.ErrProtocolNotFound
}

func (f *fakeProtocolSettings) ApplyProtocolSettings(_ context.Context, settings model.ProtocolSettings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, settings)
	return nil
}

// fakeServerSettings is the in-memory version of server_settings.
type fakeServerSettings struct {
	mu       sync.Mutex
	settings model.Server
	err      error
}

func (f *fakeServerSettings) ServerSettings(context.Context) (*model.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	copied := f.settings
	return &copied, nil
}

func (f *fakeServerSettings) UpdateServerSettings(_ context.Context, settings *model.Server) (*model.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := *settings
	f.settings = copied
	return &copied, nil
}

// fakeStatisticsStore serves the statistics report and the per-device counts.
type fakeStatisticsStore struct {
	mu         sync.Mutex
	snapshots  []*model.Statistics
	counts     map[int64]int64
	lastFrom   time.Time
	lastTo     time.Time
	countCalls int
}

func (f *fakeStatisticsStore) QueryStatistics(_ context.Context, from, to time.Time) ([]*model.Statistics, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastFrom, f.lastTo = from, to
	return f.snapshots, nil
}

func (f *fakeStatisticsStore) PositionCounts(
	_ context.Context, deviceIDs []int64, from, to time.Time,
) (map[int64]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastFrom, f.lastTo = from, to
	f.countCalls++
	counts := make(map[int64]int64, len(deviceIDs))
	for _, deviceID := range deviceIDs {
		counts[deviceID] = f.counts[deviceID]
	}
	return counts, nil
}

// fakeAudit is the in-memory audit trail.
type fakeAudit struct {
	mu      sync.Mutex
	actions []model.Action
	err     error
}

func (f *fakeAudit) RecordAction(_ context.Context, action *model.Action) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	action.ID = int64(len(f.actions) + 1)
	f.actions = append(f.actions, *action)
	return nil
}

func (f *fakeAudit) QueryActions(
	_ context.Context, from, to time.Time, limit, offset int,
) ([]model.Action, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if limit > 0 && len(f.actions) > limit {
		return f.actions[:limit], nil
	}
	return f.actions, nil
}

// fakeCatalogRouter is a command router that also reports the protocol
// command types.
type fakeCatalogRouter struct {
	fakeSender
	types map[string][]string
}

func (f *fakeCatalogRouter) SupportedCommands(protocol string, textChannel bool) ([]string, bool) {
	if !textChannel {
		return nil, false
	}
	types, ok := f.types[protocol]
	return types, ok
}

func TestProtocolsListAndUpdate(t *testing.T) {
	store := &fakeProtocolSettings{rows: []model.ProtocolSettings{
		{ID: 1, Protocol: "gt06", Port: 5023, Transport: model.TransportTCP},
		{ID: 2, Protocol: "h02", Port: 5013, Transport: model.TransportTCP},
	}}
	handler := NewServer(nil, WithProtocolSettings(store, store)).Handler()

	recorder := get(t, handler, "/api/protocols")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/protocols = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	var rows []protocolResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if len(rows) != 2 || rows[0].Protocol != "gt06" || rows[1].Protocol != "h02" {
		t.Fatalf("rows = %+v", rows)
	}

	updated := doJSON(t, handler, http.MethodPut, "/api/protocols/1", `{"port":5024,"transport":"udp"}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 (body %s)", updated.Code, updated.Body)
	}
	var row protocolResponse
	if err := json.Unmarshal(updated.Body.Bytes(), &row); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if row.Port != 5024 || row.Transport != "udp" {
		t.Errorf("row = %+v", row)
	}
	if len(store.applied) != 1 || store.applied[0].Port != 5024 {
		t.Errorf("applied = %+v", store.applied)
	}

	invalid := doJSON(t, handler, http.MethodPut, "/api/protocols/1", `{"port":1,"transport":"sctp"}`)
	if invalid.Code != http.StatusBadRequest {
		t.Errorf("invalid transport = %d, want 400", invalid.Code)
	}
	missing := doJSON(t, handler, http.MethodPut, "/api/protocols/99", `{"port":1}`)
	if missing.Code != http.StatusNotFound {
		t.Errorf("unknown row = %d, want 404", missing.Code)
	}
}

func TestProtocolUpdateRequiresAdmin(t *testing.T) {
	store := &fakeProtocolSettings{rows: []model.ProtocolSettings{
		{ID: 1, Protocol: "gt06", Port: 5023, Transport: model.TransportTCP},
	}}
	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadDevices, auth.ScopeWriteDevices}, nil)
	handler := NewServer(nil, WithProtocolSettings(store, nil),
		withTestToken(principal)).Handler()

	recorder := withToken(t, handler, http.MethodPut, "/api/protocols/1", `{"port":1}`)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestServerSettingsAnonymousAndAdminUpdate(t *testing.T) {
	store := &fakeServerSettings{settings: model.Server{
		ID: 1, Registration: true, Announcement: "hello",
		Attributes: model.Attributes{"disableShare": true},
	}}
	admin := auth.AdminPrincipal()
	users := newFakeUsers(&model.User{ID: 1, Email: "admin@example.com", Admin: true})
	handler := NewServer(nil,
		WithServerSettings(store),
		WithUsers(users),
		WithAPIKey("secret"),
		withTestToken(admin),
	).Handler()

	// The login page asks for the settings before it has a credential.
	anonymous := get(t, handler, "/api/server")
	if anonymous.Code != http.StatusOK {
		t.Fatalf("anonymous GET = %d, want 200 (body %s)", anonymous.Code, anonymous.Body)
	}
	var settings serverResponse
	if err := json.Unmarshal(anonymous.Body.Bytes(), &settings); err != nil {
		t.Fatalf("decode: %v (body %s)", err, anonymous.Body)
	}
	if !settings.Registration || settings.Announcement != "hello" {
		t.Errorf("settings = %+v", settings)
	}
	if settings.NewServer {
		t.Error("newServer = true with an existing account")
	}

	updated := doJSONWithToken(t, handler, http.MethodPut, "/api/server",
		`{"announcement":"bye","readonly":true}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 (body %s)", updated.Code, updated.Body)
	}
	if !store.settings.Readonly || store.settings.Announcement != "bye" {
		t.Errorf("stored = %+v", store.settings)
	}
	if !store.settings.Registration {
		t.Error("partial update cleared a field it did not send")
	}
}

func TestServerSettingsUpdateRequiresAdmin(t *testing.T) {
	store := &fakeServerSettings{settings: model.Server{ID: 1}}
	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeAll}, nil)
	handler := NewServer(nil, WithServerSettings(store), withTestToken(principal)).Handler()

	recorder := withToken(t, handler, http.MethodPut, "/api/server", `{"readonly":true}`)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestStatisticsReport(t *testing.T) {
	captured := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	store := &fakeStatisticsStore{snapshots: []*model.Statistics{{
		ID: 3, CaptureTime: captured, MessagesStored: 12,
		Protocols: map[string]int{"gt06": 12},
	}}}
	handler := NewServer(nil, WithStatistics(store), WithAPIKey("secret")).Handler()

	recorder := getWith(t, handler, "/api/statistics?from=2026-11-01T00:00:00Z&to=2026-11-02T00:00:00Z",
		map[string]string{"X-Api-Key": "secret"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	var rows []statisticsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if len(rows) != 1 || rows[0].MessagesStored != 12 || rows[0].Protocols["gt06"] != 12 {
		t.Fatalf("rows = %+v", rows)
	}
	if !store.lastFrom.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("from = %v", store.lastFrom)
	}
}

func TestStatisticsDevicesRejectsLongPeriod(t *testing.T) {
	store := &fakeStatisticsStore{counts: map[int64]int64{7: 42}}
	devices := &fakeDeviceList{results: []model.Device{{ID: 7, Name: "Van"}}}
	handler := NewServer(nil, WithStatistics(store), WithDevices(devices), WithAPIKey("secret")).Handler()

	tooLong := getWith(t, handler,
		"/api/statistics/devices?from=2026-11-01T00:00:00Z&to=2026-11-03T00:00:00Z",
		map[string]string{"X-Api-Key": "secret"})
	if tooLong.Code != http.StatusBadRequest {
		t.Fatalf("long period = %d, want 400 (body %s)", tooLong.Code, tooLong.Body)
	}

	ok := getWith(t, handler,
		"/api/statistics/devices?from=2026-11-01T00:00:00Z&to=2026-11-01T12:00:00Z",
		map[string]string{"X-Api-Key": "secret"})
	if ok.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200 (body %s)", ok.Code, ok.Body)
	}
	var rows []deviceStatisticsResponse
	if err := json.Unmarshal(ok.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v (body %s)", err, ok.Body)
	}
	if len(rows) != 1 || rows[0].DeviceID != 7 || rows[0].MessagesStored != 42 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestAuditRequiresAdmin(t *testing.T) {
	actionTime := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	store := &fakeAudit{actions: []model.Action{{
		ID: 1, ActionTime: actionTime, UserID: 2, UserEmail: "admin@example.com",
		ActionType: model.ActionEdit, ObjectType: "device", ObjectID: 7,
	}}}
	handler := NewServer(nil, WithAudit(store), WithAPIKey("secret")).Handler()

	recorder := getWith(t, handler, "/api/audit", map[string]string{"X-Api-Key": "secret"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	var rows []actionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if len(rows) != 1 || rows[0].ActionType != model.ActionEdit || rows[0].UserEmail != "admin@example.com" {
		t.Fatalf("rows = %+v", rows)
	}

	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeAll}, nil)
	scoped := NewServer(nil, WithAudit(store), withTestToken(principal)).Handler()
	if forbidden := withToken(t, scoped, http.MethodGet, "/api/audit", ""); forbidden.Code != http.StatusForbidden {
		t.Errorf("non-admin = %d, want 403", forbidden.Code)
	}
}

func TestSessionCookieFlow(t *testing.T) {
	hash, err := auth.HashPassword("secret")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	users := newFakeUsers(&model.User{ID: 1, Email: "user@example.com", Name: "User"})
	users.passwords[1] = hash
	tokens := fakeTokens{tokens: map[string]*auth.Principal{
		auth.HashToken("trc_fake_plaintext"): auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeAll}, nil),
	}}
	handler := NewServer(nil, WithUsers(users), WithTokens(tokens)).Handler()

	form := url.Values{"email": {"user@example.com"}, "password": {"secret"}}
	request := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /api/session = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	var user userResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &user); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if user.ID != 1 || user.Email != "user@example.com" {
		t.Fatalf("user = %+v", user)
	}

	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != SessionCookieName || cookies[0].Value == "" {
		t.Fatalf("cookies = %+v", cookies)
	}

	// The cookie alone authenticates the next request.
	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	sessionRequest.AddCookie(cookies[0])
	sessionRecorder := httptest.NewRecorder()
	handler.ServeHTTP(sessionRecorder, sessionRequest)
	if sessionRecorder.Code != http.StatusOK {
		t.Fatalf("GET /api/session with cookie = %d, want 200 (body %s)",
			sessionRecorder.Code, sessionRecorder.Body)
	}

	// Without a credential the route answers 404 so the web interface shows the
	// login page instead of an error.
	if anonymous := get(t, handler, "/api/session"); anonymous.Code != http.StatusNotFound {
		t.Errorf("anonymous GET = %d, want 404", anonymous.Code)
	}

	// Logout revokes the token and drops the cookie.
	users.mu.Lock()
	users.tokens[1] = nil
	users.mu.Unlock()
	logoutRequest := httptest.NewRequest(http.MethodDelete, "/api/session", nil)
	logoutRequest.AddCookie(cookies[0])
	logoutRecorder := httptest.NewRecorder()
	handler.ServeHTTP(logoutRecorder, logoutRequest)
	if logoutRecorder.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/session = %d, want 204", logoutRecorder.Code)
	}
}

func TestSessionTokenQueryLogsIn(t *testing.T) {
	users := newFakeUsers(&model.User{ID: 4, Email: "qr@example.com"})
	tokens := fakeTokens{tokens: map[string]*auth.Principal{
		auth.HashToken("qr-token"): auth.NewPrincipal(4, false, []auth.Scope{auth.ScopeAll}, nil),
	}}
	handler := NewServer(nil, WithUsers(users), WithTokens(tokens)).Handler()

	recorder := get(t, handler, "/api/session?token=qr-token")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if len(recorder.Result().Cookies()) == 0 {
		t.Error("token login did not set a session cookie")
	}
	if unknown := get(t, handler, "/api/session?token=nope"); unknown.Code != http.StatusNotFound {
		t.Errorf("unknown token = %d, want 404", unknown.Code)
	}
}

func TestSessionTokenIssueAndRevoke(t *testing.T) {
	users := newFakeUsers(&model.User{ID: 1, Email: "user@example.com"})
	principal := auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeAll}, nil)
	handler := NewServer(nil, WithUsers(users), withTestToken(principal)).Handler()

	recorder := withToken(t, handler, http.MethodPost, "/api/session/token", `{"expiresIn":"1h"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /api/session/token = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if token := strings.TrimSpace(recorder.Body.String()); token != "trc_fake_plaintext" {
		t.Errorf("token = %q", token)
	}

	invalid := withToken(t, handler, http.MethodPost, "/api/session/token", `{"expiresIn":"-5m"}`)
	if invalid.Code != http.StatusBadRequest {
		t.Errorf("negative expiry = %d, want 400", invalid.Code)
	}
}

func TestCommandTypesAndGroupSend(t *testing.T) {
	router := &fakeCatalogRouter{types: map[string][]string{
		"gt06": {model.CommandEngineStop, model.CommandEngineResume, model.CommandCustom},
	}}
	permissionsStore := newFakePermissions()
	handler := NewServer(nil,
		WithCommands(router, fakeDevices{protocol: "gt06", found: true}),
		WithPermissions(permissionsStore),
		WithAPIKey("secret"),
	).Handler()

	typesRecorder := getWith(t, handler, "/api/commands/types?deviceId=7&textChannel=true",
		map[string]string{"X-Api-Key": "secret"})
	if typesRecorder.Code != http.StatusOK {
		t.Fatalf("GET types = %d, want 200 (body %s)", typesRecorder.Code, typesRecorder.Body)
	}
	var types []commandTypeResponse
	if err := json.Unmarshal(typesRecorder.Body.Bytes(), &types); err != nil {
		t.Fatalf("decode: %v (body %s)", err, typesRecorder.Body)
	}
	if len(types) != 3 {
		t.Fatalf("types = %+v", types)
	}

	unknown := getWith(t, handler, "/api/commands/types?deviceId=7&textChannel=false",
		map[string]string{"X-Api-Key": "secret"})
	if unknown.Code != http.StatusConflict {
		t.Errorf("data channel types = %d, want 409", unknown.Code)
	}

	groupSend := postWithKey(t, handler, "/api/commands/send?groupId=1",
		`{"type":"custom","attributes":{"data":"hello"}}`)
	if groupSend.Code != http.StatusOK {
		t.Fatalf("POST send group = %d, want 200 (body %s)", groupSend.Code, groupSend.Body)
	}
	var response groupCommandResponse
	if err := json.Unmarshal(groupSend.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body %s)", err, groupSend.Body)
	}
	if response.Count != 1 || response.Commands[0].DeviceID != 7 || len(response.Failed) != 0 {
		t.Fatalf("response = %+v", response)
	}
}

func TestNotificatorsAndTypes(t *testing.T) {
	handler := NewServer(nil, WithNotificators([]string{"mail", "telegram"}),
		WithAPIKey("secret")).Handler()

	recorder := getWith(t, handler, "/api/notifications/notificators",
		map[string]string{"X-Api-Key": "secret"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET notificators = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	var notificators []notificatorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &notificators); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if len(notificators) != 2 || notificators[1].Type != "telegram" {
		t.Fatalf("notificators = %+v", notificators)
	}

	typesRecorder := getWith(t, handler, "/api/notifications/types",
		map[string]string{"X-Api-Key": "secret"})
	if typesRecorder.Code != http.StatusOK {
		t.Fatalf("GET types = %d, want 200", typesRecorder.Code)
	}
	var types []notificatorResponse
	if err := json.Unmarshal(typesRecorder.Body.Bytes(), &types); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(types) == 0 || types[0].Type != "all" {
		t.Fatalf("types = %+v", types)
	}
}

// doJSON sends a JSON request as the legacy shared key, which is the admin
// credential when authentication is off.
func doJSON(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}

func doJSONWithToken(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Api-Key", "secret")
	handler.ServeHTTP(recorder, request)
	return recorder
}

func postWithKey(t *testing.T, handler http.Handler, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Api-Key", "secret")
	handler.ServeHTTP(recorder, request)
	return recorder
}

// fakeNotifier records the test and announcement messages the API asks for.
type fakeNotifier struct {
	mu       sync.Mutex
	channels []string
	sent     []notifierCall
	err      error
}

type notifierCall struct {
	Channel string
	UserID  int64
	Subject string
	Body    string
}

func (f *fakeNotifier) Channels() []string { return f.channels }

func (f *fakeNotifier) SendTo(
	_ context.Context, channel string, user model.User, subject, body string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, notifierCall{
		Channel: channel, UserID: user.ID, Subject: subject, Body: body,
	})
	return nil
}

func TestNotificationTestAndSend(t *testing.T) {
	users := newFakeUsers(
		&model.User{ID: 1, Email: "one@example.com"},
		&model.User{ID: 2, Email: "two@example.com"},
	)
	sender := &fakeNotifier{channels: []string{"telegram", "mail"}}
	handler := NewServer(nil,
		WithUsers(users),
		WithNotifierSender(sender),
		WithAPIKey("secret"),
	).Handler()

	testRecorder := postWithKey(t, handler, "/api/notifications/test/telegram",
		`{"subject":"Hi","body":"There"}`)
	if testRecorder.Code != http.StatusNoContent {
		t.Fatalf("test = %d, want 204 (body %s)", testRecorder.Code, testRecorder.Body)
	}
	if len(sender.sent) != 1 || sender.sent[0].Channel != "telegram" ||
		sender.sent[0].Subject != "Hi" {
		t.Fatalf("sent = %+v", sender.sent)
	}

	allRecorder := postWithKey(t, handler, "/api/notifications/test", `{}`)
	if allRecorder.Code != http.StatusNoContent {
		t.Fatalf("test all = %d, want 204 (body %s)", allRecorder.Code, allRecorder.Body)
	}
	if len(sender.sent) != 3 {
		t.Fatalf("sent = %+v", sender.sent)
	}

	sendRecorder := postWithKey(t, handler,
		"/api/notifications/send/telegram?userId=1&userId=2", `{"subject":"All","body":"Notice"}`)
	if sendRecorder.Code != http.StatusNoContent {
		t.Fatalf("send = %d, want 204 (body %s)", sendRecorder.Code, sendRecorder.Body)
	}
	if len(sender.sent) != 5 || sender.sent[3].UserID != 1 || sender.sent[4].UserID != 2 {
		t.Fatalf("sent = %+v", sender.sent)
	}
}

func TestNotificatorsAnnouncementFilter(t *testing.T) {
	sender := &fakeNotifier{channels: []string{"telegram", "webhook", "command"}}
	handler := NewServer(nil, WithNotifierSender(sender), WithAPIKey("secret")).Handler()

	recorder := getWith(t, handler, "/api/notifications/notificators?announcement=true",
		map[string]string{"X-Api-Key": "secret"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}
	var list []notificatorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 || list[0].Type != "telegram" {
		t.Fatalf("list = %+v", list)
	}
}

// fakeGeocoder answers with a fixed address or a fixed error.
type fakeGeocoder struct {
	address string
	err     error
}

func (f fakeGeocoder) Address(context.Context, float64, float64) (string, error) {
	return f.address, f.err
}

func TestServerGeocode(t *testing.T) {
	handler := NewServer(nil,
		WithGeocoder(fakeGeocoder{address: "Riga, Latvia"}),
		withTestToken(auth.NewPrincipal(1, false,
			[]auth.Scope{auth.ScopeReadPositions}, nil)),
	).Handler()

	recorder := withToken(t, handler, http.MethodGet,
		"/api/server/geocode?latitude=54.94535&longitude=24.01762", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}
	var address string
	if err := json.Unmarshal(recorder.Body.Bytes(), &address); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if address != "Riga, Latvia" {
		t.Fatalf("address = %q", address)
	}
}

func TestServerGeocodeWithoutAService(t *testing.T) {
	handler := NewServer(nil, withTestToken(auth.AdminPrincipal())).Handler()
	recorder := withToken(t, handler, http.MethodGet,
		"/api/server/geocode?latitude=1&longitude=2", "")
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", recorder.Code)
	}
}

// fakeSpeedLimits answers with a fixed limit, or reports it does not know.
type fakeSpeedLimits struct {
	limit float64
	known bool
	err   error
}

func (f fakeSpeedLimits) Limit(context.Context, float64, float64) (float64, bool, error) {
	return f.limit, f.known, f.err
}

func TestServerSpeedLimit(t *testing.T) {
	handler := NewServer(nil,
		WithSpeedLimits(fakeSpeedLimits{limit: 26.99, known: true}),
		withTestToken(auth.NewPrincipal(1, false,
			[]auth.Scope{auth.ScopeReadPositions}, nil)),
	).Handler()

	recorder := withToken(t, handler, http.MethodGet,
		"/api/server/speedlimit?latitude=54.94535&longitude=24.01762", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}
	var limit float64
	if err := json.Unmarshal(recorder.Body.Bytes(), &limit); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if limit != 26.99 {
		t.Fatalf("limit = %v", limit)
	}

	unknown := NewServer(nil,
		WithSpeedLimits(fakeSpeedLimits{}),
		withTestToken(auth.AdminPrincipal()),
	).Handler()
	if status := withToken(t, unknown, http.MethodGet,
		"/api/server/speedlimit?latitude=1&longitude=2", "").Code; status != http.StatusNotFound {
		t.Fatalf("unknown limit status = %d, want 404", status)
	}
}

// fakeGeolocator answers a fixed coordinate.
type fakeGeolocator struct {
	latitude  float64
	longitude float64
	found     bool
}

func (f fakeGeolocator) Locate(context.Context, *model.Network) (float64, float64, bool) {
	return f.latitude, f.longitude, f.found
}

func TestServerGeolocation(t *testing.T) {
	handler := NewServer(nil,
		WithGeolocation(fakeGeolocator{latitude: 54.9, longitude: 24.0, found: true}),
		withTestToken(auth.NewPrincipal(1, false,
			[]auth.Scope{auth.ScopeReadPositions}, nil)),
	).Handler()

	body := `{"homeMobileCountryCode":247,"homeMobileNetworkCode":1,"radioType":"lte",
		"cellTowers":[{"cellId":2000,"locationAreaCode":100,"mobileCountryCode":247,"mobileNetworkCode":1}]}`
	recorder := withToken(t, handler, http.MethodPost, "/api/server/geolocation", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}
	var response map[string]float64
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response["latitude"] != 54.9 || response["longitude"] != 24.0 {
		t.Fatalf("response = %v", response)
	}
}

func TestServerGeolocationNeedsCells(t *testing.T) {
	handler := NewServer(nil,
		WithGeolocation(fakeGeolocator{found: true}),
		withTestToken(auth.AdminPrincipal()),
	).Handler()

	if status := withToken(t, handler, http.MethodPost, "/api/server/geolocation", `{}`).Code; status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}
