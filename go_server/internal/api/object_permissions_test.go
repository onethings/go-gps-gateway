package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

// fakePermissions is an in-memory permission store. Groups 1 -> 2 -> 3 form a
// three level tree; device 7 sits in group 2 and device 8 has no group.
type fakePermissions struct {
	mu           sync.Mutex
	pairs        []permissions.Permission
	parents      map[int64]int64
	deviceGroups map[int64]int64
	groups       map[int64]string
	err          error
}

func newFakePermissions() *fakePermissions {
	return &fakePermissions{
		parents:      map[int64]int64{1: 0, 2: 1, 3: 2},
		deviceGroups: map[int64]int64{7: 2, 8: 0},
		groups:       map[int64]string{1: "Depot", 2: "North", 3: "North Yard"},
	}
}

func (f *fakePermissions) UserGrants(_ context.Context, userID int64) ([]int64, []int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, nil, f.err
	}
	var devices, groups []int64
	for _, pair := range f.pairs {
		if pair.Owner != permissions.ObjectUser || pair.OwnerID != userID {
			continue
		}
		switch pair.Property {
		case permissions.ObjectDevice:
			devices = append(devices, pair.PropertyID)
		case permissions.ObjectGroup:
			groups = append(groups, pair.PropertyID)
		}
	}
	return devices, groups, nil
}

func (f *fakePermissions) GroupParents(context.Context) (map[int64]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := make(map[int64]int64, len(f.parents))
	for id, parent := range f.parents {
		copied[id] = parent
	}
	return copied, nil
}

func (f *fakePermissions) DevicesInGroups(_ context.Context, groupIDs []int64) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	wanted := make(map[int64]struct{}, len(groupIDs))
	for _, id := range groupIDs {
		wanted[id] = struct{}{}
	}
	var devices []int64
	for deviceID, groupID := range f.deviceGroups {
		if _, ok := wanted[groupID]; ok {
			devices = append(devices, deviceID)
		}
	}
	return devices, nil
}

func (f *fakePermissions) DeviceGroup(_ context.Context, deviceID int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deviceGroups[deviceID], nil
}

func (f *fakePermissions) Permissions(_ context.Context, owner permissions.Object, ownerID int64, property permissions.Object, propertyID int64) ([]permissions.Permission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var matched []permissions.Permission
	for _, pair := range f.pairs {
		if pair.Matches(owner, ownerID, property, propertyID) {
			matched = append(matched, pair)
		}
	}
	return matched, nil
}

func (f *fakePermissions) AddPermission(_ context.Context, permission permissions.Permission) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	for _, pair := range f.pairs {
		if pair == permission {
			return nil
		}
	}
	f.pairs = append(f.pairs, permission)
	return nil
}

func (f *fakePermissions) RemovePermission(_ context.Context, permission permissions.Permission) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	kept := make([]permissions.Permission, 0, len(f.pairs))
	for _, pair := range f.pairs {
		if pair != permission {
			kept = append(kept, pair)
		}
	}
	f.pairs = kept
	return nil
}

func (f *fakePermissions) Groups(_ context.Context, filter model.ResourceFilter) ([]*model.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	groups := make([]*model.Group, 0, len(f.groups))
	for id, name := range f.groups {
		if !containsID(filter.IDs, id) {
			continue
		}
		groups = append(groups, &model.Group{ID: id, Name: name})
	}
	return groups, nil
}

func containsID(ids []int64, id int64) bool {
	if len(ids) == 0 {
		return true
	}
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func (f *fakePermissions) add(t *testing.T, owner permissions.Object, ownerID int64, property permissions.Object, propertyID int64) {
	t.Helper()
	pair, err := permissions.New(owner, ownerID, property, propertyID)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pairs = append(f.pairs, pair)
}

func (f *fakePermissions) snapshot() []permissions.Permission {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]permissions.Permission(nil), f.pairs...)
}

func withForm(t *testing.T, handler http.Handler, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer "+testToken)
	handler.ServeHTTP(recorder, request)
	return recorder
}

// permissionServer builds a server with permissions, one account and one device.
func permissionServer(t *testing.T, principal *auth.Principal, store *fakePermissions, options ...Option) http.Handler {
	t.Helper()
	devices := &fakeDeviceList{results: []model.Device{{ID: 7, UniqueID: "862000000000007", Name: "van"}}}
	users := newFakeUsers(&model.User{ID: 2, Email: "owner@example.test", Name: "owner"})
	base := []Option{
		WithPermissions(store),
		WithDevices(devices),
		WithUsers(users),
		withTestToken(principal),
	}
	return NewServer(nil, append(base, options...)...).Handler()
}

