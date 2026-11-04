// Package report computes the historical reports the Java server exposes under
// /api/reports: route, trips, stops, summary, events, geofences, devices and
// combined, plus the CSV / GPX / KML / XLSX exports and the mail delivery.
package report

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
)

// ErrTooManyPositions is returned when a report would read more positions than
// report.maxPositions allows.
var ErrTooManyPositions = errors.New("report: position limit exceeded")

// ErrPeriodTooLong is returned when the requested range is longer than
// report.periodLimit.
var ErrPeriodTooLong = errors.New("report: time period exceeds the limit")

// Store is the persistence the reports read. *storage.Store implements it.
type Store interface {
	QueryPositions(ctx context.Context, filter model.PositionFilter) ([]model.Position, error)
	QueryEvents(ctx context.Context, filter model.EventFilter) ([]model.Event, error)
	Devices(ctx context.Context, filter model.DeviceFilter) ([]model.Device, error)
	LatestPositions(ctx context.Context, deviceIDs []int64) ([]model.Position, error)
	QueryGeofences(ctx context.Context, filter model.ResourceFilter) ([]*geofence.Geofence, error)
	Drivers(ctx context.Context, filter model.ResourceFilter) ([]*model.Driver, error)
}

// Config holds the report limits and the trip detection thresholds. The zero
// value of PeriodLimit disables the period limit, matching the Java default.
// MaxPositions is report.maxPositions: zero takes the DefaultConfig value and a
// negative value disables the cap.
type Config struct {
	MaxPositions int
	PeriodLimit  time.Duration
	// TripMinDistance is the shortest movement that counts as a trip, in
	// metres. TripMinDuration and TripParkingDuration are the time those states
	// must persist.
	TripMinDistance     float64
	TripMinDuration     time.Duration
	TripParkingDuration time.Duration
	TripNoDataDuration  time.Duration
	IgnoreOdometer      bool
}

// DefaultConfig is what the command uses when no report key is configured.
func DefaultConfig() Config {
	return Config{
		MaxPositions:        50000,
		TripMinDistance:     500,
		TripMinDuration:     300 * time.Second,
		TripParkingDuration: 300 * time.Second,
		TripNoDataDuration:  3600 * time.Second,
	}
}

// Service computes the reports for one request.
type Service struct {
	store Store
	cfg   Config
	now   func() time.Time
}

// New creates a service. Missing limits fall back to DefaultConfig.
func New(store Store, cfg Config) *Service {
	defaults := DefaultConfig()
	if cfg.MaxPositions == 0 {
		cfg.MaxPositions = defaults.MaxPositions
	}
	if cfg.TripMinDuration <= 0 {
		cfg.TripMinDuration = defaults.TripMinDuration
	}
	if cfg.TripParkingDuration <= 0 {
		cfg.TripParkingDuration = defaults.TripParkingDuration
	}
	if cfg.TripNoDataDuration <= 0 {
		cfg.TripNoDataDuration = defaults.TripNoDataDuration
	}
	if cfg.TripMinDistance <= 0 {
		cfg.TripMinDistance = defaults.TripMinDistance
	}
	return &Service{store: store, cfg: cfg, now: time.Now}
}

// WithClock replaces the clock, which tests use for daily summaries.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// ValidateRange rejects an inverted range and enforces the period limit.
func (s *Service) ValidateRange(from, to time.Time) error {
	if from.IsZero() || to.IsZero() {
		return errors.New("report: from and to are required")
	}
	if from.After(to) {
		return errors.New("report: from must not be after to")
	}
	if s.cfg.PeriodLimit > 0 && to.Sub(from) > s.cfg.PeriodLimit {
		return ErrPeriodTooLong
	}
	return nil
}

