package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

const testAdminKey = "admin-key"

// fakeUsers is an in-memory UserStore. It keeps just enough state to exercise
// the handlers: accounts, device grants and tokens.
type fakeUsers struct {
	mu       sync.Mutex
	users    map[int64]*model.User
	grants   map[int64][]int64
	tokens   map[int64][]model.APIToken
	nextID   int64
	nextTok  int64
	err      error
	lastList model.UserFilter
}

func newFakeUsers(users ...*model.User) *fakeUsers {
	store := &fakeUsers{
		users:   make(map[int64]*model.User, len(users)),
		grants:  make(map[int64][]int64),
		tokens:  make(map[int64][]model.APIToken),
		nextID:  int64(len(users)),
		nextTok: 1,
	}
	for _, user := range users {
		copied := *user
		store.users[user.ID] = &copied
		if user.ID > store.nextID {
			store.nextID = user.ID
		}
	}
	return store
}

func (f *fakeUsers) fail() error {
	return f.err
}

func (f *fakeUsers) Users(_ context.Context, filter model.UserFilter) ([]model.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastList = filter
	if err := f.fail(); err != nil {
		return nil, err
	}

	users := make([]model.User, 0, len(f.users))
	for id := int64(1); id <= f.nextID; id++ {
		user, ok := f.users[id]
		if !ok {
			continue
		}
		copied := *user
		copied.DeviceCount = len(f.grants[id])
		copied.TokenCount = len(f.tokens[id])
		users = append(users, copied)
	}
	return users, nil
}

func (f *fakeUsers) User(_ context.Context, userID int64) (*model.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return nil, err
	}
	user, ok := f.users[userID]
	if !ok {
		return nil, model.ErrUserNotFound
	}
	copied := *user
	return &copied, nil
}

func (f *fakeUsers) CreateUser(_ context.Context, create model.UserCreate) (*model.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return nil, err
	}
	for _, user := range f.users {
		if user.Email == create.Email {
			return nil, model.ErrDuplicateUser
		}
	}
	f.nextID++
	user := &model.User{
		ID:        f.nextID,
		Email:     create.Email,
		Name:      create.Name,
		Admin:     create.Admin,
		CreatedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	}
	f.users[user.ID] = user
	copied := *user
	return &copied, nil
}

func (f *fakeUsers) UpdateUser(_ context.Context, userID int64, update model.UserUpdate) (*model.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return nil, err
	}
	user, ok := f.users[userID]
	if !ok {
		return nil, model.ErrUserNotFound
	}
	if update.Name != nil {
		user.Name = *update.Name
	}
	if update.Admin != nil {
		user.Admin = *update.Admin
	}
	if update.Disabled != nil {
		user.Disabled = *update.Disabled
	}
	copied := *user
	return &copied, nil
}

func (f *fakeUsers) DeleteUser(_ context.Context, userID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return err
	}
	if _, ok := f.users[userID]; !ok {
		return model.ErrUserNotFound
	}
	delete(f.users, userID)
	delete(f.grants, userID)
	delete(f.tokens, userID)
	return nil
}

func (f *fakeUsers) UserDevices(_ context.Context, userID int64) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return nil, err
	}
	if _, ok := f.users[userID]; !ok {
		return nil, model.ErrUserNotFound
	}
	return append([]int64{}, f.grants[userID]...), nil
}

func (f *fakeUsers) AssignDevice(_ context.Context, userID, deviceID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return err
	}
	for _, granted := range f.grants[userID] {
		if granted == deviceID {
			return nil
		}
	}
	f.grants[userID] = append(f.grants[userID], deviceID)
	return nil
}

func (f *fakeUsers) UnassignDevice(_ context.Context, userID, deviceID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return err
	}
	grants := f.grants[userID][:0]
	for _, granted := range f.grants[userID] {
		if granted != deviceID {
			grants = append(grants, granted)
		}
	}
	f.grants[userID] = grants
	return nil
}

func (f *fakeUsers) Tokens(_ context.Context, userID int64) ([]model.APIToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return nil, err
	}
	if _, ok := f.users[userID]; !ok {
		return nil, model.ErrUserNotFound
	}
	return append([]model.APIToken{}, f.tokens[userID]...), nil
}

