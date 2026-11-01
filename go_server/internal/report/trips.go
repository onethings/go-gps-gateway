package report

import (
	"context"
	"sort"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// motionState is MotionState: the streak, the position and time the current
// state started, and the distance at that moment.
type motionState struct {
	motionState      bool
	motionStreak     bool
	motionPositionID int64
	motionTime       time.Time
	motionDistance   float64
}

// motionEvent is the event MotionProcessor raises when a state lasted long
// enough to count.
type motionEvent struct {
	eventType  string
	positionID int64
	at         time.Time
}

// updateMotion is MotionProcessor.updateState: a no-data gap closes a moving
// state, and a state only becomes an event after the configured duration,
// distance or ignition change.
func (s *Service) updateMotion(
	state *motionState, last, position *model.Position, moving bool,
) *motionEvent {
	if last != nil {
		gap := position.Time.Sub(last.Time)
		if gap >= s.cfg.TripNoDataDuration && state.motionStreak {
			state.motionStreak = false
			state.motionState = false
			state.motionPositionID = 0
			state.motionTime = time.Time{}
			state.motionDistance = 0
			return &motionEvent{eventType: model.EventDeviceStopped, positionID: last.ID, at: last.Time}
		}
	}

	oldState := state.motionState
	if oldState == moving {
		if state.motionTime.IsZero() {
			return nil
		}
		elapsed := position.Time.Sub(state.motionTime)
		totalDistance := position.Attributes.GetDouble(model.KeyTotalDist, 0)
		distance := totalDistance - state.motionDistance
		ignition := false
		hasIgnition := false
		if moving && position.Attributes.Has(model.KeyIgnition) {
			ignition = position.Attributes.GetBoolean(model.KeyIgnition)
			hasIgnition = true
		}

		generate := false
		if moving {
			if elapsed >= s.cfg.TripMinDuration || distance >= s.cfg.TripMinDistance {
				generate = true
			}
		} else {
			if elapsed >= s.cfg.TripParkingDuration || (hasIgnition && !ignition) {
				generate = true
			}
		}
		if !generate {
			return nil
		}

		eventType := model.EventDeviceStopped
		if moving {
			eventType = model.EventDeviceMoving
		}
		event := &motionEvent{
			eventType:  eventType,
			positionID: state.motionPositionID,
			at:         state.motionTime,
		}
		state.motionStreak = moving
		state.motionPositionID = 0
		state.motionTime = time.Time{}
		state.motionDistance = 0
		return event
	}

	state.motionState = moving
	if state.motionStreak == moving {
		state.motionPositionID = 0
		state.motionTime = time.Time{}
		state.motionDistance = 0
	} else {
		state.motionPositionID = position.ID
		state.motionTime = position.Time
		state.motionDistance = position.Attributes.GetDouble(model.KeyTotalDist, 0)
	}
	return nil
}

// segment is one detected stretch of movement or rest with its endpoints.
type segment struct {
	start    int
	end      int
	maxSpeed float64
}

// detectSegments runs the motion state machine over the positions and returns
// the ranges that count as trips (wantMoving) or stops.
func (s *Service) detectSegments(
	ctx context.Context, deviceID int64, from, to time.Time, wantMoving bool,
) ([]model.Position, []segment, error) {
	positions, err := s.Positions(ctx, deviceID, from, to)
	if err != nil {
		return nil, nil, err
	}
	if len(positions) < 2 {
		return positions, nil, nil
	}

	index := make(map[int64]int, len(positions))
	for positionIndex, position := range positions {
		index[position.ID] = positionIndex
	}

	state := motionState{}
	start := -1
	var events []motionEvent
	var last *model.Position

	for positionIndex := range positions {
		position := &positions[positionIndex]
		moving := position.Attributes.GetBoolean(model.KeyMotion)
		if last == nil {
			if moving == wantMoving {
				start = positionIndex
			}
			state.motionStreak = moving
			state.motionState = moving
		}
		if event := s.updateMotion(&state, last, position, moving); event != nil {
			events = append(events, *event)
		}
		last = position
	}

	var segments []segment
	for _, event := range events {
		moving := event.eventType == model.EventDeviceMoving
		eventIndex, known := index[event.positionID]
		if !known {
			continue
		}
		if moving == wantMoving {
			start = eventIndex
			continue
		}
		if start >= 0 {
			segments = append(segments, segment{
				start: start, end: eventIndex, maxSpeed: maxSpeedBetween(positions, start, eventIndex),
			})
		}
		start = -1
	}
	if start >= 0 {
		segments = append(segments, segment{
			start: start, end: len(positions) - 1,
			maxSpeed: maxSpeedBetween(positions, start, len(positions)-1),
		})
	}
	return positions, segments, nil
}

// maxSpeedBetween returns the highest speed reported inside a segment. The
// Java server attaches the running maximum to the event that ends the state,
// which reports the stop's speed for a trip ending; the segment maximum is what
// an operator reads as the trip maximum.
func maxSpeedBetween(positions []model.Position, start, end int) float64 {
	maxSpeed := 0.0
	for index := start; index <= end && index < len(positions); index++ {
		if positions[index].Speed > maxSpeed {
			maxSpeed = positions[index].Speed
		}
	}
	return maxSpeed
}

// TripsReport returns the trips of every device, oldest first.
func (s *Service) TripsReport(
	ctx context.Context, deviceIDs []int64, from, to time.Time,
) ([]TripItem, error) {
	devices, err := s.accessDevices(ctx, deviceIDs)
	if err != nil {
		return nil, err
	}
	drivers, err := s.driverNames(ctx)
	if err != nil {
		return nil, err
	}

	response := make([]TripItem, 0)
	for _, device := range devices {
		positions, segments, err := s.detectSegments(ctx, device.ID, from, to, true)
		if err != nil {
			return nil, err
		}
		for _, detected := range segments {
			response = append(response, s.tripItem(device, positions, detected, drivers))
		}
	}
	sort.Slice(response, func(i, j int) bool { return response[i].StartTime.Before(response[j].StartTime) })
	return response, nil
}

// StopsReport returns the stops of every device, oldest first.
func (s *Service) StopsReport(
	ctx context.Context, deviceIDs []int64, from, to time.Time,
) ([]StopItem, error) {
	devices, err := s.accessDevices(ctx, deviceIDs)
	if err != nil {
		return nil, err
	}
	response := make([]StopItem, 0)
	for _, device := range devices {
		positions, segments, err := s.detectSegments(ctx, device.ID, from, to, false)
		if err != nil {
			return nil, err
		}
		for _, detected := range segments {
			response = append(response, s.stopItem(device, positions, detected))
		}
	}
	sort.Slice(response, func(i, j int) bool { return response[i].StartTime.Before(response[j].StartTime) })
	return response, nil
}

// tripItem renders one trip segment.
func (s *Service) tripItem(
	device model.Device, positions []model.Position, detected segment, drivers map[string]string,
) TripItem {
	start, end := positions[detected.start], positions[detected.end]
	item := TripItem{
		BaseItem:        s.baseItem(device, positions, detected, true),
		StartPositionID: start.ID,
		EndPositionID:   end.ID,
		StartLat:        start.Latitude,
		StartLon:        start.Longitude,
		EndLat:          end.Latitude,
		EndLon:          end.Longitude,
		StartAddress:    addressOf(start),
		EndAddress:      addressOf(end),
		Duration:        end.Time.Sub(start.Time).Milliseconds(),
	}
	item.Business = start.Attributes.GetBoolean(model.KeyBusiness)
	if driverID := start.Attributes.GetString(model.KeyDriverID, ""); driverID != "" {
		item.DriverUniqueID = driverID
		item.DriverName = drivers[driverID]
	}
	return item
}

// stopItem renders one stop segment.
func (s *Service) stopItem(
	device model.Device, positions []model.Position, detected segment,
) StopItem {
	start, end := positions[detected.start], positions[detected.end]
	item := StopItem{
		BaseItem:   s.baseItem(device, positions, detected, false),
		PositionID: start.ID,
		Latitude:   start.Latitude,
		Longitude:  start.Longitude,
		Address:    addressOf(start),
		Duration:   end.Time.Sub(start.Time).Milliseconds(),
	}
	if start.Attributes.Has(model.KeyHours) && end.Attributes.Has(model.KeyHours) {
		item.EngineHours = end.Attributes.GetLong(model.KeyHours, 0) -
			start.Attributes.GetLong(model.KeyHours, 0)
	}
	return item
}

// baseItem fills the shared fields of a trip or a stop.
func (s *Service) baseItem(
	device model.Device, positions []model.Position, detected segment, moving bool,
) BaseItem {
	start, end := positions[detected.start], positions[detected.end]
	item := BaseItem{
		DeviceID:   device.ID,
		DeviceName: device.Name,
		Distance:   s.distance(positions, detected.start, detected.end),
		MaxSpeed:   detected.maxSpeed,
		SpentFuel:  s.spentFuel(device, positions, detected.start, detected.end),
		StartTime:  start.Time.UTC(),
		EndTime:    end.Time.UTC(),
	}
	item.StartOdometer, item.EndOdometer = s.odometers(positions, detected.start, detected.end)
	if moving {
		seconds := end.Time.Sub(start.Time).Seconds()
		if seconds > 0 {
			item.AverageSpeed = knotsFromMps(item.Distance / seconds)
		}
	}
	return item
}

// spentFuel sums what the device reported between two indexes. The fuel event
// history is not consulted, so a vehicle whose tank was refilled mid-range
// reports a smaller value than the Java server; the position attributes are the
// source here.
func (s *Service) spentFuel(
	device model.Device, positions []model.Position, start, end int,
) float64 {
	first, last := positions[start], positions[end]
	if first.Attributes.Has(model.KeyFuelUsed) && last.Attributes.Has(model.KeyFuelUsed) {
		return last.Attributes.GetDouble(model.KeyFuelUsed, 0) -
			first.Attributes.GetDouble(model.KeyFuelUsed, 0)
	}
	if first.Attributes.Has(model.KeyFuel) && last.Attributes.Has(model.KeyFuel) {
		return first.Attributes.GetDouble(model.KeyFuel, 0) - last.Attributes.GetDouble(model.KeyFuel, 0)
	}
	capacity := device.Attributes.GetDouble("fuelCapacity", 0)
	if capacity > 0 && first.Attributes.Has(model.KeyFuelLevel) && last.Attributes.Has(model.KeyFuelLevel) {
		delta := first.Attributes.GetDouble(model.KeyFuelLevel, 0) -
			last.Attributes.GetDouble(model.KeyFuelLevel, 0)
		return delta / 100 * capacity
	}
	return 0
}