// Positions reads every position of a device between from and to, oldest first,
// paging around the storage limit of 1000 rows per query.
func (s *Service) Positions(ctx context.Context, deviceID int64, from, to time.Time) ([]model.Position, error) {
	const page = 1000
	var positions []model.Position
	for offset := 0; ; offset += page {
		batch, err := s.store.QueryPositions(ctx, model.PositionFilter{
			DeviceID:  deviceID,
			From:      from,
			To:        to,
			Ascending: true,
			Limit:     page,
			Offset:    offset,
		})
		if err != nil {
			return nil, err
		}
		positions = append(positions, batch...)
		if s.cfg.MaxPositions > 0 && len(positions) > s.cfg.MaxPositions {
			return nil, fmt.Errorf("%w: > %d", ErrTooManyPositions, s.cfg.MaxPositions)
		}
		if len(batch) < page {
			break
		}
	}
	return positions, nil
}

// Route returns the positions of every device in the range, oldest first.
func (s *Service) Route(ctx context.Context, deviceIDs []int64, from, to time.Time) ([]model.Position, error) {
	devices, err := s.store.Devices(ctx, model.DeviceFilter{DeviceIDs: deviceIDs})
	if err != nil {
		return nil, err
	}
	var route []model.Position
	for _, device := range devices {
		positions, err := s.Positions(ctx, device.ID, from, to)
		if err != nil {
			return nil, err
		}
		route = append(route, positions...)
	}
	return route, nil
}

// BaseItem is the part every report row shares.
type BaseItem struct {
	DeviceID      int64     `json:"deviceId"`
	DeviceName    string    `json:"deviceName,omitempty"`
	Distance      float64   `json:"distance"`
	AverageSpeed  float64   `json:"averageSpeed"`
	MaxSpeed      float64   `json:"maxSpeed"`
	SpentFuel     float64   `json:"spentFuel"`
	StartOdometer float64   `json:"startOdometer"`
	EndOdometer   float64   `json:"endOdometer"`
	StartTime     time.Time `json:"startTime"`
	EndTime       time.Time `json:"endTime"`
}

// TripItem is one trip or one stop row.
type TripItem struct {
	BaseItem
	Business        bool    `json:"business"`
	StartPositionID int64   `json:"startPositionId"`
	EndPositionID   int64   `json:"endPositionId"`
	StartLat        float64 `json:"startLat"`
	StartLon        float64 `json:"startLon"`
	EndLat          float64 `json:"endLat"`
	EndLon          float64 `json:"endLon"`
	StartAddress    string  `json:"startAddress,omitempty"`
	EndAddress      string  `json:"endAddress,omitempty"`
	Duration        int64   `json:"duration"`
	DriverUniqueID  string  `json:"driverUniqueId,omitempty"`
	DriverName      string  `json:"driverName,omitempty"`
}

