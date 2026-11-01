package model

import (
	"sort"
	"time"
)

// Event type values. They match the Java server's Event.TYPE_* strings, so
// reports and clients written for it keep working.
const (
	EventDeviceOnline    = "deviceOnline"
	EventDeviceOffline   = "deviceOffline"
	EventDeviceMoving    = "deviceMoving"
	EventDeviceStopped   = "deviceStopped"
	EventDeviceOverspeed = "deviceOverspeed"
	EventGeofenceEnter   = "geofenceEnter"
	EventGeofenceExit    = "geofenceExit"
	EventPowerCut        = "powerCut"
	EventPowerRestored   = "powerRestored"
)

// EventTypes returns every event type the gateway itself can raise, sorted. It
// backs /api/notifications/types and the subscription validation.
func EventTypes() []string {
	types := []string{
		EventDeviceOnline, EventDeviceOffline, EventDeviceMoving, EventDeviceStopped,
		EventDeviceOverspeed, EventGeofenceEnter, EventGeofenceExit,
		EventPowerCut, EventPowerRestored,
	}
	sort.Strings(types)
	return types
}

// IsEventType reports whether value is one of the types the gateway raises
// itself. Device reported alarms arrive as their own strings, so a value that is
// not listed here is not necessarily wrong.
func IsEventType(value string) bool {
	switch value {
	case EventDeviceOnline, EventDeviceOffline, EventDeviceMoving, EventDeviceStopped,
		EventDeviceOverspeed, EventGeofenceEnter, EventGeofenceExit,
		EventPowerCut, EventPowerRestored:
		return true
	default:
		return false
	}
}

// Event is one row of the events table: something that happened to a device at
// a point in time. The position that caused it is linked when the gateway had
// one, which is what the Java server keeps in tc_events.positionId.
type Event struct {
	ID       int64
	DeviceID int64
	// PositionID stays zero for events that happen between positions, such as
	// a device going online.
	PositionID int64
	GeofenceID int64
	Type       string
	Time       time.Time
	Attributes Attributes
}

// Set stores an attribute, ignoring nil values.
func (e *Event) Set(key string, value any) {
	if value == nil {
		return
	}
	if e.Attributes == nil {
		e.Attributes = Attributes{}
	}
	e.Attributes.Set(key, value)
}

// NewEvent creates an event of the given type for a device.
func NewEvent(eventType string, deviceID int64, at time.Time) *Event {
	return &Event{Type: eventType, DeviceID: deviceID, Time: at}
}

// EventFilter selects events for a query. Zero values mean "no restriction";
// Limit and Offset are clamped by the storage layer.
type EventFilter struct {
	// ID selects one event by primary key, which backs GET /api/events/{id}.
	ID       int64
	DeviceID int64
	// DeviceIDs restricts the result to a set of devices. Nil means "no
	// restriction"; a non-nil empty slice matches nothing, which is how a
	// token without device grants stays isolated.
	DeviceIDs []int64
	Types     []string
	From      time.Time
	To        time.Time
	Limit     int
	Offset    int
}
