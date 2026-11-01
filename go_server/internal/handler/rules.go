package handler

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"

	"traccar-server_go/go_server/internal/eventctx"
	"traccar-server_go/go_server/internal/model"
)

// Device attribute keys the event rules read for a per-device threshold. They
// mirror the Java Keys values without the "event." prefix, which is how
// AttributeUtil.lookup resolves them.
const (
	AttrFuelIncreaseThreshold = "fuelIncreaseThreshold"
	AttrFuelDropThreshold     = "fuelDropThreshold"
	AttrAccelerationThreshold = "accelerationThreshold"
	AttrBrakingThreshold      = "brakingThreshold"
	AttrProximityEnter        = "proximityEnterDistance"
	AttrProximityExit         = "proximityExitDistance"
	AttrUnaccompanied         = "unaccompaniedDistance"
)

// mpsFromKnots matches UnitsConverter.mpsFromKnots.
const knotsToMps = 0.514444

// EventRule raises zero or more events for one position.
type EventRule interface {
	// Name labels the rule in logs and tests.
	Name() string
	// Events returns the events the position raised. A rule keeps its own
	// previous position, so the same position can be seen by several rules in
	// several stages without one consuming the history of another.
	Events(position *model.Position) []*model.Event
}

// RuleStage runs one or more rules at a stage of the chain.
type RuleStage struct {
	Stage string
	Rules []EventRule
}

// Name implements PositionHandler.
func (r RuleStage) Name() string { return r.Stage }

// HandlePosition implements PositionHandler. It never fails and never drops a
// position: a rule only adds events to the buffer the writer drains.
func (r RuleStage) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	for _, rule := range r.Rules {
		if rule == nil {
			continue
		}
		eventctx.Append(ctx, rule.Events(position)...)
	}
	return next(ctx, position)
}

// EventConfig holds the thresholds of the rules that have one. Zero disables a
// rule, which is what the Java server does with the same keys.
type EventConfig struct {
	IgnoreDuplicateAlarms  bool
	FuelIncreaseThreshold  float64
	FuelDropThreshold      float64
	AccelerationThreshold  float64
	BrakingThreshold       float64
	ProximityEnterDistance float64
	ProximityExitDistance  float64
	UnaccompaniedDistance  float64
}

// EventProviders supplies the per-device data the rules cannot derive from a
// position alone. Every field may be nil, which turns the rule that needs it
// off.
type EventProviders struct {
	// Attributes returns the device attribute snapshot, refreshed by the
	// command like the alert thresholds.
	Attributes func(deviceID int64) map[string]any
	// Maintenances returns the maintenance intervals linked to a device.
	Maintenances func(deviceID int64) []*model.Maintenance
	// LinkedDevices returns the devices linked to this one for a proximity
	// check.
	LinkedDevices func(deviceID int64) []int64
	// LastPosition returns the newest known position of a linked device.
	LastPosition func(deviceID int64) *model.Position
}

// attribute returns a device attribute, falling back to the compiled-in value.
func (p EventProviders) attribute(deviceID int64, key string, fallback float64) float64 {
	if p.Attributes == nil {
		return fallback
	}
	values := p.Attributes(deviceID)
	if values == nil {
		return fallback
	}
	attributes := model.Attributes(values)
	if typed, ok := asFloat(attributes[key]); ok {
		return typed
	}
	return fallback
}

// asFloat converts the numeric types an attribute may hold.
func asFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

// history is the previous position of a rule, per device.
type history struct {
	mu   sync.Mutex
	last map[int64]*model.Position
}

func (h *history) previous(deviceID int64) *model.Position {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last[deviceID]
}

// remember stores a copy of the position and returns the previous one.
func (h *history) remember(position *model.Position) *model.Position {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.last == nil {
		h.last = make(map[int64]*model.Position)
	}
	previous := h.last[position.DeviceID]
	copied := *position
	h.last[position.DeviceID] = &copied
	return previous
}

