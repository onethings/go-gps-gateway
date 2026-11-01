package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/report"
)

// fakeReportStore serves one device with a couple of positions and events.
type fakeReportStore struct {
	mu        sync.Mutex
	devices   []model.Device
	positions map[int64][]model.Position
	events    map[int64][]model.Event
	filters   []model.DeviceFilter
}

func newFakeReportStore() *fakeReportStore {
	base := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	positions := []model.Position{}
	for index := 0; index < 3; index++ {
		position := model.NewPosition("gt06")
		position.ID = int64(index + 1)
		position.DeviceID = 7
		position.Time = base.Add(time.Duration(index) * 10 * time.Minute)
		position.Valid = true
		position.Latitude = 54.9 + float64(index)*0.01
		position.Longitude = 24.1
		position.Speed = 10
		position.Set(model.KeyMotion, index > 0)
		positions = append(positions, *position)
	}
	return &fakeReportStore{
		devices:   []model.Device{{ID: 7, UniqueID: "864717003283581", Name: "van-7"}},
		positions: map[int64][]model.Position{7: positions},
		events: map[int64][]model.Event{7: {
			{ID: 1, DeviceID: 7, Type: model.EventDeviceMoving, PositionID: 1, Time: base},
		}},
	}
}

func (f *fakeReportStore) QueryPositions(_ context.Context, filter model.PositionFilter) ([]model.Position, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.positions[filter.DeviceID]
	if filter.Offset >= len(all) {
		return nil, nil
	}
	end := len(all)
	if filter.Limit > 0 && filter.Offset+filter.Limit < end {
		end = filter.Offset + filter.Limit
	}
	return append([]model.Position(nil), all[filter.Offset:end]...), nil
}

func (f *fakeReportStore) QueryEvents(_ context.Context, filter model.EventFilter) ([]model.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Event(nil), f.events[filter.DeviceID]...), nil
}

func (f *fakeReportStore) Devices(_ context.Context, filter model.DeviceFilter) ([]model.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filters = append(f.filters, filter)
	if filter.DeviceIDs == nil {
		return append([]model.Device(nil), f.devices...), nil
	}
	wanted := make(map[int64]struct{}, len(filter.DeviceIDs))
	for _, id := range filter.DeviceIDs {
		wanted[id] = struct{}{}
	}
	var devices []model.Device
	for _, device := range f.devices {
		if _, ok := wanted[device.ID]; ok {
			devices = append(devices, device)
		}
	}
	return devices, nil
}

func (f *fakeReportStore) LatestPositions(context.Context, []int64) ([]model.Position, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var latest []model.Position
	for _, positions := range f.positions {
		if len(positions) > 0 {
			latest = append(latest, positions[len(positions)-1])
		}
	}
	return latest, nil
}

func (f *fakeReportStore) QueryGeofences(context.Context, model.ResourceFilter) ([]*geofence.Geofence, error) {
	return nil, nil
}

func (f *fakeReportStore) Drivers(context.Context, model.ResourceFilter) ([]*model.Driver, error) {
	return nil, nil
}

// fakeReportMailer records the report mail.
type fakeReportMailer struct {
	mu       sync.Mutex
	messages []reportMailCall
	err      error
}

type reportMailCall struct {
	Recipients []string
	Subject    string
	Filename   string
	Data       []byte
}

func (f *fakeReportMailer) SendReport(
	_ context.Context, recipients []string, subject, body, filename, contentType string, data []byte,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.messages = append(f.messages, reportMailCall{
		Recipients: recipients, Subject: subject, Filename: filename, Data: data,
	})
	return nil
}

func reportHandler(options ...Option) http.Handler {
	store := newFakeReportStore()
	base := []Option{
		WithReports(report.New(store, report.Config{})),
		WithAPIKey("secret"),
	}
	return NewServer(nil, append(base, options...)...).Handler()
}

func TestReportRouteAndExports(t *testing.T) {
	handler := reportHandler()

	recorder := getWith(t, handler,
		"/api/reports/route?deviceId=7&from=2026-11-01T00:00:00Z&to=2026-11-02T00:00:00Z",
		map[string]string{"X-Api-Key": "secret"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("route = %d (body %s)", recorder.Code, recorder.Body)
	}
	var positions []report.PositionItem
	if err := json.Unmarshal(recorder.Body.Bytes(), &positions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(positions) != 3 || positions[0].DeviceID != 7 {
		t.Fatalf("positions = %+v", positions)
	}

	csvRecorder := getWith(t, handler,
		"/api/reports/route/csv?deviceId=7&from=2026-11-01T00:00:00Z&to=2026-11-02T00:00:00Z",
		map[string]string{"X-Api-Key": "secret"})
	if csvRecorder.Code != http.StatusOK {
		t.Fatalf("csv = %d (body %s)", csvRecorder.Code, csvRecorder.Body)
	}
	if contentType := csvRecorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/csv") {
		t.Errorf("content type = %q", contentType)
	}
	if disposition := csvRecorder.Header().Get("Content-Disposition"); disposition != "attachment; filename=report.csv" {
		t.Errorf("disposition = %q", disposition)
	}
	if !strings.HasPrefix(csvRecorder.Body.String(), "id,deviceId,protocol") {
		t.Errorf("csv = %s", csvRecorder.Body.String())
	}

	gpxRecorder := getWith(t, handler,
		"/api/reports/route/gpx?deviceId=7&from=2026-11-01T00:00:00Z&to=2026-11-02T00:00:00Z",
		map[string]string{"X-Api-Key": "secret"})
	if gpxRecorder.Code != http.StatusOK || !strings.Contains(gpxRecorder.Body.String(), "<trkpt") {
		t.Fatalf("gpx = %d (body %s)", gpxRecorder.Code, gpxRecorder.Body)
	}
}

func TestReportRequiresRange(t *testing.T) {
	handler := reportHandler()
	recorder := getWith(t, handler, "/api/reports/route?deviceId=7",
		map[string]string{"X-Api-Key": "secret"})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestReportRespectsDisabledServerSetting(t *testing.T) {
	store := &fakeServerSettings{settings: model.Server{ID: 1, DisableReports: true}}
	handler := reportHandler(WithServerSettings(store))

	recorder := getWith(t, handler,
		"/api/reports/route?deviceId=7&from=2026-11-01T00:00:00Z&to=2026-11-02T00:00:00Z",
		map[string]string{"X-Api-Key": "secret"})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestReportMailVariant(t *testing.T) {
	mailer := &fakeReportMailer{}
	users := newFakeUsers(&model.User{ID: 1, Email: "ops@example.com"})
	handler := reportHandler(WithReportMailer(mailer), WithUsers(users),
		withTestToken(auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeAll}, nil)))

	recorder := withToken(t, handler, http.MethodGet,
		"/api/reports/trips/mail?deviceId=7&from=2026-11-01T00:00:00Z&to=2026-11-02T00:00:00Z", "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("mail = %d (body %s)", recorder.Code, recorder.Body)
	}
	mailer.mu.Lock()
	defer mailer.mu.Unlock()
	if len(mailer.messages) != 1 || mailer.messages[0].Filename != "report.xlsx" {
		t.Fatalf("messages = %+v", mailer.messages)
	}
	if len(mailer.messages[0].Data) == 0 {
		t.Error("empty attachment")
	}
}
