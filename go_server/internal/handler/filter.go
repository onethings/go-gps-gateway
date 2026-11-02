package handler

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// Device attribute keys the filter stage reads. They mirror the Java Keys values,
// which is how AttributeUtil.lookup resolves them on a device row.
const (
	AttrFilterInvalid     = "filter.invalid"
	AttrFilterZero        = "filter.zero"
	AttrFilterDuplicate   = "filter.duplicate"
	AttrFilterOutdated    = "filter.outdated"
	AttrFilterFuture      = "filter.future"
	AttrFilterPast        = "filter.past"
	AttrFilterAccuracy    = "filter.accuracy"
	AttrFilterApproximate = "filter.approximate"
	AttrFilterStatic      = "filter.static"
	AttrFilterDistance    = "filter.distance"
	AttrFilterMaxSpeed    = "filter.maxSpeed"
	AttrFilterMinPeriod   = "filter.minPeriod"
	AttrFilterSkipLimit   = "filter.skipLimit"
	AttrFilterSkipAttr    = "filter.skipAttributes"
)

// DefaultFilterFuture is the Java default of filter.future in seconds: a
// position stamped more than a day ahead of the server clock is discarded.
const DefaultFilterFuture = 86400

// OutdatedHandler completes a position that carries no GPS fix of its own with
// the last known location, like the Java OutdatedHandler. A frame that only
// reports cell towers, battery level or an alarm therefore still yields a
// position that can be drawn and stored.
type OutdatedHandler struct {
	// Stage is the name of the stage it is installed in. The gateway puts it in
	// the distance slot, because the Java server completes an outdated position
	// before the distance and filter stages read its coordinates.
	Stage string
	// LastPosition returns the newest stored position of a device.
	LastPosition func(deviceID int64) *model.Position
}

// Name implements PositionHandler.
func (h OutdatedHandler) Name() string { return h.Stage }

// HandlePosition implements PositionHandler.
func (h OutdatedHandler) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	if position != nil && position.Outdated {
		h.fill(position)
	}
	return next(ctx, position)
}

// fill copies the location fields of the last position. The device time is left
// alone: unlike the Java server the model has no separate fix time, so the time
// the frame carried is the only one worth keeping.
func (h OutdatedHandler) fill(position *model.Position) {
	if h.LastPosition != nil {
		if last := h.LastPosition(position.DeviceID); last != nil {
			position.Valid = last.Valid
			position.Latitude = last.Latitude
			position.Longitude = last.Longitude
			position.Altitude = last.Altitude
			position.Speed = last.Speed
			position.Course = last.Course
			position.Accuracy = last.Accuracy
		}
	}
	if position.Time.IsZero() {
		position.Time = position.ServerTime
	}
}

// FilterHandler drops positions a device should not have produced: invalid or
// zero coordinates, duplicates, drift jumps, a fix from the future or the
// distant past, and the rest of the filter.* attributes. A dropped position is
// simply not handed on, which is how the Java FilterHandler stops the chain.
//
// Every filter is off unless the device row enables it, except filter.future,
// whose compiled-in default matches the Java one. The stage runs after the
// distance stage, like the Java server, so position.distance is already set.
type FilterHandler struct {
	// Stage is the stage name, StageFilter.
	Stage string
	// Attributes returns the device attribute snapshot. Nil leaves every filter
	// but filter.future disabled.
	Attributes func(deviceID int64) map[string]any
	// LastPosition returns the newest stored position of a device. The
	// duplicate, distance, speed and period filters compare against it.
	LastPosition func(deviceID int64) *model.Position
	// Now is the clock the future and past filters compare against. Nil uses
	// time.Now.
	Now func() time.Time
}

// Name implements PositionHandler.
func (h FilterHandler) Name() string { return h.Stage }

// HandlePosition implements PositionHandler.
func (h FilterHandler) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	if h.filtered(position) {
		return nil
	}
	return next(ctx, position)
}

