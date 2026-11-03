package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

// fakeNotifications is an in-memory NotificationStore.
type fakeNotifications struct {
	mu            sync.Mutex
	notifications map[int64]*model.Notification
	subscriptions map[int64]map[int64]struct{}
	nextID        int64
	err           error
}

func newFakeNotifications(notifications ...*model.Notification) *fakeNotifications {
	store := &fakeNotifications{
		notifications: make(map[int64]*model.Notification, len(notifications)),
		subscriptions: make(map[int64]map[int64]struct{}),
	}
	for _, notification := range notifications {
		copied := *notification
		copied.Notificators = append([]string(nil), notification.Notificators...)
		store.notifications[notification.ID] = &copied
		if notification.ID > store.nextID {
			store.nextID = notification.ID
		}
	}
	return store
}

func (f *fakeNotifications) fail() error {
	return f.err
}

func cloneNotification(notification model.Notification) model.Notification {
	copied := notification
	copied.Notificators = append([]string(nil), notification.Notificators...)
	return copied
}

func (f *fakeNotifications) Notifications(_ context.Context) ([]model.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return nil, err
	}
	result := make([]model.Notification, 0, len(f.notifications))
	for id := int64(1); id <= f.nextID; id++ {
		if notification, ok := f.notifications[id]; ok {
			result = append(result, cloneNotification(*notification))
		}
	}
	return result, nil
}

func (f *fakeNotifications) Notification(_ context.Context, notificationID int64) (*model.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return nil, err
	}
	notification, ok := f.notifications[notificationID]
	if !ok {
		return nil, model.ErrNotificationNotFound
	}
	copied := cloneNotification(*notification)
	return &copied, nil
}

func (f *fakeNotifications) CreateNotification(_ context.Context, create model.NotificationCreate) (*model.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return nil, err
	}
	f.nextID++
	notification := &model.Notification{
		ID:           f.nextID,
		Type:         create.Type,
		Notificators: append([]string(nil), create.Notificators...),
		Description:  create.Description,
		Always:       create.Always,
		CommandID:    create.CommandID,
		CalendarID:   create.CalendarID,
		Attributes:   create.Attributes,
		CreatedAt:    time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	}
	f.notifications[notification.ID] = notification
	copied := cloneNotification(*notification)
	return &copied, nil
}

func (f *fakeNotifications) UpdateNotification(_ context.Context, notificationID int64, update model.NotificationUpdate) (*model.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return nil, err
	}
	notification, ok := f.notifications[notificationID]
	if !ok {
		return nil, model.ErrNotificationNotFound
	}
	if update.Type != nil {
		notification.Type = *update.Type
	}
	if update.Notificators != nil {
		notification.Notificators = append([]string(nil), (*update.Notificators)...)
	}
	if update.Description != nil {
		notification.Description = *update.Description
	}
	if update.Always != nil {
		notification.Always = *update.Always
	}
	if update.CommandID != nil {
		notification.CommandID = *update.CommandID
	}
	if update.CalendarID != nil {
		notification.CalendarID = *update.CalendarID
	}
	if update.Attributes != nil {
		notification.Attributes = *update.Attributes
	}
	copied := cloneNotification(*notification)
	return &copied, nil
}

func (f *fakeNotifications) DeleteNotification(_ context.Context, notificationID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return err
	}
	if _, ok := f.notifications[notificationID]; !ok {
		return model.ErrNotificationNotFound
	}
	delete(f.notifications, notificationID)
	return nil
}

func (f *fakeNotifications) UserNotifications(_ context.Context, userID int64) ([]model.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return nil, err
	}
	ids := f.subscriptions[userID]
	result := make([]model.Notification, 0, len(ids))
	for id := int64(1); id <= f.nextID; id++ {
		if _, ok := ids[id]; !ok {
			continue
		}
		if notification, ok := f.notifications[id]; ok {
			result = append(result, cloneNotification(*notification))
		}
	}
	return result, nil
}

func (f *fakeNotifications) AssignNotification(_ context.Context, userID, notificationID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return err
	}
	if f.subscriptions[userID] == nil {
		f.subscriptions[userID] = make(map[int64]struct{})
	}
	f.subscriptions[userID][notificationID] = struct{}{}
	return nil
}

func (f *fakeNotifications) UnassignNotification(_ context.Context, userID, notificationID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return err
	}
	delete(f.subscriptions[userID], notificationID)
	return nil
}

func notificationHandler(users UserStore, notifications NotificationStore) http.Handler {
	options := []Option{WithAPIKey(testAdminKey)}
	if users != nil {
		options = append(options, WithUsers(users))
	}
	if notifications != nil {
		options = append(options, WithNotifications(notifications))
	}
	return NewServer(nil, options...).Handler()
}

