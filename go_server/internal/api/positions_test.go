package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

type fakePositions struct {
	mu      sync.Mutex
	results []model.Position
	err     error
	filters []model.PositionFilter
}

func (f *fakePositions) QueryPositions(_ context.Context, filter model.PositionFilter) ([]model.Position, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.filters = append(f.filters, filter)
	if f.err != nil {
		return nil, f.err
	}

	results := f.results
	if filter.Offset > 0 {
		if filter.Offset >= len(results) {
			return nil, nil
		}
		results = results[filter.Offset:]
	}
	if filter.Limit > 0 && filter.Limit < len(results) {
		results = results[:filter.Limit]
	}
	return results, nil
}

func (f *fakePositions) lastFilter(t *testing.T) model.PositionFilter {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.filters) == 0 {
		t.Fatal("query was not called")
	}
	return f.filters[len(f.filters)-1]
}

func TestPositionsResponse(t *testing.T) {
	deviceTime := time.Date(2026, 10, 31, 13, 2, 10, 0, time.UTC)
	serverTime := deviceTime.Add(2 * time.Second)

	querier := &fakePositions{results: []model.Position{{
		ID:         11,
		DeviceID:   7,
		Protocol:   "gt06",
		Time:       deviceTime,
		ServerTime: serverTime,
		Valid:      true,
		Latitude:   54.943,
		Longitude:  25.206,
		Speed:      27,
		Course:     120,
		Accuracy:   5,
		Attributes: map[string]any{"sat": 9},
	}}}
	handler := NewServer(nil, WithPositions(querier)).Handler()

	recorder := get(t, handler, "/api/positions?deviceId=7")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	var response []positionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if len(response) != 1 {
		t.Fatalf("response = %+v", response)
	}

	position := response[0]
	if position.ID != 11 || position.DeviceID != 7 || position.Protocol != "gt06" {
		t.Fatalf("position = %+v", position)
	}
	if !position.DeviceTime.Equal(deviceTime) || !position.FixTime.Equal(deviceTime) ||
		!position.ServerTime.Equal(serverTime) {
		t.Errorf("times = %s / %s / %s", position.DeviceTime, position.FixTime, position.ServerTime)
	}
	if !position.Valid || position.Latitude != 54.943 || position.Longitude != 25.206 {
		t.Errorf("position = %+v", position)
	}
	if position.Attributes["sat"] != float64(9) {
		t.Errorf("attributes = %v", position.Attributes)
	}

	if filter := querier.lastFilter(t); filter.DeviceID != 7 {
		t.Errorf("filter = %+v", filter)
	}
}

func TestPositionsFilterParameters(t *testing.T) {
	querier := &fakePositions{}
	handler := NewServer(nil, WithPositions(querier)).Handler()

	recorder := get(t, handler,
		"/api/positions?deviceId=7&from=2026-10-30T00:00:00Z&to=1793491200000&limit=50&offset=10")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	filter := querier.lastFilter(t)
	if filter.DeviceID != 7 {
		t.Errorf("deviceId = %d", filter.DeviceID)
	}
	if filter.From.Format(time.RFC3339) != "2026-10-30T00:00:00Z" {
		t.Errorf("from = %s", filter.From)
	}
	if filter.To.Format(time.RFC3339) != "2026-11-01T00:00:00Z" {
		t.Errorf("to = %s", filter.To)
	}
	if filter.Limit != 50 || filter.Offset != 10 {
		t.Errorf("limit/offset = %d/%d", filter.Limit, filter.Offset)
	}
}

func TestPositionsInvalidParameters(t *testing.T) {
	querier := &fakePositions{}
	handler := NewServer(nil, WithPositions(querier)).Handler()

	for _, target := range []string{
		"/api/positions?deviceId=0",
		"/api/positions?deviceId=abc",
		"/api/positions?from=yesterday",
		"/api/positions?from=2026-10-31T00:00:00Z&to=2026-10-30T00:00:00Z",
		"/api/positions?limit=0",
		"/api/positions?limit=5000",
		"/api/positions?offset=-1",
	} {
		if recorder := get(t, handler, target); recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, recorder.Code)
		}
	}

	querier.mu.Lock()
	defer querier.mu.Unlock()
	if len(querier.filters) != 0 {
		t.Errorf("query was called %d times", len(querier.filters))
	}
}

func TestPositionsQueryError(t *testing.T) {
	querier := &fakePositions{err: errors.New("boom")}
	handler := NewServer(nil, WithPositions(querier)).Handler()

	recorder := get(t, handler, "/api/positions")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "boom") {
		t.Errorf("body leaks the database error: %s", recorder.Body)
	}
}

func TestPositionsNotConfigured(t *testing.T) {
	handler := NewServer(nil).Handler()

	if recorder := get(t, handler, "/api/positions"); recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", recorder.Code)
	}
}

func TestPositionsEmptyAttributes(t *testing.T) {
	querier := &fakePositions{results: []model.Position{{
		ID:       1,
		DeviceID: 7,
		Protocol: "h02",
		Time:     time.Date(2026, 10, 31, 13, 2, 10, 0, time.UTC),
	}}}
	handler := NewServer(nil, WithPositions(querier)).Handler()

	recorder := get(t, handler, "/api/positions")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"attributes":{}`) {
		t.Errorf("body = %s, want an empty attributes object", recorder.Body)
	}
}

// TestPositionSingle covers GET /api/positions/{id} and the geofence ids the
// geofence stage writes onto a position.
func TestPositionSingle(t *testing.T) {
	deviceTime := time.Date(2026, 10, 31, 13, 2, 10, 0, time.UTC)
	querier := &fakePositions{results: []model.Position{{
		ID:          11,
		DeviceID:    7,
		Protocol:    "gt06",
		Time:        deviceTime,
		ServerTime:  deviceTime.Add(time.Second),
		Valid:       true,
		Latitude:    54.943,
		Longitude:   25.206,
		GeofenceIDs: []int64{5, 9},
	}}}
	handler := NewServer(nil, WithPositions(querier)).Handler()

	recorder := get(t, handler, "/api/positions/11")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	position := decodeBodyValue[positionResponse](t, recorder)
	if position.ID != 11 || position.DeviceID != 7 {
		t.Errorf("position = %+v", position)
	}
	if len(position.GeofenceIDs) != 2 || position.GeofenceIDs[0] != 5 || position.GeofenceIDs[1] != 9 {
		t.Errorf("geofenceIds = %v, want [5 9]", position.GeofenceIDs)
	}
	if filter := querier.lastFilter(t); len(filter.IDs) != 1 || filter.IDs[0] != 11 {
		t.Errorf("filter ids = %v, want [11]", filter.IDs)
	}

	if recorder := get(t, handler, "/api/positions/abc"); recorder.Code != http.StatusBadRequest {
		t.Errorf("bad id status = %d, want 400", recorder.Code)
	}
	if recorder := get(t, NewServer(nil).Handler(), "/api/positions/11"); recorder.Code != http.StatusNotImplemented {
		t.Errorf("unconfigured status = %d, want 501", recorder.Code)
	}
}

func TestPositionSingleNotFound(t *testing.T) {
	querier := &fakePositions{}
	handler := NewServer(nil, WithPositions(querier)).Handler()

	if recorder := get(t, handler, "/api/positions/11"); recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (body %s)", recorder.Code, recorder.Body)
	}
}
