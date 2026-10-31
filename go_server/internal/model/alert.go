package model

import "time"

// Alert type values. They reuse the strings of Event.TYPE_* so that reports
// and clients written for the Java server keep working.
const (
	AlertOverspeed     = "deviceOverspeed"
	AlertGeofenceEnter = "geofenceEnter"
	AlertGeofenceExit  = "geofenceExit"
	AlertPowerCut      = "powerCut"
)

// Alert is one row of the alerts table: a rule verdict attached to a device at
// a point in time.
type Alert struct {
	ID       int64
	DeviceID int64
	// PositionID stays zero until positions are inserted with RETURNING; the
	// query side joins on device id and time until then.
	PositionID int64
	Type       string
	Time       time.Time
	GeofenceID int64
	Attributes map[string]any
}

// Set stores an attribute, ignoring nil values.
func (a *Alert) Set(key string, value any) {
	if value == nil {
		return
	}
	if a.Attributes == nil {
		a.Attributes = make(map[string]any)
	}
	a.Attributes[key] = value
}

// AlertFilter selects alerts for a query. Zero values mean "no restriction";
// Limit and Offset are clamped by the storage layer.
type AlertFilter struct {
	DeviceID int64
	Types    []string
	From     time.Time
	To       time.Time
	Limit    int
	Offset   int
}
