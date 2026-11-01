package report

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
)

// fakeStore serves positions and events from maps, honouring the filter the
// report passes down.
type fakeStore struct {
	positions map[int64][]model.Position
	events    map[int64][]model.Event
	devices   []model.Device
	geofences []*geofence.Geofence
	drivers   []*model.Driver
}

func (f *fakeStore) QueryPositions(_ context.Context, filter model.PositionFilter) ([]model.Position, error) {
	all := f.positions[filter.DeviceID]
	if !filter.From.IsZero() {
		filtered := all[:0]
		for _, position := range all {
			if !position.Time.Before(filter.From) {
				filtered = append(filtered, position)
			}
		}
		all = filtered
	}
	if !filter.To.IsZero() {
		filtered := all[:0]
		for _, position := range all {
			if !position.Time.After(filter.To) {
				filtered = append(filtered, position)
			}
		}
		all = filtered
	}
	limit := filter.Limit
	if limit <= 0 || limit > len(all) {
		limit = len(all)
	}
	offset := filter.Offset
	if offset > len(all) {
		offset = len(all)
	}
	return append([]model.Position(nil), all[offset:limit]...), nil
}

func (f *fakeStore) QueryEvents(_ context.Context, filter model.EventFilter) ([]model.Event, error) {
	var result []model.Event
	for _, event := range f.events[filter.DeviceID] {
		if event.Time.Before(filter.From) || event.Time.After(filter.To) {
			continue
		}
		if len(filter.Types) > 0 && !containsString(filter.Types, event.Type) {
			continue
		}
		result = append(result, event)
	}
	return result, nil
}

func (f *fakeStore) Devices(context.Context, model.DeviceFilter) ([]model.Device, error) {
	return append([]model.Device(nil), f.devices...), nil
}

func (f *fakeStore) LatestPositions(context.Context, []int64) ([]model.Position, error) {
	var result []model.Position
	for _, devicePositions := range f.positions {
		if len(devicePositions) > 0 {
			result = append(result, devicePositions[len(devicePositions)-1])
		}
	}
	return result, nil
}

func (f *fakeStore) QueryGeofences(context.Context, model.ResourceFilter) ([]*geofence.Geofence, error) {
	return f.geofences, nil
}

func (f *fakeStore) Drivers(context.Context, model.ResourceFilter) ([]*model.Driver, error) {
	return f.drivers, nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func at(minutes int) time.Time {
	return time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute)
}

// movementPosition builds a position with an id, a time and a motion flag.
func movementPosition(id int64, minutes int, moving bool, speed float64) model.Position {
	position := model.NewPosition("gt06")
	position.ID = id
	position.DeviceID = 7
	position.Time = at(minutes)
	position.Valid = true
	position.Latitude = 54.9 + float64(minutes)*0.001
	position.Longitude = 24.1
	position.Speed = speed
	position.Set(model.KeyMotion, moving)
	return *position
}

func testStore(positions []model.Position) *fakeStore {
	return &fakeStore{
		positions: map[int64][]model.Position{7: positions},
		devices: []model.Device{{
			ID: 7, UniqueID: "864717003283581", Name: "van-7",
			Attributes: model.Attributes{"fuelCapacity": 60.0},
		}},
	}
}

func TestTripsReport(t *testing.T) {
	positions := []model.Position{
		movementPosition(1, 0, false, 0),
		movementPosition(2, 1, true, 10),
		movementPosition(3, 10, true, 30),
		movementPosition(4, 20, true, 20),
		movementPosition(5, 21, false, 0),
		movementPosition(6, 40, false, 0),
	}
	service := New(testStore(positions), Config{})

	trips, err := service.TripsReport(context.Background(), []int64{7}, at(-60), at(120))
	if err != nil {
		t.Fatalf("trips: %v", err)
	}
	if len(trips) != 1 {
		t.Fatalf("trips = %+v", trips)
	}
	trip := trips[0]
	if trip.StartPositionID != 2 || trip.EndPositionID != 5 {
		t.Errorf("endpoints = %d..%d, want 2..5", trip.StartPositionID, trip.EndPositionID)
	}
	if trip.DeviceID != 7 || trip.DeviceName != "van-7" {
		t.Errorf("device = %+v", trip)
	}
	if trip.Duration != int64(20*time.Minute/time.Millisecond) {
		t.Errorf("duration = %d", trip.Duration)
	}
	if trip.MaxSpeed != 30 {
		t.Errorf("maxSpeed = %v", trip.MaxSpeed)
	}
	if trip.Distance <= 0 {
		t.Errorf("distance = %v", trip.Distance)
	}
	if trip.StartTime.IsZero() || trip.EndTime.IsZero() {
		t.Errorf("times = %v..%v", trip.StartTime, trip.EndTime)
	}
}

