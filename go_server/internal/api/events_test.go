package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
