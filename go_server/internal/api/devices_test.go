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

type fakeDeviceList struct {
	mu      sync.Mutex
	results []model.Device
	err     error
	filters []model.DeviceFilter
	created []model.DeviceCreate
	updated []model.DeviceUpdate
	deleted []int64
	device  *model.Device
	// Per-method errors so a test can fail exactly one operation.
	createErr error
	updateErr error
	deleteErr error
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

func (f *fakeDeviceList) CreateDevice(_ context.Context, create model.DeviceCreate) (*model.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.created = append(f.created, create)
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.device != nil {
		return f.device, nil
	}
	return &model.Device{
		ID:        1,
		UniqueID:  create.UniqueID,
		Name:      create.Name,
		Model:     create.Model,
		Protocol:  create.Protocol,
		CreatedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	}, nil
}

func (f *fakeDeviceList) UpdateDevice(_ context.Context, deviceID int64, update model.DeviceUpdate) (*model.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.updated = append(f.updated, update)
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if f.device != nil {
		return f.device, nil
	}
	return &model.Device{
		ID:        deviceID,
		UniqueID:  "864717003283581",
		Name:      update.Name,
		Model:     update.Model,
		Protocol:  "gt06",
		CreatedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	}, nil
}

func (f *fakeDeviceList) DeleteDevice(_ context.Context, deviceID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deleted = append(f.deleted, deviceID)
	return f.deleteErr
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

// request sends one arbitrary HTTP request, which the create / update /
// delete tests need because they are not GETs.
func request(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestCreateDevice(t *testing.T) {
	querier := &fakeDeviceList{}
	handler := NewServer(nil, WithDevices(querier)).Handler()

	recorder := request(t, handler, http.MethodPost, "/api/devices",
		`{"uniqueId":" 864717003283581 ","name":" van-7 ","model":"GT06N","protocol":"gt06"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body)
	}

	var device deviceResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &device); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if device.ID != 1 || device.UniqueID != "864717003283581" || device.Protocol != "gt06" {
		t.Fatalf("device = %+v", device)
	}
	if device.LastUpdate != nil {
		t.Errorf("lastUpdate = %v, want it omitted", device.LastUpdate)
	}

	querier.mu.Lock()
	defer querier.mu.Unlock()
	if len(querier.created) != 1 {
		t.Fatalf("created = %+v", querier.created)
	}
	create := querier.created[0]
	if create.UniqueID != "864717003283581" || create.Name != "van-7" || create.Model != "GT06N" {
		t.Errorf("create = %+v", create)
	}
}

func TestCreateDeviceValidation(t *testing.T) {
	querier := &fakeDeviceList{}
	handler := NewServer(nil, WithDevices(querier)).Handler()

	for name, body := range map[string]string{
		"invalid json":    `{"uniqueId":`,
		"missing unique":  `{"name":"van"}`,
		"blank unique id": `{"uniqueId":"   "}`,
	} {
		t.Run(name, func(t *testing.T) {
			if recorder := request(t, handler, http.MethodPost, "/api/devices", body); recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body)
			}
		})
	}

	querier.mu.Lock()
	defer querier.mu.Unlock()
	if len(querier.created) != 0 {
		t.Errorf("create was called %d times", len(querier.created))
	}
}

func TestCreateDeviceDuplicate(t *testing.T) {
	querier := &fakeDeviceList{createErr: model.ErrDuplicateDevice}
	handler := NewServer(nil, WithDevices(querier)).Handler()

	recorder := request(t, handler, http.MethodPost, "/api/devices", `{"uniqueId":"864717003283581"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestUpdateDevice(t *testing.T) {
	querier := &fakeDeviceList{}
	handler := NewServer(nil, WithDevices(querier)).Handler()

	recorder := request(t, handler, http.MethodPut, "/api/devices/7",
		`{"name":"van-7","model":"GT06N"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	var device deviceResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &device); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body)
	}
	if device.ID != 7 || device.Name != "van-7" || device.Model != "GT06N" {
		t.Fatalf("device = %+v", device)
	}

	querier.mu.Lock()
	defer querier.mu.Unlock()
	if len(querier.updated) != 1 || querier.updated[0].Name != "van-7" {
		t.Fatalf("updated = %+v", querier.updated)
	}
}

func TestUpdateDeviceErrors(t *testing.T) {
	notFound := &fakeDeviceList{updateErr: model.ErrDeviceNotFound}
	if recorder := request(t, NewServer(nil, WithDevices(notFound)).Handler(),
		http.MethodPut, "/api/devices/7", `{}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}

	good := &fakeDeviceList{}
	handler := NewServer(nil, WithDevices(good)).Handler()
	for _, target := range []string{"/api/devices/abc", "/api/devices/0", "/api/devices/-3"} {
		if recorder := request(t, handler, http.MethodPut, target, `{}`); recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, recorder.Code)
		}
	}

	failing := &fakeDeviceList{updateErr: errors.New("boom")}
	recorder := request(t, NewServer(nil, WithDevices(failing)).Handler(),
		http.MethodPut, "/api/devices/7", `{}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "boom") {
		t.Errorf("body leaks the database error: %s", recorder.Body)
	}
}

func TestDeleteDevice(t *testing.T) {
	querier := &fakeDeviceList{}
	handler := NewServer(nil, WithDevices(querier)).Handler()

	recorder := request(t, handler, http.MethodDelete, "/api/devices/7", "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", recorder.Code, recorder.Body)
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("body = %s, want empty", recorder.Body)
	}

	querier.mu.Lock()
	defer querier.mu.Unlock()
	if len(querier.deleted) != 1 || querier.deleted[0] != 7 {
		t.Fatalf("deleted = %v", querier.deleted)
	}
}

func TestDeleteDeviceErrors(t *testing.T) {
	notFound := &fakeDeviceList{deleteErr: model.ErrDeviceNotFound}
	if recorder := request(t, NewServer(nil, WithDevices(notFound)).Handler(),
		http.MethodDelete, "/api/devices/7", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}

	good := &fakeDeviceList{}
	if recorder := request(t, NewServer(nil, WithDevices(good)).Handler(),
		http.MethodDelete, "/api/devices/abc", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}

	failing := &fakeDeviceList{deleteErr: errors.New("boom")}
	recorder := request(t, NewServer(nil, WithDevices(failing)).Handler(),
		http.MethodDelete, "/api/devices/7", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "boom") {
		t.Errorf("body leaks the database error: %s", recorder.Body)
	}
}

func TestDeviceWritesNotConfigured(t *testing.T) {
	handler := NewServer(nil).Handler()

	for _, test := range []struct{ method, target, body string }{
		{http.MethodPost, "/api/devices", `{"uniqueId":"1"}`},
		{http.MethodPut, "/api/devices/7", `{}`},
		{http.MethodDelete, "/api/devices/7", ""},
	} {
		if recorder := request(t, handler, test.method, test.target, test.body); recorder.Code != http.StatusNotImplemented {
			t.Errorf("%s %s: status = %d, want 501", test.method, test.target, recorder.Code)
		}
	}
}