func TestStopsReport(t *testing.T) {
	positions := []model.Position{
		movementPosition(1, 0, false, 0),
		movementPosition(2, 1, true, 10),
		movementPosition(3, 10, true, 30),
		movementPosition(4, 20, false, 0),
		movementPosition(5, 40, false, 0),
	}
	service := New(testStore(positions), Config{})

	stops, err := service.StopsReport(context.Background(), []int64{7}, at(-60), at(120))
	if err != nil {
		t.Fatalf("stops: %v", err)
	}
	if len(stops) != 2 {
		t.Fatalf("stops = %+v", stops)
	}
	// The initial stopped state counts as a stop until the device moved.
	if stops[0].PositionID != 1 || stops[0].EndTime != at(1) {
		t.Errorf("first stop = %+v", stops[0])
	}
	if stops[1].PositionID != 4 || stops[1].Duration != int64(20*time.Minute/time.Millisecond) {
		t.Errorf("second stop = %+v", stops[1])
	}
}

func TestSummaryReport(t *testing.T) {
	positions := []model.Position{
		movementPosition(1, 0, true, 10),
		movementPosition(2, 10, true, 40),
		movementPosition(3, 20, false, 0),
	}
	store := testStore(positions)
	service := New(store, Config{})

	items, err := service.SummaryReport(context.Background(), []int64{7}, at(-60), at(120), false)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].MaxSpeed != 40 || items[0].Distance <= 0 {
		t.Errorf("item = %+v", items[0])
	}
}

func TestSummaryReportDaily(t *testing.T) {
	positions := []model.Position{
		movementPosition(1, 0, true, 10),
		movementPosition(2, 20*60, true, 20), // next day, 08:00
	}
	service := New(testStore(positions), Config{})

	items, err := service.SummaryReport(context.Background(), []int64{7}, at(0), at(20*60), true)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("daily items = %+v", items)
	}
}

func TestEventsReportFilters(t *testing.T) {
	store := testStore(nil)
	store.events = map[int64][]model.Event{
		7: {
			{ID: 1, DeviceID: 7, Type: model.EventAlarm, Time: at(1),
				Attributes: model.Attributes{model.KeyAlarm: model.AlarmSOS}},
			{ID: 2, DeviceID: 7, Type: model.EventAlarm, Time: at(2),
				Attributes: model.Attributes{model.KeyAlarm: model.AlarmVibration}},
			{ID: 3, DeviceID: 7, Type: model.EventIgnitionOn, Time: at(3)},
		},
	}
	service := New(store, Config{})

	all, err := service.EventsReport(context.Background(), []int64{7}, at(0), at(10), nil, nil)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all events = %+v", all)
	}

	alarms, err := service.EventsReport(context.Background(), []int64{7}, at(0), at(10),
		[]string{model.EventAlarm}, []string{model.AlarmSOS})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(alarms) != 1 || alarms[0].ID != 1 {
		t.Fatalf("alarm events = %+v", alarms)
	}
}

func TestGeofencesReport(t *testing.T) {
	store := testStore(nil)
	store.events = map[int64][]model.Event{
		7: {
			{ID: 1, DeviceID: 7, Type: model.EventGeofenceEnter, GeofenceID: 5, Time: at(1)},
			{ID: 2, DeviceID: 7, Type: model.EventGeofenceEnter, GeofenceID: 6, Time: at(2)},
			{ID: 3, DeviceID: 7, Type: model.EventGeofenceExit, GeofenceID: 5, Time: at(10)},
		},
	}
	service := New(store, Config{})

	items, err := service.GeofencesReport(context.Background(), []int64{7}, []int64{5}, at(0), at(20))
	if err != nil {
		t.Fatalf("geofences: %v", err)
	}
	if len(items) != 1 || items[0].GeofenceID != 5 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].EndTime.Sub(items[0].StartTime) != 9*time.Minute {
		t.Errorf("interval = %v", items[0].EndTime.Sub(items[0].StartTime))
	}
}