func TestObjectPermissionsRouteNeedsStore(t *testing.T) {
	handler := NewServer(nil,
		withTestToken(auth.NewPrincipal(2, true, []auth.Scope{auth.ScopeAll}, nil)),
	).Handler()

	recorder := withToken(t, handler, http.MethodGet, "/api/permissions?userId=2", "")
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestObjectPermissionsListRequiresScope(t *testing.T) {
	store := newFakePermissions()
	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadDevices}, nil)
	handler := permissionServer(t, principal, store)

	recorder := withToken(t, handler, http.MethodGet, "/api/permissions?userId=2", "")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestObjectPermissionsListReturnsBareArray(t *testing.T) {
	store := newFakePermissions()
	store.add(t, permissions.ObjectUser, 2, permissions.ObjectDevice, 7)

	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadPermissions}, []int64{7})
	handler := permissionServer(t, principal, store)

	recorder := withToken(t, handler, http.MethodGet, "/api/permissions?userId=2&deviceId=7", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if body := strings.TrimSpace(recorder.Body.String()); body != `[{"userId":2,"deviceId":7}]` {
		t.Errorf("body = %s, want the bare array traccar-web reads", body)
	}

	// A pair that does not exist is an empty array, not an error.
	adminHandler := permissionServer(t, auth.NewPrincipal(1, true, []auth.Scope{auth.ScopeAll}, nil), store)
	recorder = withToken(t, adminHandler, http.MethodGet, "/api/permissions?userId=99&deviceId=99", "")
	if body := strings.TrimSpace(recorder.Body.String()); body != "[]" {
		t.Errorf("empty answer = %s, want []", body)
	}

	// Asking about a device the caller cannot reach is still 403: existence is
	// not leaked to somebody who has no grant at all.
	recorder = withToken(t, handler, http.MethodGet, "/api/permissions?userId=2&deviceId=8", "")
	if recorder.Code != http.StatusForbidden {
		t.Errorf("unreachable device status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestObjectPermissionsListWildcardIsAdminOnly(t *testing.T) {
	store := newFakePermissions()
	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadPermissions}, []int64{7})
	handler := permissionServer(t, principal, store)

	// ?userId=3 would list another account's grants.
	recorder := withToken(t, handler, http.MethodGet, "/api/permissions?userId=3", "")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}

	// Listing one's own grants with a wildcard is fine.
	recorder = withToken(t, handler, http.MethodGet, "/api/permissions?userId=2", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("own grants: status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestObjectPermissionsListRejectsBadQuery(t *testing.T) {
	store := newFakePermissions()
	handler := permissionServer(t, auth.NewPrincipal(2, true, []auth.Scope{auth.ScopeAll}, nil), store)

	for _, target := range []string{
		"/api/permissions",
		"/api/permissions?userId=2&deviceId=7&groupId=1",
		"/api/permissions?userId=zero",
		"/api/permissions?planetsId=1",
		"/api/permissions?deviceId=7&deviceId=8",
	} {
		recorder := withToken(t, handler, http.MethodGet, target, "")
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", target, recorder.Code, recorder.Body)
		}
	}
}

func TestObjectPermissionsGrantAndRevoke(t *testing.T) {
	store := newFakePermissions()
	handler := permissionServer(t, auth.NewPrincipal(1, true, []auth.Scope{auth.ScopeAll}, nil), store)

	recorder := withToken(t, handler, http.MethodPost, "/api/permissions", `{"userId":2,"deviceId":7}`)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("post status = %d, want 204 (body %s)", recorder.Code, recorder.Body)
	}
	if got := store.snapshot(); len(got) != 1 || got[0].PropertyID != 7 {
		t.Fatalf("stored = %v, want one user 2 -> device 7", got)
	}

	// Repeating the grant is a no-op.
	recorder = withToken(t, handler, http.MethodPost, "/api/permissions", `{"userId":2,"deviceId":7}`)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("second post status = %d", recorder.Code)
	}
	if got := store.snapshot(); len(got) != 1 {
		t.Fatalf("stored = %v, want one pair", got)
	}

	recorder = withToken(t, handler, http.MethodDelete, "/api/permissions", `{"userId":2,"deviceId":7}`)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 (body %s)", recorder.Code, recorder.Body)
	}
	if got := store.snapshot(); len(got) != 0 {
		t.Fatalf("stored = %v, want none", got)
	}
}

