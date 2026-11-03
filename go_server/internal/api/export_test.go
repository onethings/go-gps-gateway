package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
)

// fakeGeofenceLookup serves the geofences a geofenceId filter or an export
// asks for.
type fakeGeofenceLookup struct {
	fences []*geofence.Geofence
	err    error
}

func (f *fakeGeofenceLookup) QueryGeofences(_ context.Context, filter model.ResourceFilter) ([]*geofence.Geofence, error) {
	if f.err != nil {
		return nil, f.err
	}
	if filter.IDs == nil {
		return f.fences, nil
	}
	wanted := make(map[int64]struct{}, len(filter.IDs))
	for _, id := range filter.IDs {
		wanted[id] = struct{}{}
	}
	var matched []*geofence.Geofence
	for _, fence := range f.fences {
		if _, ok := wanted[fence.ID]; ok {
			matched = append(matched, fence)
		}
	}
	return matched, nil
}

func sampleTrack() []model.Position {
	first := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	second := first.Add(30 * time.Second)
	return []model.Position{
		{
			ID:         1,
			DeviceID:   7,
			Protocol:   "gt06",
			Time:       first,
			ServerTime: first,
			Valid:      true,
			Latitude:   51.5,
			Longitude:  0.25,
			Altitude:   10,
			Speed:      20,
			Course:     90,
			Accuracy:   5,
			Attributes: map[string]any{"note": "=cmd|' /C calc'!A0", "sat": 9},
		},
		{
			ID:         2,
			DeviceID:   7,
			Protocol:   "gt06",
			Time:       second,
			ServerTime: second,
			Valid:      true,
			Latitude:   51.5001,
			Longitude:  0.2501,
			Altitude:   12,
			Speed:      22,
			Course:     91,
			Accuracy:   5,
			Attributes: map[string]any{"sat": 10},
		},
	}
}

func exportServer(positions *fakePositions, geofences *fakeGeofenceLookup) http.Handler {
	devices := &fakeDeviceList{device: &model.Device{
		ID:       7,
		UniqueID: "864717003283581",
		Name:     "van <7>",
		Protocol: "gt06",
	}}
	options := []Option{WithPositions(positions), WithDevices(devices)}
	if geofences != nil {
		options = append(options, WithGeofenceLookup(geofences))
	}
	return NewServer(nil, options...).Handler()
}

func TestPositionsGeofenceFilter(t *testing.T) {
	area, err := geofence.Parse("CIRCLE (51.5 0.0, 1000)")
	if err != nil {
		t.Fatalf("parse fence: %v", err)
	}
	lookup := &fakeGeofenceLookup{fences: []*geofence.Geofence{{ID: 3, Name: "depot", Area: area}}}
	positions := &fakePositions{results: []model.Position{
		{ID: 1, DeviceID: 7, Latitude: 51.5, Longitude: 0.0},
		{ID: 2, DeviceID: 7, Latitude: 51.6, Longitude: 0.0},
	}}
	handler := exportServer(positions, lookup)

	recorder := get(t, handler, "/api/positions?geofenceId=3")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	response := decodeBodyValue[[]positionResponse](t, recorder)
	if len(response) != 1 || response[0].ID != 1 {
		t.Errorf("positions = %+v, want only the one inside the fence", response)
	}

	if recorder := get(t, handler, "/api/positions?geofenceId=9"); recorder.Code != http.StatusNotFound {
		t.Errorf("unknown fence status = %d, want 404", recorder.Code)
	}
	if recorder := get(t, handler, "/api/positions?geofenceId=abc"); recorder.Code != http.StatusBadRequest {
		t.Errorf("bad fence status = %d, want 400", recorder.Code)
	}

	// Without the lookup the parameter cannot be honoured.
	plain := exportServer(positions, nil)
	if recorder := get(t, plain, "/api/positions?geofenceId=3"); recorder.Code != http.StatusNotImplemented {
		t.Errorf("unconfigured status = %d, want 501", recorder.Code)
	}
}

func TestPositionsCSVExport(t *testing.T) {
	positions := &fakePositions{results: sampleTrack()}
	handler := exportServer(positions, nil)

	recorder := get(t, handler, "/api/positions/csv?deviceId=7")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/csv" {
		t.Errorf("content type = %q", contentType)
	}
	if disposition := recorder.Header().Get("Content-Disposition"); disposition != "attachment; filename=positions.csv" {
		t.Errorf("disposition = %q", disposition)
	}

	rows, err := csv.NewReader(strings.NewReader(recorder.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v (body %s)", err, recorder.Body)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want a header and two positions", len(rows))
	}
	header := strings.Join(rows[0], ",")
	for _, column := range []string{"id", "deviceId", "deviceTime", "latitude", "note", "sat"} {
		if !strings.Contains(header, column) {
			t.Errorf("header %q is missing %q", header, column)
		}
	}
	// The attribute columns are sorted, so note comes before sat.
	if rows[0][len(rows[0])-2] != "note" || rows[0][len(rows[0])-1] != "sat" {
		t.Errorf("attribute columns = %v, want note then sat", rows[0][len(rows[0])-2:])
	}

	first := rows[1]
	if first[0] != "1" || first[1] != "7" || first[2] != "gt06" || first[3] != "2026-11-01 12:00:00" {
		t.Errorf("row = %v", first)
	}
	// A cell that starts like a spreadsheet formula is blanked, the same guard
	// the Java exporter applies.
	if note := first[len(first)-2]; note != "" {
		t.Errorf("note cell = %q, want the formula guard to blank it", note)
	}
	if sat := first[len(first)-1]; sat != "9" {
		t.Errorf("sat cell = %q, want 9", sat)
	}

	if filter := positions.lastFilter(t); filter.DeviceID != 7 || !filter.Ascending {
		t.Errorf("filter = %+v, want the device and an ascending track", filter)
	}
}