func sampleNotification(id int64, notificationType string, notificators ...string) *model.Notification {
	return &model.Notification{
		ID:           id,
		Type:         notificationType,
		Notificators: notificators,
		Description:  "preset " + notificationType,
		CreatedAt:    time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC),
	}
}

func TestNotificationRoutesReportNotConfigured(t *testing.T) {
	handler := NewServer(nil).Handler()

	tests := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/api/notifications"},
		{http.MethodPost, "/api/notifications"},
		{http.MethodPut, "/api/notifications/1"},
		{http.MethodDelete, "/api/notifications/1"},
		{http.MethodGet, "/api/users/1/notifications"},
		{http.MethodPut, "/api/users/1/notifications/2"},
		{http.MethodDelete, "/api/users/1/notifications/2"},
	}
	for _, test := range tests {
		recorder := apiRequest(t, handler, test.method, test.target, "", "")
		if recorder.Code != http.StatusNotImplemented {
			t.Errorf("%s %s: status = %d, want 501", test.method, test.target, recorder.Code)
		}
	}
}

func TestNotificationPresetRequiresAdminButListIsOpen(t *testing.T) {
	store := newFakeNotifications(sampleNotification(1, model.EventDeviceOverspeed, "mail"))
	principal := auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeAll}, nil)
	handler := NewServer(nil,
		WithUsers(newFakeUsers(sampleUser(1, "alice@example.test"))),
		WithNotifications(store),
		withTestToken(principal),
	).Handler()

	if recorder := withToken(t, handler, http.MethodGet, "/api/notifications", ""); recorder.Code != http.StatusOK {
		t.Errorf("list status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	tests := []struct {
		method string
		target string
		body   string
	}{
		{http.MethodPost, "/api/notifications", `{"type":"powerCut","notificators":["mail"]}`},
		{http.MethodPut, "/api/notifications/1", `{"description":"x"}`},
		{http.MethodDelete, "/api/notifications/1", ""},
	}
	for _, test := range tests {
		recorder := withToken(t, handler, test.method, test.target, test.body)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want 403 (body %s)",
				test.method, test.target, recorder.Code, recorder.Body)
		}
	}
	if _, ok := store.notifications[1]; !ok || len(store.notifications) != 1 {
		t.Error("a non-admin request changed the presets")
	}
}

func TestNotificationPresetCRUD(t *testing.T) {
	store := newFakeNotifications()
	handler := notificationHandler(newFakeUsers(sampleUser(1, "alice@example.test")), store)

	recorder := apiRequest(t, handler, http.MethodPost, "/api/notifications",
		`{"type":" deviceOverspeed ","notificators":["email","webhook","mail"],"description":" fast "}`,
		testAdminKey)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d (body %s)", recorder.Code, recorder.Body)
	}
	created := decodeBody[notificationResponse](t, recorder)
	if created.Type != model.EventDeviceOverspeed || created.Description != "fast" {
		t.Errorf("created = %+v", created)
	}
	if len(created.Notificators) != 2 || created.Notificators[0] != "mail" || created.Notificators[1] != "webhook" {
		t.Errorf("notificators = %v", created.Notificators)
	}

	recorder = apiRequest(t, handler, http.MethodGet, "/api/notifications", "", testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d", recorder.Code)
	}
	list := decodeBody[[]notificationResponse](t, recorder)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}

	recorder = apiRequest(t, handler, http.MethodPut, "/api/notifications/1",
		`{"notificators":["mail"]}`, testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("update status = %d (body %s)", recorder.Code, recorder.Body)
	}
	updated := decodeBody[notificationResponse](t, recorder)
	if updated.Type != model.EventDeviceOverspeed || updated.Description != "fast" ||
		len(updated.Notificators) != 1 || updated.Notificators[0] != "mail" {
		t.Errorf("updated = %+v", updated)
	}

	recorder = apiRequest(t, handler, http.MethodDelete, "/api/notifications/1", "", testAdminKey)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d (body %s)", recorder.Code, recorder.Body)
	}
	if recorder = apiRequest(t, handler, http.MethodDelete, "/api/notifications/1", "", testAdminKey); recorder.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404", recorder.Code)
	}
}

func TestNotificationValidation(t *testing.T) {
	handler := notificationHandler(newFakeUsers(), newFakeNotifications())

	for _, body := range []string{
		`{"type":"none","notificators":["mail"]}`,
		`{"type":"deviceOverspeed","notificators":[]}`,
		`{"type":"deviceOverspeed","notificators":["carrier-pigeon"]}`,
		`{"type":"","notificators":["mail"]}`,
	} {
		recorder := apiRequest(t, handler, http.MethodPost, "/api/notifications", body, testAdminKey)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400 (body %s)", body, recorder.Code, recorder.Body)
		}
	}
}