func (f *fakeUsers) IssueTokenForUser(_ context.Context, userID int64, scopes []string, expiresAt *time.Time) (*model.APIToken, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return nil, "", err
	}
	if _, ok := f.users[userID]; !ok {
		return nil, "", model.ErrUserNotFound
	}
	token := model.APIToken{
		ID:        f.nextTok,
		UserID:    userID,
		Scopes:    scopes,
		ExpiresAt: expiresAt,
		CreatedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	}
	f.nextTok++
	f.tokens[userID] = append(f.tokens[userID], token)
	return &token, "trc_fake_plaintext", nil
}

func (f *fakeUsers) RevokeToken(_ context.Context, userID, tokenID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return err
	}
	tokens := f.tokens[userID]
	for index, token := range tokens {
		if token.ID == tokenID {
			f.tokens[userID] = append(tokens[:index], tokens[index+1:]...)
			return nil
		}
	}
	return model.ErrTokenNotFound
}

// adminUsersHandler builds a handler whose shared key is an administrator.
func adminUsersHandler(users UserStore, devices DeviceStore) http.Handler {
	options := []Option{WithUsers(users), WithAPIKey(testAdminKey)}
	if devices != nil {
		options = append(options, WithDevices(devices))
	}
	return NewServer(nil, options...).Handler()
}

func apiRequest(t *testing.T, handler http.Handler, method, target, body, credential string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	httpRequest := httptest.NewRequest(method, target, strings.NewReader(body))
	httpRequest.Header.Set("Content-Type", "application/json")
	if credential != "" {
		httpRequest.Header.Set("X-Api-Key", credential)
	}
	handler.ServeHTTP(recorder, httpRequest)
	return recorder
}

func decodeBody[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var payload T
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %T: %v (body %s)", payload, err, recorder.Body)
	}
	return payload
}

func sampleUser(id int64, email string) *model.User {
	return &model.User{
		ID:        id,
		Email:     email,
		Name:      "User " + email,
		CreatedAt: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC),
	}
}

func TestUserRoutesReportNotConfigured(t *testing.T) {
	handler := NewServer(nil).Handler()

	tests := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/api/users"},
		{http.MethodPost, "/api/users"},
		{http.MethodPut, "/api/users/1"},
		{http.MethodDelete, "/api/users/1"},
		{http.MethodGet, "/api/users/1/devices"},
		{http.MethodPut, "/api/users/1/devices/2"},
		{http.MethodDelete, "/api/users/1/devices/2"},
		{http.MethodGet, "/api/users/1/tokens"},
		{http.MethodPost, "/api/users/1/tokens"},
		{http.MethodDelete, "/api/users/1/tokens/2"},
	}
	for _, test := range tests {
		recorder := apiRequest(t, handler, test.method, test.target, "", "")
		if recorder.Code != http.StatusNotImplemented {
			t.Errorf("%s %s: status = %d, want 501", test.method, test.target, recorder.Code)
		}
	}
}

func TestUserRoutesRequireAdmin(t *testing.T) {
	store := newFakeUsers(sampleUser(1, "alice@example.test"))
	// "*" would pass every scope check, yet account administration still
	// belongs to admins only.
	principal := auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeAll}, nil)
	handler := NewServer(nil, WithUsers(store), withTestToken(principal)).Handler()

	tests := []struct {
		method string
		target string
		body   string
	}{
		{http.MethodGet, "/api/users", ""},
		{http.MethodPost, "/api/users", `{"email":"bob@example.test"}`},
		{http.MethodPut, "/api/users/1", `{"name":"x"}`},
		{http.MethodDelete, "/api/users/1", ""},
		{http.MethodGet, "/api/users/1/devices", ""},
		{http.MethodGet, "/api/users/1/tokens", ""},
		{http.MethodPost, "/api/users/1/tokens", `{"scopes":["read:positions"]}`},
		{http.MethodDelete, "/api/users/1/tokens/1", ""},
	}
	for _, test := range tests {
		recorder := withToken(t, handler, test.method, test.target, test.body)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want 403 (body %s)",
				test.method, test.target, recorder.Code, recorder.Body)
		}
	}
	if len(store.users) != 1 || len(store.tokens) != 0 {
		t.Error("a non-admin request changed storage")
	}
}