func TestObjectPermissionsBulk(t *testing.T) {
	store := newFakePermissions()
	handler := permissionServer(t, auth.NewPrincipal(1, true, []auth.Scope{auth.ScopeAll}, nil), store)

	body := `[{"userId":2,"deviceId":7},{"userId":2,"deviceId":8}]`
	recorder := withToken(t, handler, http.MethodPost, "/api/permissions", body)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", recorder.Code, recorder.Body)
	}
	if got := store.snapshot(); len(got) != 2 {
		t.Fatalf("stored = %v, want two pairs", got)
	}

	// Mixing shapes would make a partial failure impossible to describe.
	recorder = withToken(t, handler, http.MethodPost, "/api/permissions",
		`[{"userId":2,"deviceId":7},{"userId":2,"groupId":1}]`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("mixed bulk status = %d, want 400 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestObjectPermissionsRejectMalformedBodies(t *testing.T) {
	store := newFakePermissions()
	handler := permissionServer(t, auth.NewPrincipal(1, true, []auth.Scope{auth.ScopeAll}, nil), store)

	for _, body := range []string{
		``,
		`{`,
		`{}`,
		`{"userId":1}`,
		`{"userId":0,"deviceId":7}`,
		`{"geofenceId":1,"deviceId":7}`,
		`[]`,
		`{"userId":1,"deviceId":7} {"userId":1,"deviceId":8}`,
	} {
		recorder := withToken(t, handler, http.MethodPost, "/api/permissions", body)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400 (body %s)", body, recorder.Code, recorder.Body)
		}
	}
	if got := store.snapshot(); len(got) != 0 {
		t.Errorf("stored = %v, want nothing", got)
	}
}

func TestObjectPermissionsNonAdminCannotGrantWhatItCannotSee(t *testing.T) {
	store := newFakePermissions()
	// User 2 was granted group 2, so device 7 (in group 2) is reachable and
	// device 8 (no group, no grant) is not.
	store.add(t, permissions.ObjectUser, 2, permissions.ObjectGroup, 2)

	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeWritePermissions}, []int64{7})
	handler := permissionServer(t, principal, store)

	recorder := withToken(t, handler, http.MethodPost, "/api/permissions",
		`{"userId":2,"deviceId":8}`)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}

	recorder = withToken(t, handler, http.MethodPost, "/api/permissions",
		`{"userId":2,"deviceId":7}`)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("reachable device status = %d, want 204 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestObjectPermissionsRejectUnsupportedMethod(t *testing.T) {
	store := newFakePermissions()
	handler := permissionServer(t, auth.NewPrincipal(1, true, []auth.Scope{auth.ScopeAll}, nil), store)

	recorder := withToken(t, handler, http.MethodPut, "/api/permissions", `{}`)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 (body %s)", recorder.Code, recorder.Body)
	}
	if allow := recorder.Header().Get("Allow"); allow == "" {
		t.Error("405 without an Allow header")
	}
}

func TestObjectPermissionsStoreErrorIsInternal(t *testing.T) {
	store := newFakePermissions()
	store.err = errors.New("boom")
	handler := permissionServer(t, auth.NewPrincipal(1, true, []auth.Scope{auth.ScopeAll}, nil), store)

	recorder := withToken(t, handler, http.MethodGet, "/api/permissions?userId=2", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", recorder.Code, recorder.Body)
	}
}

func shareServer(t *testing.T, principal *auth.Principal, store *fakePermissions, options ...Option) http.Handler {
	t.Helper()
	users := newFakeUsers(&model.User{ID: 2, Email: "owner@example.test", Name: "owner"})
	return shareServerWithUsers(t, principal, store, users, options...)
}

// shareServerWithUsers is shareServer with a caller supplied account store, so a
// test can inspect the share account that was created.
func shareServerWithUsers(
	t *testing.T, principal *auth.Principal, store *fakePermissions, users *fakeUsers, options ...Option,
) http.Handler {
	t.Helper()
	devices := &fakeDeviceList{results: []model.Device{
		{ID: 7, UniqueID: "862000000000007", Name: "van"},
	}}
	base := []Option{
		WithPermissions(store),
		WithDevices(devices),
		WithUsers(users),
		withTestToken(principal),
	}
	return NewServer(nil, append(base, options...)...).Handler()
}