// StopItem is one stop row.
type StopItem struct {
	BaseItem
	PositionID  int64   `json:"positionId"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Address     string  `json:"address,omitempty"`
	Duration    int64   `json:"duration"`
	EngineHours int64   `json:"engineHours"`
}

// SummaryItem is the per-device summary row.
type SummaryItem struct {
	BaseItem
	EngineHours int64 `json:"engineHours"`
	StartHours  int64 `json:"startHours"`
	EndHours    int64 `json:"endHours"`
}

// GeofenceItem is one interval a device spent inside a geofence.
type GeofenceItem struct {
	BaseItem
	GeofenceID int64 `json:"geofenceId"`
}

// EventItem is one row of the events report.
type EventItem struct {
	ID            int64          `json:"id"`
	DeviceID      int64          `json:"deviceId"`
	PositionID    int64          `json:"positionId,omitempty"`
	GeofenceID    int64          `json:"geofenceId,omitempty"`
	MaintenanceID int64          `json:"maintenanceId,omitempty"`
	Type          string         `json:"type"`
	EventTime     time.Time      `json:"eventTime"`
	Attributes    map[string]any `json:"attributes,omitempty"`
}

// DeviceInfo is the JSON shape of a device inside a report.
type DeviceInfo struct {
	ID         int64          `json:"id"`
	UniqueID   string         `json:"uniqueId"`
	Name       string         `json:"name"`
	Model      string         `json:"model,omitempty"`
	Protocol   string         `json:"protocol,omitempty"`
	GroupID    int64          `json:"groupId,omitempty"`
	Status     string         `json:"status,omitempty"`
	LastUpdate time.Time      `json:"lastUpdate,omitempty"`
	Attributes map[string]any `json:"attributes"`
}

// NewDeviceInfo renders one device.
func NewDeviceInfo(device model.Device) DeviceInfo {
	attributes := map[string]any(device.Attributes)
	if attributes == nil {
		attributes = map[string]any{}
	}
	return DeviceInfo{
		ID:         device.ID,
		UniqueID:   device.UniqueID,
		Name:       device.Name,
		Model:      device.Model,
		Protocol:   device.Protocol,
		GroupID:    device.GroupID,
		Status:     device.Status,
		LastUpdate: device.LastUpdate.UTC(),
		Attributes: attributes,
	}
}

// DeviceItem is one row of the devices report: the device and its newest
// position.
type DeviceItem struct {
	Device   DeviceInfo    `json:"device"`
	Position *PositionItem `json:"position,omitempty"`
}

// CombinedItem is one row of the combined report.
type CombinedItem struct {
	DeviceID  int64          `json:"deviceId"`
	Route     [][2]float64   `json:"route"`
	Events    []EventItem    `json:"events"`
	Positions []PositionItem `json:"positions"`
}

// PositionItem is the JSON shape of a position inside a report.
type PositionItem struct {
	ID         int64          `json:"id"`
	DeviceID   int64          `json:"deviceId"`
	Protocol   string         `json:"protocol"`
	DeviceTime time.Time      `json:"deviceTime"`
	ServerTime time.Time      `json:"serverTime,omitempty"`
	Valid      bool           `json:"valid"`
	Latitude   float64        `json:"latitude"`
	Longitude  float64        `json:"longitude"`
	Altitude   float64        `json:"altitude"`
	Speed      float64        `json:"speed"`
	Course     float64        `json:"course"`
	Accuracy   float64        `json:"accuracy"`
	Attributes map[string]any `json:"attributes"`
}

// NewPositionItem renders one position for a report response.
func NewPositionItem(position model.Position) PositionItem {
	attributes := position.Attributes
	if attributes == nil {
		attributes = map[string]any{}
	}
	return PositionItem{
		ID:         position.ID,
		DeviceID:   position.DeviceID,
		Protocol:   position.Protocol,
		DeviceTime: position.Time.UTC(),
		ServerTime: position.ServerTime.UTC(),
		Valid:      position.Valid,
		Latitude:   position.Latitude,
		Longitude:  position.Longitude,
		Altitude:   position.Altitude,
		Speed:      position.Speed,
		Course:     position.Course,
		Accuracy:   position.Accuracy,
		Attributes: attributes,
	}
}

// accessDevices lists the devices a report covers, in id order.
func (s *Service) accessDevices(ctx context.Context, deviceIDs []int64) ([]model.Device, error) {
	devices, err := s.store.Devices(ctx, model.DeviceFilter{DeviceIDs: deviceIDs})
	if err != nil {
		return nil, err
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	return devices, nil
}

// Events reads the events of a device in the range, oldest first, optionally
// filtered by type and alarm.
func (s *Service) Events(
	ctx context.Context, deviceID int64, from, to time.Time, types, alarms []string,
) ([]model.Event, error) {
	events, err := s.store.QueryEvents(ctx, model.EventFilter{
		DeviceID: deviceID, From: from, To: to, Limit: 1000,
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
	return filterEvents(events, types, alarms), nil
}

// allEvents is the wildcard value /api/reports/events uses.
const allEvents = "all"

// filterEvents applies the type and alarm filters. An empty type list or "all"
// keeps every type; the alarm filter narrows only the alarm event.
func filterEvents(events []model.Event, types, alarms []string) []model.Event {
	all := len(types) == 0
	for _, value := range types {
		if value == allEvents || value == "*" {
			all = true
		}
	}
	var wantedTypes map[string]struct{}
	if !all {
		wantedTypes = make(map[string]struct{}, len(types))
		for _, value := range types {
			wantedTypes[value] = struct{}{}
		}
	}
	var wantedAlarms map[string]struct{}
	if len(alarms) > 0 {
		wantedAlarms = make(map[string]struct{}, len(alarms))
		for _, value := range alarms {
			wantedAlarms[value] = struct{}{}
		}
	}

	kept := make([]model.Event, 0, len(events))
	for _, event := range events {
		if wantedTypes != nil {
			if _, ok := wantedTypes[event.Type]; !ok {
				continue
			}
		}
		if event.Type == model.EventAlarm && wantedAlarms != nil {
			alarm := event.Attributes.GetString(model.KeyAlarm, "")
			if _, ok := wantedAlarms[alarm]; !ok {
				continue
			}
		}
		kept = append(kept, event)
	}
	return kept
}

// eventItem converts one event for the JSON response.
func eventItem(event model.Event) EventItem {
	attributes := map[string]any(event.Attributes)
	if attributes == nil {
		attributes = map[string]any{}
	}
	item := EventItem{
		ID:         event.ID,
		DeviceID:   event.DeviceID,
		PositionID: event.PositionID,
		GeofenceID: event.GeofenceID,
		Type:       event.Type,
		EventTime:  event.Time.UTC(),
		Attributes: attributes,
	}
	item.MaintenanceID = event.Attributes.GetLong("maintenanceId", 0)
	return item
}

// EventsReport returns every matching event of every device.
func (s *Service) EventsReport(
	ctx context.Context, deviceIDs []int64, from, to time.Time, types, alarms []string,
) ([]EventItem, error) {
	devices, err := s.accessDevices(ctx, deviceIDs)
	if err != nil {
		return nil, err
	}
	var response []EventItem
	for _, device := range devices {
		events, err := s.Events(ctx, device.ID, from, to, types, alarms)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			response = append(response, eventItem(event))
		}
	}
	if response == nil {
		response = []EventItem{}
	}
	return response, nil
}

// GeofencesReport returns the intervals a device spent inside the requested
// geofences, oldest first. An empty geofence list reports nothing, which is what
// the Java provider does.
func (s *Service) GeofencesReport(
	ctx context.Context, deviceIDs, geofenceIDs []int64, from, to time.Time,
) ([]GeofenceItem, error) {
	wanted := make(map[int64]struct{}, len(geofenceIDs))
	for _, id := range geofenceIDs {
		wanted[id] = struct{}{}
	}
	if len(wanted) == 0 {
		return []GeofenceItem{}, nil
	}

	devices, err := s.accessDevices(ctx, deviceIDs)
	if err != nil {
		return nil, err
	}
	var response []GeofenceItem
	for _, device := range devices {
		events, err := s.store.QueryEvents(ctx, model.EventFilter{
			DeviceID: device.ID,
			From:     from,
			To:       to,
			Types:    []string{model.EventGeofenceEnter, model.EventGeofenceExit},
			Limit:    1000,
		})
		if err != nil {
			return nil, err
		}
		sort.Slice(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })

		open := make(map[int64]model.Event)
		for _, event := range events {
			if _, ok := wanted[event.GeofenceID]; !ok {
				continue
			}
			switch event.Type {
			case model.EventGeofenceEnter:
				open[event.GeofenceID] = event
			case model.EventGeofenceExit:
				enter, ok := open[event.GeofenceID]
				if !ok || event.Time.Before(enter.Time) {
					continue
				}
				delete(open, event.GeofenceID)
				response = append(response, GeofenceItem{
					BaseItem: BaseItem{
						DeviceID:   device.ID,
						DeviceName: device.Name,
						StartTime:  enter.Time.UTC(),
						EndTime:    event.Time.UTC(),
					},
					GeofenceID: event.GeofenceID,
				})
			}
		}
	}
	sort.Slice(response, func(i, j int) bool { return response[i].StartTime.Before(response[j].StartTime) })
	if response == nil {
		response = []GeofenceItem{}
	}
	return response, nil
}

// SummaryReport returns one row per device (or one row per day when daily is
// set).
func (s *Service) SummaryReport(
	ctx context.Context, deviceIDs []int64, from, to time.Time, daily bool,
) ([]SummaryItem, error) {
	devices, err := s.accessDevices(ctx, deviceIDs)
	if err != nil {
		return nil, err
	}
	var response []SummaryItem
	for _, device := range devices {
		if !daily {
			item, ok, err := s.summary(ctx, device, from, to)
			if err != nil {
				return nil, err
			}
			if ok {
				response = append(response, item)
			}
			continue
		}
		for cursor := from.UTC().Truncate(24 * time.Hour); cursor.Before(to); cursor = cursor.Add(24 * time.Hour) {
			end := cursor.Add(24 * time.Hour)
			if end.After(to) {
				end = to
			}
			if !end.After(cursor) {
				continue
			}
			item, ok, err := s.summary(ctx, device, cursor, end)
			if err != nil {
				return nil, err
			}
			if ok {
				response = append(response, item)
			}
		}
	}
	if response == nil {
		response = []SummaryItem{}
	}
	return response, nil
}

// summary computes the summary of one device for one range.
func (s *Service) summary(
	ctx context.Context, device model.Device, from, to time.Time,
) (SummaryItem, bool, error) {
	positions, err := s.Positions(ctx, device.ID, from, to)
	if err != nil {
		return SummaryItem{}, false, err
	}
	if len(positions) == 0 {
		return SummaryItem{}, false, nil
	}
	first, last := positions[0], positions[len(positions)-1]

	item := SummaryItem{BaseItem: BaseItem{
		DeviceID:   device.ID,
		DeviceName: device.Name,
		StartTime:  first.Time.UTC(),
		EndTime:    last.Time.UTC(),
	}}
	for _, position := range positions {
		if position.Speed > item.MaxSpeed {
			item.MaxSpeed = position.Speed
		}
	}
	item.Distance = s.distance(positions, 0, len(positions)-1)
	item.StartOdometer, item.EndOdometer = s.odometers(positions, 0, len(positions)-1)

	if first.Attributes.Has(model.KeyHours) && last.Attributes.Has(model.KeyHours) {
		item.StartHours = first.Attributes.GetLong(model.KeyHours, 0)
		item.EndHours = last.Attributes.GetLong(model.KeyHours, 0)
		item.EngineHours = item.EndHours - item.StartHours
		if item.EngineHours > 0 {
			item.AverageSpeed = knotsFromMps(item.Distance * 1000 / float64(item.EngineHours))
		}
	}
	return item, true, nil
}

// DevicesReport returns every device with its newest position.
func (s *Service) DevicesReport(ctx context.Context, deviceIDs []int64) ([]DeviceItem, error) {
	devices, err := s.accessDevices(ctx, deviceIDs)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(devices))
	for _, device := range devices {
		ids = append(ids, device.ID)
	}
	latest, err := s.store.LatestPositions(ctx, ids)
	if err != nil {
		return nil, err
	}
	byDevice := make(map[int64]model.Position, len(latest))
	for _, position := range latest {
		byDevice[position.DeviceID] = position
	}

	response := make([]DeviceItem, 0, len(devices))
	for _, device := range devices {
		item := DeviceItem{Device: NewDeviceInfo(device)}
		if position, ok := byDevice[device.ID]; ok {
			rendered := NewPositionItem(position)
			item.Position = &rendered
		}
		response = append(response, item)
	}
	return response, nil
}

// CombinedReport returns the route, the events and the positions that caused
// them for every device, which is what the combined report page draws.
func (s *Service) CombinedReport(
	ctx context.Context, deviceIDs []int64, from, to time.Time,
) ([]CombinedItem, error) {
	devices, err := s.accessDevices(ctx, deviceIDs)
	if err != nil {
		return nil, err
	}
	response := make([]CombinedItem, 0, len(devices))
	for _, device := range devices {
		positions, err := s.Positions(ctx, device.ID, from, to)
		if err != nil {
			return nil, err
		}
		events, err := s.store.QueryEvents(ctx, model.EventFilter{
			DeviceID: device.ID,
			From:     from,
			To:       to,
			Limit:    1000,
		})
		if err != nil {
			return nil, err
		}
		sort.Slice(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })

		item := CombinedItem{
			DeviceID:  device.ID,
			Route:     make([][2]float64, 0, len(positions)),
			Events:    make([]EventItem, 0, len(events)),
			Positions: make([]PositionItem, 0, len(events)),
		}
		byID := make(map[int64]model.Position, len(positions))
		for _, position := range positions {
			item.Route = append(item.Route, [2]float64{position.Longitude, position.Latitude})
			byID[position.ID] = position
		}
		for _, event := range events {
			// deviceMoving is implied by the route itself and is left out, the
			// same way the Java provider does.
			if event.PositionID == 0 || event.Type == model.EventDeviceMoving {
				continue
			}
			item.Events = append(item.Events, eventItem(event))
		}
		for _, event := range item.Events {
			if position, ok := byID[event.PositionID]; ok {
				item.Positions = append(item.Positions, NewPositionItem(position))
			}
		}
		response = append(response, item)
	}
	return response, nil
}

// distance returns the distance between two indexes of a position slice: the
// odometer or total distance delta when the device reports one, and the sum of
// the haversine legs otherwise. The fallback is what makes the reports work for
// a device that has no distance attribute.
func (s *Service) distance(positions []model.Position, start, end int) float64 {
	if start == end {
		return 0
	}
	if s.cfg.IgnoreOdometer {
		if value, ok := totalDistanceDelta(positions[start], positions[end]); ok {
			return value
		}
	} else {
		if value, ok := attributeDelta(positions[start], positions[end], model.KeyOdometer); ok {
			return value
		}
		if value, ok := totalDistanceDelta(positions[start], positions[end]); ok {
			return value
		}
	}
	total := 0.0
	previous := positions[start]
	for index := start + 1; index <= end; index++ {
		if positions[index].Valid && previous.Valid {
			total += haversine(previous.Latitude, previous.Longitude,
				positions[index].Latitude, positions[index].Longitude)
		}
		previous = positions[index]
	}
	return total
}

func attributeDelta(first, last model.Position, key string) (float64, bool) {
	from := first.Attributes.GetDouble(key, 0)
	to := last.Attributes.GetDouble(key, 0)
	if from == 0 || to == 0 || to < from {
		return 0, false
	}
	return to - from, true
}

func totalDistanceDelta(first, last model.Position) (float64, bool) {
	return attributeDelta(first, last, model.KeyTotalDist)
}

// odometers returns the start and end odometer of a range, falling back to the
// accumulated total distance, which is what the Java report shows.
func (s *Service) odometers(positions []model.Position, start, end int) (float64, float64) {
	if !s.cfg.IgnoreOdometer {
		from := positions[start].Attributes.GetDouble(model.KeyOdometer, 0)
		to := positions[end].Attributes.GetDouble(model.KeyOdometer, 0)
		if from != 0 && to != 0 {
			return from, to
		}
	}
	return positions[start].Attributes.GetDouble(model.KeyTotalDist, 0),
		positions[end].Attributes.GetDouble(model.KeyTotalDist, 0)
}

// knotsFromMps matches UnitsConverter.knotsFromMps.
func knotsFromMps(value float64) float64 {
	return value / 0.514444
}

// haversine is the distance between two coordinates in metres.
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadius = 6378137.0
	const degToRad = math.Pi / 180

	deltaLon := (lon2 - lon1) * degToRad
	deltaLat := (lat2 - lat1) * degToRad
	a := math.Sin(deltaLat/2)*math.Sin(deltaLat/2) +
		math.Cos(lat1*degToRad)*math.Cos(lat2*degToRad)*
			math.Sin(deltaLon/2)*math.Sin(deltaLon/2)
	return earthRadius * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// driverName resolves the driver of a trip. It is looked up once per unique id.
func (s *Service) driverNames(ctx context.Context) (map[string]string, error) {
	drivers, err := s.store.Drivers(ctx, model.ResourceFilter{Limit: 1000})
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(drivers))
	for _, driver := range drivers {
		if driver != nil && driver.UniqueID != "" {
			names[driver.UniqueID] = driver.Name
		}
	}
	return names, nil
}

// addressOf reads the address the geocoder stored on the position.
func addressOf(position model.Position) string {
	return strings.TrimSpace(position.Attributes.GetString("address", ""))
}