func TestCombinedReport(t *testing.T) {
	positions := []model.Position{
		movementPosition(1, 0, true, 10),
		movementPosition(2, 10, true, 20),
	}
	store := testStore(positions)
	store.events = map[int64][]model.Event{
		7: {
			{ID: 1, DeviceID: 7, Type: model.EventDeviceMoving, PositionID: 1, Time: at(0)},
			{ID: 2, DeviceID: 7, Type: model.EventDeviceOverspeed, PositionID: 2, Time: at(10)},
		},
	}
	service := New(store, Config{})

	items, err := service.CombinedReport(context.Background(), []int64{7}, at(-60), at(60))
	if err != nil {
		t.Fatalf("combined: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	if len(items[0].Route) != 2 || items[0].Route[0][1] != 54.9 {
		t.Errorf("route = %v", items[0].Route)
	}
	if len(items[0].Events) != 1 || items[0].Events[0].Type != model.EventDeviceOverspeed {
		t.Errorf("events = %+v", items[0].Events)
	}
	if len(items[0].Positions) != 1 || items[0].Positions[0].ID != 2 {
		t.Errorf("positions = %+v", items[0].Positions)
	}
}

func TestTableExports(t *testing.T) {
	positions := []model.Position{movementPosition(1, 0, true, 10)}
	table := RouteTable(positions)

	var csvBuffer bytes.Buffer
	if err := WriteCSV(&csvBuffer, table); err != nil {
		t.Fatalf("csv: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(csvBuffer.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) != 2 || records[0][0] != "id" || records[1][0] != "1" {
		t.Fatalf("records = %v", records)
	}

	var gpxBuffer bytes.Buffer
	if err := WriteRouteGPX(&gpxBuffer, "route", positions); err != nil {
		t.Fatalf("gpx: %v", err)
	}
	if !strings.Contains(gpxBuffer.String(), "<trkpt") {
		t.Errorf("gpx = %s", gpxBuffer.String())
	}
	if err := xml.Unmarshal(gpxBuffer.Bytes(), new(any)); err != nil {
		t.Errorf("gpx is not XML: %v", err)
	}

	var kmlBuffer bytes.Buffer
	if err := WriteRouteKML(&kmlBuffer, positions); err != nil {
		t.Fatalf("kml: %v", err)
	}
	if !strings.Contains(kmlBuffer.String(), "<coordinates>") {
		t.Errorf("kml = %s", kmlBuffer.String())
	}

	var xlsxBuffer bytes.Buffer
	if err := WriteXLSX(&xlsxBuffer, []Sheet{{Name: "trips", Rows: tableRowsForTest(table)}}); err != nil {
		t.Fatalf("xlsx: %v", err)
	}
	archive, err := zip.NewReader(bytes.NewReader(xlsxBuffer.Bytes()), int64(xlsxBuffer.Len()))
	if err != nil {
		t.Fatalf("xlsx zip: %v", err)
	}
	names := make(map[string]bool)
	for _, file := range archive.File {
		names[file.Name] = true
	}
	for _, required := range []string{
		"[Content_Types].xml", "xl/workbook.xml", "xl/worksheets/sheet1.xml",
	} {
		if !names[required] {
			t.Errorf("missing %s", required)
		}
	}
}

func tableRowsForTest(table Table) [][]any {
	rows := [][]any{}
	header := make([]any, len(table.Header))
	for index, value := range table.Header {
		header[index] = value
	}
	rows = append(rows, header)
	for _, row := range table.Rows {
		cells := make([]any, len(row))
		for index, value := range row {
			cells[index] = value
		}
		rows = append(rows, cells)
	}
	return rows
}

func TestPeriodLimit(t *testing.T) {
	service := New(testStore(nil), Config{PeriodLimit: time.Hour})
	if err := service.ValidateRange(at(0), at(30)); err != nil {
		t.Fatalf("range inside the limit: %v", err)
	}
	if err := service.ValidateRange(at(0), at(120)); err != ErrPeriodTooLong {
		t.Fatalf("err = %v, want ErrPeriodTooLong", err)
	}
	if err := service.ValidateRange(at(10), at(0)); err == nil {
		t.Fatal("inverted range accepted")
	}
}