// filtered reports whether the position must be dropped.
func (h FilterHandler) filtered(position *model.Position) bool {
	if position == nil {
		return true
	}
	deviceID := position.DeviceID

	if h.enabled(deviceID, AttrFilterInvalid) &&
		(!position.Valid || position.Latitude > 90 || position.Latitude < -90 ||
			position.Longitude > 180 || position.Longitude < -180) {
		return true
	}
	if h.enabled(deviceID, AttrFilterZero) && position.Latitude == 0 && position.Longitude == 0 {
		return true
	}
	if h.enabled(deviceID, AttrFilterOutdated) && position.Outdated {
		return true
	}

	now := h.now()
	if future := h.seconds(deviceID, AttrFilterFuture, DefaultFilterFuture); future > 0 &&
		position.Time.After(now.Add(time.Duration(future)*time.Second)) {
		return true
	}
	if past := h.seconds(deviceID, AttrFilterPast, 0); past > 0 &&
		position.Time.Before(now.Add(-time.Duration(past)*time.Second)) {
		return true
	}
	if accuracy := h.number(deviceID, AttrFilterAccuracy); accuracy > 0 && position.Accuracy > accuracy {
		return true
	}
	if h.enabled(deviceID, AttrFilterApproximate) && attributeFlag(position, model.KeyApprox) {
		return true
	}

	last := h.lastPosition(deviceID)
	if last == nil {
		return false
	}

	// An operator can let a position through even when a filter matched, which
	// is what filter.skipLimit and filter.skipAttributes are for.
	skip := h.skip(deviceID, position, last)
	if !skip {
		if h.enabled(deviceID, AttrFilterDuplicate) && duplicateOf(position, last) {
			return true
		}
		if h.enabled(deviceID, AttrFilterStatic) && position.Speed == 0 {
			return true
		}
		if limit := h.number(deviceID, AttrFilterDistance); limit > 0 &&
			distance(position.Latitude, position.Longitude, last.Latitude, last.Longitude) < limit {
			return true
		}
	}

	if maxSpeed := h.number(deviceID, AttrFilterMaxSpeed); maxSpeed > 0 {
		meters := distance(position.Latitude, position.Longitude, last.Latitude, last.Longitude)
		if elapsed := position.Time.Sub(last.Time).Seconds(); elapsed > 0 &&
			meters/elapsed/knotsToMps > maxSpeed {
			return true
		}
	}
	if period := h.number(deviceID, AttrFilterMinPeriod); period > 0 {
		if elapsed := position.Time.Sub(last.Time); elapsed > 0 &&
			elapsed < time.Duration(period)*time.Second {
			return true
		}
	}
	return false
}

// skip reports whether the device asked to bypass the excessive data filters,
// either because the previous position is old enough or because a tracked
// attribute changed.
func (h FilterHandler) skip(deviceID int64, position, last *model.Position) bool {
	serverTime := position.ServerTime
	if serverTime.IsZero() {
		// A live position has no server time until it is stored; use the clock
		// the rest of the handler already trusts.
		serverTime = h.now()
	}
	if limit := h.number(deviceID, AttrFilterSkipLimit); limit > 0 &&
		serverTime.Sub(last.ServerTime) > time.Duration(limit)*time.Second {
		return true
	}

	value, ok := h.attribute(deviceID, AttrFilterSkipAttr)
	if !ok {
		return false
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	for _, key := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ' '
	}) {
		if !position.HasAttribute(key) {
			continue
		}
		current, _ := position.Get(key)
		previous, _ := last.Get(key)
		if !reflect.DeepEqual(current, previous) {
			return true
		}
	}
	return false
}

func (h FilterHandler) lastPosition(deviceID int64) *model.Position {
	if h.LastPosition == nil {
		return nil
	}
	return h.LastPosition(deviceID)
}

func (h FilterHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// attribute reads one device attribute. ok is false when the device row does not
// set it, which is different from setting it to zero.
func (h FilterHandler) attribute(deviceID int64, key string) (any, bool) {
	if h.Attributes == nil {
		return nil, false
	}
	value, ok := h.Attributes(deviceID)[key]
	return value, ok
}

func (h FilterHandler) enabled(deviceID int64, key string) bool {
	value, ok := h.attribute(deviceID, key)
	return ok && truthy(value)
}

func (h FilterHandler) number(deviceID int64, key string) float64 {
	value, ok := h.attribute(deviceID, key)
	if !ok {
		return 0
	}
	number, ok := asFloat(value)
	if !ok {
		return 0
	}
	return number
}

// seconds reads a filter value expressed in seconds, falling back to the
// compiled-in default when the device does not set it.
func (h FilterHandler) seconds(deviceID int64, key string, fallback float64) float64 {
	if _, ok := h.attribute(deviceID, key); !ok {
		return fallback
	}
	return h.number(deviceID, key)
}

// duplicateOf reports whether two positions carry the same fix time and the new
// position introduces no attribute the previous one lacks.
func duplicateOf(position, last *model.Position) bool {
	if !position.Time.Equal(last.Time) {
		return false
	}
	for key := range position.Attributes {
		if !last.HasAttribute(key) {
			return false
		}
	}
	return true
}

// attributeFlag reads a boolean position attribute.
func attributeFlag(position *model.Position, key string) bool {
	value, ok := position.Get(key)
	return ok && truthy(value)
}

// truthy interprets the value an attribute holds after JSON decoding.
func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return err == nil && parsed
	default:
		number, ok := asFloat(value)
		return ok && number != 0
	}
}