// event creates an event stamped like the alert engine does.
func event(eventType string, position *model.Position) *model.Event {
	event := &model.Event{
		DeviceID: position.DeviceID,
		Type:     eventType,
		Time:     position.Time,
	}
	if event.Time.IsZero() {
		event.Time = position.ServerTime
	}
	event.Set("protocol", position.Protocol)
	return event
}

// AlarmRule turns the alarm a device reported into an alarm event. With
// ignoreDuplicates a repeated alarm is only reported when it differs from the
// previous position's alarm.
type AlarmRule struct {
	IgnoreDuplicates bool
	history          history
}

// Name implements EventRule.
func (r *AlarmRule) Name() string { return "alarm" }

// Events implements EventRule.
func (r *AlarmRule) Events(position *model.Position) []*model.Event {
	alarms := position.Alarms
	if len(alarms) == 0 {
		raw := position.Attributes.GetString(model.KeyAlarm, "")
		for _, part := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				alarms = append(alarms, trimmed)
			}
		}
	}
	previous := r.history.remember(position)
	if len(alarms) == 0 {
		return nil
	}

	skip := map[string]struct{}{}
	if r.IgnoreDuplicates && previous != nil {
		for _, alarm := range alarmValues(previous) {
			skip[alarm] = struct{}{}
		}
	}

	events := make([]*model.Event, 0, len(alarms))
	for _, alarm := range alarms {
		if _, duplicate := skip[alarm]; duplicate {
			continue
		}
		detected := event(model.EventAlarm, position)
		detected.Set(model.KeyAlarm, alarm)
		events = append(events, detected)
	}
	return events
}