func TestPositionsGPXExport(t *testing.T) {
	handler := exportServer(&fakePositions{results: sampleTrack()}, nil)

	recorder := get(t, handler, "/api/positions/gpx?deviceId=7")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/gpx+xml" {
		t.Errorf("content type = %q", contentType)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`<gpx version="1.0">`,
		`<name>van &lt;7&gt;</name>`,
		`<trkpt lat="51.500000" lon="0.250000">`,
		`<ele>10.000000</ele>`,
		`<time>2026-11-01T12:00:00Z</time>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q:\n%s", want, body)
		}
	}
}

func TestPositionsKMLExport(t *testing.T) {
	handler := exportServer(&fakePositions{results: sampleTrack()}, nil)

	recorder := get(t, handler, "/api/positions/kml?deviceId=7")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/vnd.google-earth.kml+xml" {
		t.Errorf("content type = %q", contentType)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`<kml xmlns="http://www.opengis.net/kml/2.2">`,
		`<name>van &lt;7&gt;</name>`,
		`<coordinates>0.250000,51.500000,10.000000`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q:\n%s", want, body)
		}
	}
}

func TestPositionsKMZExport(t *testing.T) {
	handler := exportServer(&fakePositions{results: sampleTrack()}, nil)

	recorder := get(t, handler, "/api/positions/kmz?deviceId=7")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/vnd.google-earth.kmz" {
		t.Errorf("content type = %q", contentType)
	}

	body := recorder.Body.Bytes()
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("open kmz: %v", err)
	}
	if len(archive.File) != 1 || archive.File[0].Name != "doc.kml" {
		t.Fatalf("entries = %v, want doc.kml", archive.File)
	}
	entry, err := archive.File[0].Open()
	if err != nil {
		t.Fatalf("open entry: %v", err)
	}
	defer entry.Close()
	content, err := io.ReadAll(entry)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if !strings.Contains(string(content), "<coordinates>") {
		t.Errorf("doc.kml = %s", content)
	}
}

func TestPositionsExportValidation(t *testing.T) {
	handler := exportServer(&fakePositions{results: sampleTrack()}, nil)

	for _, target := range []string{
		"/api/positions/csv",
		"/api/positions/gpx",
		"/api/positions/kml?deviceId=abc",
		"/api/positions/csv?deviceId=7&from=not-a-time",
	} {
		if recorder := get(t, handler, target); recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, recorder.Code)
		}
	}

	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadPositions}, []int64{8})
	scoped := NewServer(nil,
		WithPositions(&fakePositions{results: sampleTrack()}),
		WithDevices(&fakeDeviceList{device: &model.Device{ID: 7, Name: "van"}}),
		withTestToken(principal),
	).Handler()
	if recorder := withToken(t, scoped, http.MethodGet, "/api/positions/csv?deviceId=7", ""); recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}

	notConfigured := NewServer(nil).Handler()
	if recorder := get(t, notConfigured, "/api/positions/csv?deviceId=7"); recorder.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", recorder.Code)
	}
}

// TestExportPositionsPagesTheTrack pins the paging loop: the store caps one
// query at exportPageSize, so a longer track has to be read page by page.
func TestExportPositionsPagesTheTrack(t *testing.T) {
	track := make([]model.Position, 0, exportPageSize+5)
	for index := 0; index < exportPageSize+5; index++ {
		track = append(track, model.Position{ID: int64(index + 1), DeviceID: 7})
	}
	positions := &fakePositions{results: track}
	server := NewServer(nil, WithPositions(positions))

	got, err := server.exportPositions(context.Background(), model.PositionFilter{DeviceID: 7, Ascending: true}, nil)
	if err != nil {
		t.Fatalf("export positions: %v", err)
	}
	if len(got) != len(track) {
		t.Errorf("positions = %d, want %d", len(got), len(track))
	}

	positions.mu.Lock()
	filters := append([]model.PositionFilter(nil), positions.filters...)
	positions.mu.Unlock()
	if len(filters) != 2 || filters[0].Offset != 0 || filters[1].Offset != exportPageSize {
		t.Fatalf("filters = %+v, want two pages", filters)
	}
	for _, filter := range filters {
		if filter.Limit != exportPageSize || !filter.Ascending {
			t.Errorf("filter = %+v, want an ascending page", filter)
		}
	}
}
