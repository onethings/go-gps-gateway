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

	"traccar-server_go/go_server/internal/model"
)

type fakeQuerier struct {
	mu      sync.Mutex
	alerts  []model.Alert
	err     error
	filters []model.AlertFilter
}

func (f *fakeQuerier) QueryAlerts(_ context.Context, filter model.AlertFilter) ([]model.Alert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filters = append(f.filters, filter)
	if f.err != nil {
		return nil, f.err
	}
	return f.alerts, nil
}

func (f *fakeQuerier) lastFilter(t *testing.T) model.AlertFilter {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.filters) == 0 {
		t.Fatal("query was not called")
	}
	return f.filters[len(f.filters)-1]
}

func get(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

func TestAlertsResponse(t *testing.T) {
	alertTime := time.Date(2026, 10, 31, 13, 2, 10, 0, time.UTC)
	querier := &fakeQuerier{alerts: []model.Alert{{
		ID:         3,
		DeviceID:   7,
		PositionID: 11,
		Type:       model.AlertGeofenceEnter,
		Time:       alertTime,
		GeofenceID: 5,
		Attributes: map[string]any{"geofence": "depot"},
	}}}
	handler := NewServer(querier).Handler()

	recorder := get(t, handler, "/api/alerts?deviceId=7")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("content type = %q", contentType)
	}

	var response alertsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if response.Count != 1 || len(response.Alerts) != 1 {
		t.Fatalf("response = %+v", response)
	}
	alert := response.Alerts[0]
	if alert.ID != 3 || alert.DeviceID != 7 || alert.PositionID != 11 ||
		alert.Type != model.AlertGeofenceEnter || alert.GeofenceID != 5 {
		t.Errorf("alert = %+v", alert)
	}
	if !alert.AlertTime.Equal(alertTime) {
		t.Errorf("alert time = %v, want %v", alert.AlertTime, alertTime)
	}
	if alert.Attributes["geofence"] != "depot" {
		t.Errorf("attributes = %#v", alert.Attributes)
	}
}

func TestAlertsParsesFilter(t *testing.T) {
	querier := &fakeQuerier{}
	handler := NewServer(querier).Handler()

	recorder := get(t, handler,
		"/api/alerts?deviceId=7&type=deviceOverspeed,geofenceEnter&type=powerCut"+
			"&from=2026-10-30T00:00:00Z&to=1793491200000&limit=50&offset=10")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}

	filter := querier.lastFilter(t)
	if filter.DeviceID != 7 {
		t.Errorf("device id = %d, want 7", filter.DeviceID)
	}
	if strings.Join(filter.Types, "|") != "deviceOverspeed|geofenceEnter|powerCut" {
		t.Errorf("types = %v", filter.Types)
	}
	if want := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC); !filter.From.Equal(want) {
		t.Errorf("from = %v, want %v", filter.From, want)
	}
	if want := time.UnixMilli(1793491200000).UTC(); !filter.To.Equal(want) {
		t.Errorf("to = %v, want %v", filter.To, want)
	}
	if filter.Limit != 50 || filter.Offset != 10 {
		t.Errorf("limit/offset = %d/%d", filter.Limit, filter.Offset)
	}
}

func TestAlertsEmptyResult(t *testing.T) {
	handler := NewServer(&fakeQuerier{}).Handler()

	recorder := get(t, handler, "/api/alerts")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := strings.TrimSpace(recorder.Body.String())
	if body != `{"alerts":[],"count":0}` {
		t.Errorf("body = %s", body)
	}
}

func TestAlertsRejectsBadQueries(t *testing.T) {
	tests := []string{
		"/api/alerts?deviceId=abc",
		"/api/alerts?deviceId=0",
		"/api/alerts?limit=0",
		"/api/alerts?limit=5000",
		"/api/alerts?offset=-1",
		"/api/alerts?from=yesterday",
		"/api/alerts?from=2026-10-31T00:00:00Z&to=2026-10-30T00:00:00Z",
	}
	querier := &fakeQuerier{}
	handler := NewServer(querier).Handler()

	for _, target := range tests {
		recorder := get(t, handler, target)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, recorder.Code)
			continue
		}
		var payload map[string]string
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil || payload["error"] == "" {
			t.Errorf("%s: body = %s", target, recorder.Body)
		}
	}
	if len(querier.filters) != 0 {
		t.Errorf("storage was queried %d times for invalid requests", len(querier.filters))
	}
}

func TestAlertsHidesStorageErrors(t *testing.T) {
	querier := &fakeQuerier{err: errors.New("pq: relation \"alerts\" does not exist")}
	var logged strings.Builder
	handler := NewServer(querier, WithLogger(func(format string, args ...any) {
		logged.WriteString(format)
	})).Handler()

	recorder := get(t, handler, "/api/alerts")
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

func TestAlertsRejectsOtherMethods(t *testing.T) {
	recorder := httptest.NewRecorder()
	NewServer(&fakeQuerier{}).Handler().ServeHTTP(recorder,
		httptest.NewRequest(http.MethodPost, "/api/alerts", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}

func TestHealth(t *testing.T) {
	recorder := get(t, NewServer(&fakeQuerier{}).Handler(), "/api/health")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["status"] != "ok" {
		t.Errorf("payload = %v", payload)
	}
}

func TestParseTime(t *testing.T) {
	if _, err := parseTime(""); err != nil {
		t.Errorf("empty value: %v", err)
	}

	rfc, err := parseTime("2026-10-31T14:02:27Z")
	if err != nil || !rfc.Equal(time.Date(2026, 10, 31, 14, 2, 27, 0, time.UTC)) {
		t.Errorf("rfc3339 = %v, err = %v", rfc, err)
	}

	millis, err := parseTime("1793491200123")
	if err != nil || millis.UnixMilli() != 1793491200123 {
		t.Errorf("millis = %v, err = %v", millis, err)
	}

	if _, err := parseTime("31/10/2026"); err == nil {
		t.Error("expected an error for an unsupported format")
	}
}