func TestUserNotificationSubscriptionsSelfService(t *testing.T) {
	users := newFakeUsers(
		sampleUser(1, "alice@example.test"),
		sampleUser(2, "bob@example.test"),
	)
	notifications := newFakeNotifications(sampleNotification(5, model.EventPowerCut, "mail"))
	handler := NewServer(nil,
		WithUsers(users), WithNotifications(notifications),
		withTestToken(auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeReadAlerts}, nil)),
	).Handler()

	if recorder := withToken(t, handler, http.MethodPut, "/api/users/1/notifications/5", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("subscribe status = %d (body %s)", recorder.Code, recorder.Body)
	}

	recorder := withToken(t, handler, http.MethodGet, "/api/users/1/notifications", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d (body %s)", recorder.Code, recorder.Body)
	}
	list := decodeBody[userNotificationsResponse](t, recorder)
	if list.UserID != 1 || list.Count != 1 || list.Notifications[0].ID != 5 {
		t.Errorf("subscriptions = %+v", list)
	}

	// The same token may not touch another account's subscriptions.
	if recorder := withToken(t, handler, http.MethodPut, "/api/users/2/notifications/5", ""); recorder.Code != http.StatusForbidden {
		t.Errorf("other user subscribe status = %d, want 403", recorder.Code)
	}

	if recorder := withToken(t, handler, http.MethodDelete, "/api/users/1/notifications/5", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("unsubscribe status = %d (body %s)", recorder.Code, recorder.Body)
	}
	recorder = withToken(t, handler, http.MethodGet, "/api/users/1/notifications", "")
	list = decodeBody[userNotificationsResponse](t, recorder)
	if list.Count != 0 {
		t.Errorf("subscriptions after delete = %+v", list)
	}
}

func TestUserNotificationSubscriptionUnknownIDs(t *testing.T) {
	users := newFakeUsers(sampleUser(1, "alice@example.test"))
	notifications := newFakeNotifications(sampleNotification(5, model.EventPowerCut, "mail"))
	handler := notificationHandler(users, notifications)

	if recorder := apiRequest(t, handler, http.MethodPut, "/api/users/1/notifications/99", "", testAdminKey); recorder.Code != http.StatusNotFound {
		t.Errorf("unknown notification status = %d, want 404", recorder.Code)
	}
	if recorder := apiRequest(t, handler, http.MethodPut, "/api/users/99/notifications/5", "", testAdminKey); recorder.Code != http.StatusNotFound {
		t.Errorf("unknown user status = %d, want 404", recorder.Code)
	}
}

func TestUserNotificationListingAsAdmin(t *testing.T) {
	users := newFakeUsers(sampleUser(2, "bob@example.test"))
	notifications := newFakeNotifications(sampleNotification(5, model.EventPowerCut, "mail"))
	handler := notificationHandler(users, notifications)

	if recorder := apiRequest(t, handler, http.MethodPut, "/api/users/2/notifications/5", "", testAdminKey); recorder.Code != http.StatusNoContent {
		t.Fatalf("subscribe status = %d (body %s)", recorder.Code, recorder.Body)
	}
	recorder := apiRequest(t, handler, http.MethodGet, "/api/users/2/notifications", "", testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d (body %s)", recorder.Code, recorder.Body)
	}
	var payload userNotificationsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Count != 1 || payload.Notifications[0].ID != 5 {
		t.Errorf("payload = %+v", payload)
	}
}

// TestNotificationRulesRoundTrip pins the M4-3 fields: always, the alarm and
// geofence filters in attributes, the calendar and the saved command id.
func TestNotificationRulesRoundTrip(t *testing.T) {
	handler := notificationHandler(newFakeUsers(), newFakeNotifications())

	created := apiRequest(t, handler, http.MethodPost, "/api/notifications",
		`{"type":"alarm","notificators":["mail"],"always":true,"commandId":3,"calendarId":4,
		  "attributes":{"alarms":"sos,tow","priority":true}}`, testAdminKey)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d (body %s)", created.Code, created.Body)
	}
	var notification notificationResponse
	if err := json.Unmarshal(created.Body.Bytes(), &notification); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !notification.Always || notification.CommandID != 3 || notification.CalendarID != 4 {
		t.Fatalf("notification = %+v", notification)
	}
	if notification.Attributes["alarms"] != "sos,tow" || notification.Attributes["priority"] != true {
		t.Errorf("attributes = %v", notification.Attributes)
	}

	updated := apiRequest(t, handler, http.MethodPut, "/api/notifications/1",
		`{"always":false,"attributes":{"alarms":"sos"}}`, testAdminKey)
	if updated.Code != http.StatusOK {
		t.Fatalf("update = %d (body %s)", updated.Code, updated.Body)
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &notification); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if notification.Always || notification.Attributes["alarms"] != "sos" {
		t.Errorf("notification = %+v", notification)
	}
	if notification.CommandID != 3 || notification.CalendarID != 4 {
		t.Errorf("partial update cleared a field it did not send: %+v", notification)
	}
}