func TestListAndCreateUser(t *testing.T) {
	store := newFakeUsers(sampleUser(1, "alice@example.test"))
	handler := adminUsersHandler(store, nil)

	recorder := apiRequest(t, handler, http.MethodGet, "/api/users?limit=10&offset=0", "", testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d (body %s)", recorder.Code, recorder.Body)
	}
	list := decodeBody[usersResponse](t, recorder)
	if list.Count != 1 || len(list.Users) != 1 || list.Users[0].Email != "alice@example.test" {
		t.Fatalf("list = %+v", list)
	}
	if store.lastList.Limit != 10 {
		t.Errorf("filter = %+v", store.lastList)
	}

	recorder = apiRequest(t, handler, http.MethodPost, "/api/users",
		`{"email":" bob@example.test ","name":" Bob ","admin":true}`, testAdminKey)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d (body %s)", recorder.Code, recorder.Body)
	}
	created := decodeBody[userResponse](t, recorder)
	if created.ID != 2 || created.Email != "bob@example.test" || created.Name != "Bob" || !created.Admin {
		t.Errorf("created = %+v", created)
	}

	recorder = apiRequest(t, handler, http.MethodPost, "/api/users",
		`{"email":"bob@example.test"}`, testAdminKey)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d (body %s)", recorder.Code, recorder.Body)
	}
}

func TestCreateUserRejectsBadInput(t *testing.T) {
	store := newFakeUsers()
	handler := adminUsersHandler(store, nil)

	for _, body := range []string{
		`{"email":""}`,
		`{"email":"not-an-email"}`,
		`{"email":"two words@example.test"}`,
		`{"email":"@example.test"}`,
		`{"email":"alice@"}`,
		`not json`,
	} {
		recorder := apiRequest(t, handler, http.MethodPost, "/api/users", body, testAdminKey)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", body, recorder.Code, recorder.Body)
		}
	}
	if len(store.users) != 0 {
		t.Error("invalid requests created a user")
	}
}

func TestUpdateUserIsPartial(t *testing.T) {
	store := newFakeUsers(sampleUser(1, "alice@example.test"))
	handler := adminUsersHandler(store, nil)

	recorder := apiRequest(t, handler, http.MethodPut, "/api/users/1", `{"name":" Alice "}`, testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}
	updated := decodeBody[userResponse](t, recorder)
	if updated.Name != "Alice" || updated.Admin || updated.Disabled {
		t.Errorf("updated = %+v", updated)
	}

	recorder = apiRequest(t, handler, http.MethodPut, "/api/users/1", `{"disabled":true}`, testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("disable status = %d (body %s)", recorder.Code, recorder.Body)
	}
	if !decodeBody[userResponse](t, recorder).Disabled {
		t.Error("user was not disabled")
	}
	if store.users[1].Name != "Alice" {
		t.Errorf("name was cleared: %+v", store.users[1])
	}

	if recorder := apiRequest(t, handler, http.MethodPut, "/api/users/99", `{"name":"x"}`, testAdminKey); recorder.Code != http.StatusNotFound {
		t.Errorf("unknown user status = %d, want 404", recorder.Code)
	}
	if recorder := apiRequest(t, handler, http.MethodPut, "/api/users/abc", `{"name":"x"}`, testAdminKey); recorder.Code != http.StatusBadRequest {
		t.Errorf("bad id status = %d, want 400", recorder.Code)
	}
}

func TestCannotLockYourselfOut(t *testing.T) {
	store := newFakeUsers(sampleUser(1, "admin@example.test"))
	store.users[1].Admin = true
	principal := auth.NewPrincipal(1, true, nil, nil)
	handler := NewServer(nil, WithUsers(store), withTestToken(principal)).Handler()

	for _, body := range []string{`{"disabled":true}`, `{"admin":false}`} {
		recorder := withToken(t, handler, http.MethodPut, "/api/users/1", body)
		if recorder.Code != http.StatusConflict {
			t.Errorf("%s: status = %d, want 409 (body %s)", body, recorder.Code, recorder.Body)
		}
	}
	if recorder := withToken(t, handler, http.MethodDelete, "/api/users/1", ""); recorder.Code != http.StatusConflict {
		t.Fatalf("self delete status = %d, want 409", recorder.Code)
	}
	if !store.users[1].Admin || store.users[1].Disabled {
		t.Errorf("self guard did not protect the account: %+v", store.users[1])
	}
}

