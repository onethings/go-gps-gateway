package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

// TestEventsAndAlertsReturnTheSameRows covers the compatibility promise: the new
// route answers with the events vocabulary, the old one with the alert
// vocabulary, and both read the same query so no client sees different data.
func TestEventsAndAlertsReturnTheSameRows(t *testing.T) {
	eventTime := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	querier := &fakeQuerier{alerts: []model.Event{{
		ID:         9,
		DeviceID:   7,
		PositionID: 42,
		Type:       model.EventDeviceOverspeed,
		Time:       eventTime,
		GeofenceID: 5,
		Attributes: map[string]any{"speed": 41.5},
	}}}
	handler := NewServer(querier).Handler()

	eventsRecorder := get(t, handler, "/api/events?deviceId=7")
	if eventsRecorder.Code != 200 {
		t.Fatalf("GET /api/events = %d, want 200 (body %s)", eventsRecorder.Code, eventsRecorder.Body)
	}

	var events eventsResponse
	if err := json.Unmarshal(eventsRecorder.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode events: %v (body %s)", err, eventsRecorder.Body)
	}
	if events.Count != 1 || len(events.Events) != 1 {
		t.Fatalf("events response = %+v", events)
	}
	event := events.Events[0]
	if event.ID != 9 || event.DeviceID != 7 || event.PositionID != 42 {
		t.Errorf("event = %+v", event)
	}
	if event.Type != model.EventDeviceOverspeed {
		t.Errorf("type = %q", event.Type)
	}
	if !event.EventTime.Equal(eventTime) {
		t.Errorf("eventTime = %v, want %v", event.EventTime, eventTime)
	}
	if event.GeofenceID != 5 {
		t.Errorf("geofenceId = %d", event.GeofenceID)
	}

	alertsRecorder := get(t, handler, "/api/alerts?deviceId=7")
	if alertsRecorder.Code != 200 {
		t.Fatalf("GET /api/alerts = %d, want 200", alertsRecorder.Code)
	}
	var alerts alertsResponse
	if err := json.Unmarshal(alertsRecorder.Body.Bytes(), &alerts); err != nil {
		t.Fatalf("decode alerts: %v (body %s)", err, alertsRecorder.Body)
	}
	if alerts.Count != 1 || len(alerts.Alerts) != 1 {
		t.Fatalf("alerts response = %+v", alerts)
	}
	alert := alerts.Alerts[0]
	if alert.ID != event.ID || alert.DeviceID != event.DeviceID ||
		alert.PositionID != event.PositionID || alert.Type != event.Type {
		t.Errorf("routes disagree: event %+v, alert %+v", event, alert)
	}
	if !alert.AlertTime.Equal(eventTime) {
		t.Errorf("alertTime = %v, want %v", alert.AlertTime, eventTime)
	}

	// Both routes share the filter parsing, including the device scope.
	if filter := querier.lastFilter(t); filter.DeviceID != 7 {
		t.Errorf("filter device = %d, want 7", filter.DeviceID)
	}
}

// TestEventsRouteNeedsTheAlertsScope keeps the new route behind the same
// permission the old one had: a token that may read alerts may read events.
func TestEventsRouteNeedsTheAlertsScope(t *testing.T) {
	querier := &fakeQuerier{}
	handler := NewServer(querier, WithAPIKey("secret")).Handler()

	recorder := get(t, handler, "/api/events")
	if recorder.Code != 401 {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if body := recorder.Body.String(); strings.Contains(body, "events") && strings.Contains(body, "[") {
		t.Errorf("unauthenticated body leaked events: %s", body)
	}
}

// TestEventSingle covers GET /api/events/{id}: one row, 404 for an unknown id
// and 403 when the event belongs to a device the caller cannot reach.
func TestEventSingle(t *testing.T) {
	eventTime := time.Date(2026, 11, 1, 12, 30, 0, 0, time.UTC)
	querier := &fakeQuerier{alerts: []model.Event{{
		ID:         5,
		DeviceID:   7,
		PositionID: 42,
		Type:       model.EventGeofenceEnter,
		Time:       eventTime,
		GeofenceID: 3,
		Attributes: map[string]any{"geofence": "depot"},
	}}}
	handler := NewServer(querier).Handler()

	recorder := get(t, handler, "/api/events/5")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	event := decodeBodyValue[eventResponse](t, recorder)
	if event.ID != 5 || event.DeviceID != 7 || event.GeofenceID != 3 {
		t.Errorf("event = %+v", event)
	}
	if !event.EventTime.Equal(eventTime) {
		t.Errorf("eventTime = %v, want %v", event.EventTime, eventTime)
	}

	// The store returns nothing, so the id does not exist.
	empty := NewServer(&fakeQuerier{}).Handler()
	if recorder := get(t, empty, "/api/events/5"); recorder.Code != http.StatusNotFound {
		t.Errorf("missing status = %d, want 404", recorder.Code)
	}

	if recorder := get(t, handler, "/api/events/abc"); recorder.Code != http.StatusBadRequest {
		t.Errorf("bad id status = %d, want 400", recorder.Code)
	}

	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadAlerts}, []int64{8})
	scoped := NewServer(querier, withTestToken(principal)).Handler()
	if recorder := withToken(t, scoped, http.MethodGet, "/api/events/5", ""); recorder.Code != http.StatusForbidden {
		t.Errorf("unreachable device status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}

// TestEventsGroupFilterExpandsToChildGroups checks the groupId parameter: the
// devices of the group and of its descendants, which is what the Java report
// collects before it reads the events.
func TestEventsGroupFilterExpandsToChildGroups(t *testing.T) {
	querier := &fakeQuerier{}
	store := newFakePermissions()
	handler := NewServer(querier, WithPermissions(store)).Handler()

	recorder := get(t, handler, "/api/events?groupId=1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	// fakePermissions puts device 7 in group 2, below the requested group 1.
	if filter := querier.lastFilter(t); !reflect.DeepEqual(filter.DeviceIDs, []int64{7}) {
		t.Errorf("device ids = %v, want the devices of the group tree", filter.DeviceIDs)
	}

	if recorder := get(t, handler, "/api/events?groupId=abc"); recorder.Code != http.StatusBadRequest {
		t.Errorf("bad group status = %d, want 400", recorder.Code)
	}

	// A group without devices would otherwise read as "no restriction": an
	// empty non-nil slice has to reach the store instead of a nil one.
	recorder = get(t, handler, "/api/events?groupId=3")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if filter := querier.lastFilter(t); filter.DeviceIDs == nil || len(filter.DeviceIDs) != 0 {
		t.Errorf("device ids = %v, want an empty restriction", filter.DeviceIDs)
	}

	// A caller without a grant on the group is refused before the query runs.
	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadAlerts}, nil)
	scoped := NewServer(&fakeQuerier{}, WithPermissions(store), withTestToken(principal)).Handler()
	recorder = withToken(t, scoped, http.MethodGet, "/api/events?groupId=4", "")
	if recorder.Code != http.StatusForbidden {
		t.Errorf("unreachable group status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}