func alarmValues(position *model.Position) []string {
	if len(position.Alarms) > 0 {
		return position.Alarms
	}
	var values []string
	for _, part := range strings.Split(position.Attributes.GetString(model.KeyAlarm, ""), ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

// IgnitionRule raises ignitionOn / ignitionOff when the ignition attribute
// changes. It needs two positions that both carry the attribute, exactly like
// the Java handler.
type IgnitionRule struct {
	history history
}

// Name implements EventRule.
func (r *IgnitionRule) Name() string { return "ignition" }

// Events implements EventRule.
func (r *IgnitionRule) Events(position *model.Position) []*model.Event {
	previous := r.history.remember(position)
	if !position.Attributes.Has(model.KeyIgnition) || previous == nil ||
		!previous.Attributes.Has(model.KeyIgnition) {
		return nil
	}
	ignition := position.Attributes.GetBoolean(model.KeyIgnition)
	was := previous.Attributes.GetBoolean(model.KeyIgnition)
	switch {
	case ignition && !was:
		return []*model.Event{event(model.EventIgnitionOn, position)}
	case !ignition && was:
		return []*model.Event{event(model.EventIgnitionOff, position)}
	default:
		return nil
	}
}

// FuelRule raises deviceFuelIncrease / deviceFuelDrop when the fuel level
// changes by at least the configured threshold.
type FuelRule struct {
	Config    EventConfig
	Providers EventProviders
	history   history
}

// Name implements EventRule.
func (r *FuelRule) Name() string { return "fuel" }

// Events implements EventRule.
func (r *FuelRule) Events(position *model.Position) []*model.Event {
	previous := r.history.remember(position)
	if !position.Attributes.Has(model.KeyFuel) || previous == nil ||
		!previous.Attributes.Has(model.KeyFuel) {
		return nil
	}
	before := previous.Attributes.GetDouble(model.KeyFuel, 0)
	after := position.Attributes.GetDouble(model.KeyFuel, 0)
	change := after - before

	switch {
	case change > 0:
		threshold := r.Providers.attribute(position.DeviceID, AttrFuelIncreaseThreshold,
			r.Config.FuelIncreaseThreshold)
		if threshold > 0 && change >= threshold {
			detected := event(model.EventDeviceFuelIncrease, position)
			detected.Set("before", before)
			detected.Set("after", after)
			return []*model.Event{detected}
		}
	case change < 0:
		threshold := r.Providers.attribute(position.DeviceID, AttrFuelDropThreshold,
			r.Config.FuelDropThreshold)
		if threshold > 0 && math.Abs(change) >= threshold {
			detected := event(model.EventDeviceFuelDrop, position)
			detected.Set("before", before)
			detected.Set("after", after)
			return []*model.Event{detected}
		}
	}
	return nil
}

// BehaviorRule raises a hard acceleration or braking alarm from the speed
// change between two positions.
type BehaviorRule struct {
	Config    EventConfig
	Providers EventProviders
	history   history
}

// Name implements EventRule.
func (r *BehaviorRule) Name() string { return "behavior" }

// Events implements EventRule.
func (r *BehaviorRule) Events(position *model.Position) []*model.Event {
	previous := r.history.remember(position)
	if previous == nil || position.Time.Equal(previous.Time) {
		return nil
	}
	elapsed := position.Time.Sub(previous.Time).Seconds()
	if elapsed <= 0 {
		return nil
	}
	acceleration := (position.Speed - previous.Speed) * knotsToMps / elapsed

	if threshold := r.Providers.attribute(position.DeviceID, AttrAccelerationThreshold,
		r.Config.AccelerationThreshold); threshold != 0 && acceleration >= threshold {
		detected := event(model.EventAlarm, position)
		detected.Set(model.KeyAlarm, model.AlarmHardAcceleration)
		return []*model.Event{detected}
	}
	if threshold := r.Providers.attribute(position.DeviceID, AttrBrakingThreshold,
		r.Config.BrakingThreshold); threshold != 0 && acceleration <= -threshold {
		detected := event(model.EventAlarm, position)
		detected.Set(model.KeyAlarm, model.AlarmHardBraking)
		return []*model.Event{detected}
	}
	return nil
}

// DriverRule raises driverChanged when the driver unique id changes.
type DriverRule struct {
	history history
}

// Name implements EventRule.
func (r *DriverRule) Name() string { return "driver" }

// Events implements EventRule.
func (r *DriverRule) Events(position *model.Position) []*model.Event {
	driverID := position.Attributes.GetString(model.KeyDriverID, "")
	if driverID == "" {
		r.history.remember(position)
		return nil
	}
	previous := r.history.remember(position)
	was := ""
	if previous != nil {
		was = previous.Attributes.GetString(model.KeyDriverID, "")
	}
	if driverID == was {
		return nil
	}
	detected := event(model.EventDriverChanged, position)
	detected.Set(model.KeyDriverID, driverID)
	return []*model.Event{detected}
}

// MediaRule raises one media event per attached image, video or audio file.
type MediaRule struct{}

// Name implements EventRule.
func (r *MediaRule) Name() string { return "media" }

// Events implements EventRule.
func (r *MediaRule) Events(position *model.Position) []*model.Event {
	var events []*model.Event
	for _, media := range []string{model.KeyImage, model.KeyVideo, model.KeyAudio} {
		if !position.Attributes.Has(media) {
			continue
		}
		detected := event(model.EventMedia, position)
		detected.Set("media", media)
		detected.Set("file", position.Attributes.GetString(media, ""))
		events = append(events, detected)
	}
	return events
}

// CommandResultRule raises a commandResult event when a device reports the
// result of a command inside a position.
type CommandResultRule struct{}

// Name implements EventRule.
func (r *CommandResultRule) Name() string { return "commandResult" }

// Events implements EventRule.
func (r *CommandResultRule) Events(position *model.Position) []*model.Event {
	if !position.Attributes.Has(model.KeyResult) {
		return nil
	}
	detected := event(model.EventCommandResult, position)
	detected.Set(model.KeyResult, position.Attributes.GetString(model.KeyResult, ""))
	return []*model.Event{detected}
}

// MaintenanceRule raises a maintenance event when a service interval is
// crossed.
type MaintenanceRule struct {
	Maintenances func(deviceID int64) []*model.Maintenance
	history      history
}

// Name implements EventRule.
func (r *MaintenanceRule) Name() string { return "maintenance" }

// Events implements EventRule.
func (r *MaintenanceRule) Events(position *model.Position) []*model.Event {
	previous := r.history.remember(position)
	if previous == nil || r.Maintenances == nil {
		return nil
	}
	var events []*model.Event
	for _, maintenance := range r.Maintenances(position.DeviceID) {
		if maintenance == nil || maintenance.Period == 0 {
			continue
		}
		before := maintenanceValue(previous, maintenance.Type)
		after := maintenanceValue(position, maintenance.Type)
		if before == 0 || after == 0 || after < maintenance.Start {
			continue
		}
		if before < maintenance.Start ||
			int64((before-maintenance.Start)/maintenance.Period) <
				int64((after-maintenance.Start)/maintenance.Period) {
			detected := event(model.EventMaintenance, position)
			detected.Set("maintenanceId", maintenance.ID)
			detected.Set(maintenance.Type, after)
			events = append(events, detected)
		}
	}
	return events
}

// maintenanceValue reads the position value an interval is measured against.
func maintenanceValue(position *model.Position, maintenanceType string) float64 {
	switch maintenanceType {
	case "deviceTime", "fixTime":
		return float64(position.Time.UnixMilli())
	case "serverTime":
		return float64(position.ServerTime.UnixMilli())
	default:
		return position.Attributes.GetDouble(maintenanceType, 0)
	}
}

// ProximityRule raises proximityEnter / proximityExit for linked devices and an
// unaccompaniedMotion event when a device starts moving without a linked device
// nearby.
type ProximityRule struct {
	Config    EventConfig
	Providers EventProviders
	history   history
}

// Name implements EventRule.
func (r *ProximityRule) Name() string { return "proximity" }

// Events implements EventRule.
func (r *ProximityRule) Events(position *model.Position) []*model.Event {
	previous := r.history.remember(position)
	if previous == nil || r.Providers.LinkedDevices == nil || r.Providers.LastPosition == nil {
		return nil
	}

	enterDistance := r.Providers.attribute(position.DeviceID, AttrProximityEnter, r.Config.ProximityEnterDistance)
	exitDistance := r.Providers.attribute(position.DeviceID, AttrProximityExit, r.Config.ProximityExitDistance)
	unaccompanied := r.Providers.attribute(position.DeviceID, AttrUnaccompanied, r.Config.UnaccompaniedDistance)

	checkUnaccompanied := unaccompanied > 0 &&
		!previous.Attributes.GetBoolean(model.KeyMotion) &&
		position.Attributes.GetBoolean(model.KeyMotion)
	if enterDistance <= 0 && exitDistance <= 0 && !checkUnaccompanied {
		return nil
	}

	anyAccompanied := false
	var events []*model.Event
	for _, linkedID := range r.Providers.LinkedDevices(position.DeviceID) {
		linked := r.Providers.LastPosition(linkedID)
		if linked == nil {
			continue
		}
		distanceNew := distance(position.Latitude, position.Longitude, linked.Latitude, linked.Longitude)
		distanceOld := distance(previous.Latitude, previous.Longitude, linked.Latitude, linked.Longitude)

		switch {
		case enterDistance > 0 && distanceOld > enterDistance && distanceNew <= enterDistance:
			detected := event(model.EventProximityEnter, position)
			detected.Set("linkedDeviceId", linkedID)
			events = append(events, detected)
		case exitDistance > 0 && distanceOld <= exitDistance && distanceNew > exitDistance:
			detected := event(model.EventProximityExit, position)
			detected.Set("linkedDeviceId", linkedID)
			events = append(events, detected)
		}

		if unaccompanied > 0 && distanceNew <= unaccompanied {
			anyAccompanied = true
		}
	}

	if checkUnaccompanied && !anyAccompanied {
		events = append(events, event(model.EventUnaccompaniedMotion, position))
	}
	return events
}

// distance is the haversine on the equatorial radius, in metres.
func distance(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadius = 6378137.0
	const degToRad = math.Pi / 180

	deltaLon := (lon2 - lon1) * degToRad
	deltaLat := (lat2 - lat1) * degToRad
	a := math.Sin(deltaLat/2)*math.Sin(deltaLat/2) +
		math.Cos(lat1*degToRad)*math.Cos(lat2*degToRad)*
			math.Sin(deltaLon/2)*math.Sin(deltaLon/2)
	return earthRadius * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