func TestDeleteUser(t *testing.T) {
	store := newFakeUsers(sampleUser(1, "alice@example.test"), sampleUser(2, "bob@example.test"))
	handler := adminUsersHandler(store, nil)

	if recorder := apiRequest(t, handler, http.MethodDelete, "/api/users/2", "", testAdminKey); recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}
	if _, ok := store.users[2]; ok {
		t.Error("user was not deleted")
	}
	if recorder := apiRequest(t, handler, http.MethodDelete, "/api/users/2", "", testAdminKey); recorder.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404", recorder.Code)
	}
}

func TestIssueListAndRevokeToken(t *testing.T) {
	store := newFakeUsers(sampleUser(1, "alice@example.test"))
	handler := adminUsersHandler(store, nil)

	recorder := apiRequest(t, handler, http.MethodPost, "/api/users/1/tokens",
		`{"scopes":["read:positions","read:alerts"],"expiresIn":"720h"}`, testAdminKey)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("issue status = %d (body %s)", recorder.Code, recorder.Body)
	}
	issued := decodeBody[struct {
		tokenResponse
		Token string `json:"token"`
	}](t, recorder)
	if issued.Token != "trc_fake_plaintext" {
		t.Errorf("token = %q", issued.Token)
	}
	if strings.Join(issued.Scopes, ",") != "read:positions,read:alerts" {
		t.Errorf("scopes = %v", issued.Scopes)
	}
	if issued.ExpiresAt == nil {
		t.Error("expiry was not applied")
	}
	if strings.Contains(recorder.Body.String(), "tokenHash") {
		t.Errorf("body leaks the hash: %s", recorder.Body)
	}

	recorder = apiRequest(t, handler, http.MethodGet, "/api/users/1/tokens", "", testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d (body %s)", recorder.Code, recorder.Body)
	}
	list := decodeBody[tokensResponse](t, recorder)
	if list.Count != 1 || list.Tokens[0].ID != issued.ID {
		t.Fatalf("list = %+v", list)
	}
	if strings.Contains(recorder.Body.String(), "trc_fake_plaintext") {
		t.Error("list repeats the plaintext token")
	}

	if recorder := apiRequest(t, handler, http.MethodDelete, "/api/users/1/tokens/1", "", testAdminKey); recorder.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d (body %s)", recorder.Code, recorder.Body)
	}
	if len(store.tokens[1]) != 0 {
		t.Error("token was not revoked")
	}
	if recorder := apiRequest(t, handler, http.MethodDelete, "/api/users/1/tokens/1", "", testAdminKey); recorder.Code != http.StatusNotFound {
		t.Errorf("second revoke status = %d, want 404", recorder.Code)
	}
}

func TestIssueTokenRejectsBadInput(t *testing.T) {
	store := newFakeUsers(sampleUser(1, "alice@example.test"))
	handler := adminUsersHandler(store, nil)

	tests := []string{
		`{"scopes":["read:nothing"]}`,
		`{"expiresIn":"soon"}`,
		`{"expiresIn":"-1h"}`,
		`{"expiresIn":"0s"}`,
	}
	for _, body := range tests {
		recorder := apiRequest(t, handler, http.MethodPost, "/api/users/1/tokens", body, testAdminKey)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", body, recorder.Code, recorder.Body)
		}
	}
	if len(store.tokens[1]) != 0 {
		t.Error("invalid requests issued a token")
	}

	if recorder := apiRequest(t, handler, http.MethodPost, "/api/users/9/tokens", `{"scopes":[]}`, testAdminKey); recorder.Code != http.StatusNotFound {
		t.Errorf("unknown user status = %d, want 404", recorder.Code)
	}
}

