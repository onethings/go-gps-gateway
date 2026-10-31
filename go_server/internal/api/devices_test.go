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

type fakeDeviceList struct {
	mu      sync.Mutex
	results []model.Device
	err     error
	filters []model.DeviceFilter
}

func (f *fakeDeviceList) Devices(_ context.Context, filter model.DeviceFilter) ([]model.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.filters = append(f.filters, filter)
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

func (f *fakeDeviceList) lastFilter(t *testing.T) model.DeviceFilter {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.filters) == 0 {
		t.Fatal("query was not called")
	}
	return f.filters[len(f.filters)-1]
}

func TestDevicesResponse(t *testing.T) {
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 10, 31, 13, 2, 11, 0, time.UTC)

	querier := &fakeDeviceList{results: []model.Device{{
		ID:         7,
		UniqueID:   "864717003283581",
		Name:       "van-7",
		Model:      "GT06N",
		Protocol:   "gt06",
		Attributes: map[string]any{"floor": 0},
		LastUpdate: updated,
		CreatedAt:  created,
	}}}
	handler := NewServer(nil, WithDevices(querier)).Handler()

	recorder := get(t, handler, "/api/devices")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	var response devicesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if response.Count != 1 || len(response.Devices) != 1 {
		t.Fatalf("response = %+v", response)
	}

	device := response.Devices[0]
	if device.ID != 7 || device.UniqueID != "864717003283581" || device.Name != "van-7" {
		t.Fatalf("device = %+v", device)
	}
	if device.Protocol != "gt06" || device.Model != "GT06N" {
		t.Errorf("device = %+v", device)
	}
	if device.LastUpdate == nil || !device.LastUpdate.Equal(updated) {
		t.Errorf("lastUpdate = %v", device.LastUpdate)
	}
	if !device.CreatedAt.Equal(created) {
		t.Errorf("createdAt = %s", device.CreatedAt)
	}
	if device.Attributes["floor"] != float64(0) {
		t.Errorf("attributes = %v", device.Attributes)
	}
}

func TestDevicesOmitsEmptyOptionalFields(t *testing.T) {
	querier := &fakeDeviceList{results: []model.Device{{
		ID:        9,
		UniqueID:  "123456789012345",
		Protocol:  "teltonika",
		CreatedAt: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
	}}}
	handler := NewServer(nil, WithDevices(querier)).Handler()

	recorder := get(t, handler, "/api/devices")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	devices, ok := payload["devices"].([]any)
	if !ok || len(devices) != 1 {
		t.Fatalf("payload = %v", payload)
	}
	entry, ok := devices[0].(map[string]any)
	if !ok {
		t.Fatalf("entry = %v", devices[0])
	}
	for _, key := range []string{"lastUpdate", "model"} {
		if _, present := entry[key]; present {
			t.Errorf("%q should be omitted when empty: %v", key, entry)
		}
	}
	if _, present := entry["attributes"]; !present {
		t.Errorf("attributes should always be present: %v", entry)
	}
}

func TestDevicesFilterParameters(t *testing.T) {
	querier := &fakeDeviceList{}
	handler := NewServer(nil, WithDevices(querier)).Handler()

	recorder := get(t, handler, "/api/devices?protocol=gt06&limit=5&offset=2")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	filter := querier.lastFilter(t)
	if filter.Protocol != "gt06" || filter.Limit != 5 || filter.Offset != 2 {
		t.Fatalf("filter = %+v", filter)
	}
}

func TestDevicesInvalidParameters(t *testing.T) {
	querier := &fakeDeviceList{}
	handler := NewServer(nil, WithDevices(querier)).Handler()

	for _, target := range []string{
		"/api/devices?limit=0",
		"/api/devices?limit=5000",
		"/api/devices?offset=-1",
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

func TestDevicesQueryError(t *testing.T) {
	querier := &fakeDeviceList{err: errors.New("boom")}
	handler := NewServer(nil, WithDevices(querier)).Handler()

	recorder := get(t, handler, "/api/devices")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "boom") {
		t.Errorf("body leaks the database error: %s", recorder.Body)
	}
}

func TestDevicesNotConfigured(t *testing.T) {
	handler := NewServer(nil).Handler()

	if recorder := get(t, handler, "/api/devices"); recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", recorder.Code)
	}
}