func sharePrincipal() *auth.Principal {
	return auth.NewPrincipal(2, false,
		[]auth.Scope{auth.ScopeWriteShare, auth.ScopeReadPositions}, []int64{7})
}

func TestShareCreatesTemporaryAccountAndToken(t *testing.T) {
	store := newFakePermissions()
	store.add(t, permissions.ObjectUser, 2, permissions.ObjectDevice, 7)
	handler := shareServer(t, sharePrincipal(), store)

	recorder := withForm(t, handler, "/api/share/device", url.Values{"deviceId": {"7"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	var response shareResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if !strings.HasPrefix(response.Token, "trc_") {
		t.Errorf("token = %q, want a bearer token", response.Token)
	}
	if response.UserID == 0 || response.UserID == 2 {
		t.Errorf("userId = %d, want a new share account", response.UserID)
	}

	pairs := store.snapshot()
	if len(pairs) != 2 {
		t.Fatalf("stored = %v, want the original grant plus the share grant", pairs)
	}
	pairs = pairs[1:]
	if pairs[0].Property != permissions.ObjectDevice || pairs[0].PropertyID != 7 {
		t.Errorf("grant = %s, want device 7", pairs[0])
	}
	if pairs[0].OwnerID != response.UserID {
		t.Errorf("grant owner = %d, want the share account %d", pairs[0].OwnerID, response.UserID)
	}
}

func TestShareRecordsTheExpirationOnTheAccount(t *testing.T) {
	store := newFakePermissions()
	store.add(t, permissions.ObjectUser, 2, permissions.ObjectDevice, 7)
	users := newFakeUsers(&model.User{ID: 2, Email: "owner@example.test", Name: "owner"})
	handler := shareServerWithUsers(t, sharePrincipal(), store, users)

	expiration := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	recorder := withForm(t, handler, "/api/share/device", url.Values{
		"deviceId":   {"7"},
		"expiration": {expiration.Format(time.RFC3339)},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	var response shareResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}

	account, err := users.User(context.Background(), response.UserID)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if !account.Temporary {
		t.Error("the share account is not temporary")
	}
	if got := account.Attributes["expirationTime"]; got != expiration.Format(time.RFC3339) {
		// The scheduled cleanup matches on this key, so a share without it is
		// never deleted.
		t.Errorf("expirationTime = %v, want %s", got, expiration.Format(time.RFC3339))
	}
}

func TestShareWithoutExpirationLeavesNoExpirationTime(t *testing.T) {
	store := newFakePermissions()
	store.add(t, permissions.ObjectUser, 2, permissions.ObjectDevice, 7)
	users := newFakeUsers(&model.User{ID: 2, Email: "owner@example.test", Name: "owner"})
	handler := shareServerWithUsers(t, sharePrincipal(), store, users)

	recorder := withForm(t, handler, "/api/share/device", url.Values{"deviceId": {"7"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	var response shareResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	account, err := users.User(context.Background(), response.UserID)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if _, ok := account.Attributes["expirationTime"]; ok {
		t.Errorf("attributes = %v, want no expirationTime", account.Attributes)
	}
}

func TestShareSecondTimeIsConflict(t *testing.T) {
	store := newFakePermissions()
	store.add(t, permissions.ObjectUser, 2, permissions.ObjectDevice, 7)
	handler := shareServer(t, sharePrincipal(), store)

	if recorder := withForm(t, handler, "/api/share/device", url.Values{"deviceId": {"7"}}); recorder.Code != http.StatusOK {
		t.Fatalf("first share status = %d (body %s)", recorder.Code, recorder.Body)
	}
	recorder := withForm(t, handler, "/api/share/device", url.Values{"deviceId": {"7"}})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("second share status = %d, want 409 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestShareGroupGrantsTheGroup(t *testing.T) {
	store := newFakePermissions()
	store.add(t, permissions.ObjectUser, 2, permissions.ObjectGroup, 2)

	handler := shareServer(t, sharePrincipal(), store)
	recorder := withForm(t, handler, "/api/share/group", url.Values{"groupId": {"2"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	pairs := store.snapshot()
	if len(pairs) != 2 {
		t.Fatalf("stored = %v, want the original grant plus the share grant", pairs)
	}
	shared := false
	for _, pair := range pairs {
		if pair.Owner != permissions.ObjectUser || pair.OwnerID == 2 {
			continue
		}
		if pair.Property == permissions.ObjectGroup && pair.PropertyID == 2 {
			shared = true
		}
	}
	if !shared {
		t.Fatalf("stored = %v, want a user -> group 2 grant for the share account", pairs)
	}
}

func TestShareNeedsAccess(t *testing.T) {
	store := newFakePermissions()
	store.add(t, permissions.ObjectUser, 2, permissions.ObjectDevice, 7)
	handler := shareServer(t, sharePrincipal(), store)

	// The caller was granted device 7 only; device 8 is somebody else's. The
	// devices fake reports device 7 for any filter, so 8 resolves to a row and
	// the grant check is what refuses it.
	recorder := withForm(t, handler, "/api/share/device", url.Values{"deviceId": {"8"}})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestShareRejectsBadRequests(t *testing.T) {
	store := newFakePermissions()
	handler := shareServer(t, sharePrincipal(), store)

	cases := []struct {
		target string
		form   url.Values
		status int
	}{
		{"/api/share/device", url.Values{}, http.StatusBadRequest},
		{"/api/share/device", url.Values{"deviceId": {"zero"}}, http.StatusBadRequest},
		{"/api/share/device", url.Values{"deviceId": {"7"}, "expiration": {"yesterday"}}, http.StatusBadRequest},
		{"/api/share/device", url.Values{"deviceId": {"7"}, "expiration": {"2020-01-01"}}, http.StatusBadRequest},
		{"/api/share/group", url.Values{"groupId": {"99"}}, http.StatusNotFound},
	}
	for _, testCase := range cases {
		recorder := withForm(t, handler, testCase.target, testCase.form)
		if recorder.Code != testCase.status {
			t.Errorf("%s %v: status = %d, want %d (body %s)",
				testCase.target, testCase.form, recorder.Code, testCase.status, recorder.Body)
		}
	}
}

func TestShareUnknownDeviceIsNotFound(t *testing.T) {
	store := newFakePermissions()
	store.add(t, permissions.ObjectUser, 2, permissions.ObjectDevice, 7)
	handler := NewServer(nil,
		WithPermissions(store),
		// An empty device list makes every lookup miss.
		WithDevices(&fakeDeviceList{}),
		WithUsers(newFakeUsers(&model.User{ID: 2, Email: "owner@example.test"})),
		withTestToken(sharePrincipal()),
	).Handler()

	recorder := withForm(t, handler, "/api/share/device", url.Values{"deviceId": {"7"}})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestShareWithoutScopeIsForbidden(t *testing.T) {
	store := newFakePermissions()
	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadDevices}, []int64{7})
	handler := shareServer(t, principal, store)

	recorder := withForm(t, handler, "/api/share/device", url.Values{"deviceId": {"7"}})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestShareCanBeDisabled(t *testing.T) {
	store := newFakePermissions()
	handler := shareServer(t, sharePrincipal(), store, WithShareDisabled(true))

	recorder := withForm(t, handler, "/api/share/device", url.Values{"deviceId": {"7"}})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
	if got := store.snapshot(); len(got) != 0 {
		t.Errorf("stored = %v, want nothing", got)
	}
}

func TestShareWithLegacyKeyIsForbidden(t *testing.T) {
	store := newFakePermissions()
	handler := NewServer(nil,
		WithPermissions(store),
		WithDevices(&fakeDeviceList{}),
		WithUsers(newFakeUsers()),
		WithAPIKey("secret"),
	).Handler()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/share/device",
		strings.NewReader(url.Values{"deviceId": {"7"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Api-Key", "secret")
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestShareExpirationIsHonoured(t *testing.T) {
	store := newFakePermissions()
	store.add(t, permissions.ObjectUser, 2, permissions.ObjectDevice, 7)
	handler := shareServer(t, sharePrincipal(), store)

	expiration := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	recorder := withForm(t, handler, "/api/share/device", url.Values{
		"deviceId":   {"7"},
		"expiration": {expiration.Format(time.RFC3339)},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	var response shareResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if response.ExpiresAt == nil || !response.ExpiresAt.Equal(expiration) {
		t.Fatalf("expiresAt = %v, want %v", response.ExpiresAt, expiration)
	}
}