func TestUserDeviceGrants(t *testing.T) {
	store := newFakeUsers(sampleUser(1, "alice@example.test"))
	devices := &fakeDeviceList{results: []model.Device{{
		ID: 7, UniqueID: "864717003283581", Name: "van", Protocol: "gt06",
		CreatedAt: time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC),
	}}}
	handler := adminUsersHandler(store, devices)

	recorder := apiRequest(t, handler, http.MethodGet, "/api/users/1/devices", "", testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}
	list := decodeBody[devicesResponse](t, recorder)
	if list.Count != 1 || list.Devices[0].ID != 7 {
		t.Fatalf("devices = %+v", list)
	}
	if filter := devices.lastFilter(t); len(filter.DeviceIDs) != 0 {
		t.Errorf("empty grant list asked for devices %v", filter.DeviceIDs)
	}

	if recorder := apiRequest(t, handler, http.MethodPut, "/api/users/1/devices/7", "", testAdminKey); recorder.Code != http.StatusNoContent {
		t.Fatalf("assign status = %d (body %s)", recorder.Code, recorder.Body)
	}
	if len(store.grants[1]) != 1 || store.grants[1][0] != 7 {
		t.Errorf("grants = %v", store.grants[1])
	}

	if recorder := apiRequest(t, handler, http.MethodDelete, "/api/users/1/devices/7", "", testAdminKey); recorder.Code != http.StatusNoContent {
		t.Fatalf("unassign status = %d (body %s)", recorder.Code, recorder.Body)
	}
	if len(store.grants[1]) != 0 {
		t.Errorf("grants = %v", store.grants[1])
	}

	if recorder := apiRequest(t, handler, http.MethodPut, "/api/users/1/devices/abc", "", testAdminKey); recorder.Code != http.StatusBadRequest {
		t.Errorf("bad device id status = %d, want 400", recorder.Code)
	}
	if recorder := apiRequest(t, handler, http.MethodPut, "/api/users/9/devices/7", "", testAdminKey); recorder.Code != http.StatusNotFound {
		t.Errorf("unknown user status = %d, want 404", recorder.Code)
	}
}

func TestAssignUnknownDevice(t *testing.T) {
	store := newFakeUsers(sampleUser(1, "alice@example.test"))
	devices := &fakeDeviceList{}
	handler := adminUsersHandler(store, devices)

	recorder := apiRequest(t, handler, http.MethodPut, "/api/users/1/devices/7", "", testAdminKey)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", recorder.Code, recorder.Body)
	}
	if len(store.grants[1]) != 0 {
		t.Errorf("grants = %v", store.grants[1])
	}
}

func TestUserStorageErrorsAreHidden(t *testing.T) {
	store := newFakeUsers(sampleUser(1, "alice@example.test"))
	store.err = errors.New(`pq: relation "users" does not exist`)
	var logged strings.Builder
	handler := NewServer(nil, WithUsers(store), WithAPIKey(testAdminKey),
		WithLogger(func(format string, args ...any) {
			logged.WriteString(format)
		})).Handler()

	recorder := apiRequest(t, handler, http.MethodGet, "/api/users", "", testAdminKey)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "relation") {
		t.Errorf("body leaks the storage error: %s", recorder.Body)
	}
	if logged.Len() == 0 {
		t.Error("storage error was not logged")
	}
}

type fakePinger struct {
	err   error
	calls int
}

func (f *fakePinger) Ping(context.Context) error {
	f.calls++
	return f.err
}

func TestHealthReportsDatabase(t *testing.T) {
	pinger := &fakePinger{}
	handler := NewServer(&fakeQuerier{}, WithHealthPinger(pinger)).Handler()

	recorder := get(t, handler, "/api/health")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	payload := decodeBody[map[string]string](t, recorder)
	if payload["status"] != "ok" || payload["database"] != "ok" {
		t.Errorf("payload = %v", payload)
	}
	if pinger.calls != 1 {
		t.Errorf("ping calls = %d", pinger.calls)
	}
}

func TestHealthFailsWhenDatabaseIsDown(t *testing.T) {
	pinger := &fakePinger{err: errors.New("dial tcp: connection refused")}
	var logged strings.Builder
	handler := NewServer(&fakeQuerier{}, WithHealthPinger(pinger),
		WithLogger(func(format string, args ...any) {
			logged.WriteString(format)
		})).Handler()

	recorder := get(t, handler, "/api/health")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	payload := decodeBody[map[string]string](t, recorder)
	if payload["status"] != "unavailable" || payload["database"] != "unavailable" {
		t.Errorf("payload = %v", payload)
	}
	if strings.Contains(recorder.Body.String(), "connection refused") {
		t.Errorf("body leaks the driver error: %s", recorder.Body)
	}
	if logged.Len() == 0 {
		t.Error("ping failure was not logged")
	}
}
