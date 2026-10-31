// Package alert evaluates decoded positions against the alert rules and
// buffers the resulting rows so that device connections never block on the
// database. The rule set mirrors the Java server handlers: overspeed,
// power cut and geofence enter / exit.
package alert

import (
	"sync"
	"time"

	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
)

// Config holds the rule thresholds. A zero threshold disables the rule.
type Config struct {
	// OverspeedKnots is compared against Position.Speed, which every decoder
	// already converts to knots.
	OverspeedKnots float64
	// PowerCutVolts is compared against the "power" attribute, which the
	// protocols report in volts (GT06 0x94 sends 12.5, not 12500).
	PowerCutVolts float64
}

// Engine evaluates positions one at a time and remembers the geofence
// membership needed to detect enter / exit transitions.
type Engine struct {
	config Config
	now    func() time.Time

	mu        sync.RWMutex
	geofences []*geofence.Geofence
	members   map[membership]bool
}

type membership struct {
	deviceID   int64
	geofenceID int64
}

// New creates an engine with the given thresholds and geofences.
func New(config Config, geofences []*geofence.Geofence) *Engine {
	engine := &Engine{config: config, now: time.Now, members: make(map[membership]bool)}
	engine.SetGeofences(geofences)
	return engine
}

// SetGeofences replaces the geofence set, dropping the membership state of
// geofences that no longer exist.
func (e *Engine) SetGeofences(geofences []*geofence.Geofence) {
	known := make(map[int64]struct{}, len(geofences))
	for _, fence := range geofences {
		if fence != nil && fence.Area != nil {
			known[fence.ID] = struct{}{}
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.geofences = append(e.geofences[:0], geofences...)
	for key := range e.members {
		if _, ok := known[key.geofenceID]; !ok {
			delete(e.members, key)
		}
	}
}

// Geofences returns the current geofence set.
func (e *Engine) Geofences() []*geofence.Geofence {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]*geofence.Geofence(nil), e.geofences...)
}

// Evaluate returns the alerts raised by one position. It never blocks and
// never touches the database.
func (e *Engine) Evaluate(position *model.Position) []*model.Alert {
	if position == nil || position.DeviceID == 0 {
		return nil
	}
	if e.idle() {
		return nil
	}

	alerts := make([]*model.Alert, 0, 2)
	alerts = append(alerts, e.overspeed(position)...)
	alerts = append(alerts, e.powerCut(position)...)
	alerts = append(alerts, e.geofenceTransitions(position)...)
	return alerts
}

// idle reports whether no rule can fire, which keeps the per position
// allocation out of the way when alerts are not configured.
func (e *Engine) idle() bool {
	if e.config.OverspeedKnots > 0 || e.config.PowerCutVolts > 0 {
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.geofences) == 0
}

func (e *Engine) overspeed(position *model.Position) []*model.Alert {
	limit := e.config.OverspeedKnots
	if limit <= 0 || !position.Valid || position.Speed <= limit {
		return nil
	}

	alert := e.newAlert(model.AlertOverspeed, position)
	alert.Set("speed", position.Speed)
	alert.Set("limit", limit)
	return []*model.Alert{alert}
}

func (e *Engine) powerCut(position *model.Position) []*model.Alert {
	threshold := e.config.PowerCutVolts
	if threshold <= 0 {
		return nil
	}

	value, ok := position.Get(model.KeyPower)
	if !ok {
		return nil
	}
	power, ok := asFloat(value)
	if !ok || power >= threshold {
		return nil
	}

	// The position that reports the cut is still the position to attach the
	// alert to, even when it carries no GPS fix.
	alert := e.newAlert(model.AlertPowerCut, position)
	alert.Set("power", power)
	alert.Set("threshold", threshold)
	return []*model.Alert{alert}
}

func (e *Engine) geofenceTransitions(position *model.Position) []*model.Alert {
	if !position.IsLocated() {
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if len(e.geofences) == 0 {
		return nil
	}

	var alerts []*model.Alert
	for _, fence := range e.geofences {
		if fence == nil || fence.Area == nil {
			continue
		}

		key := membership{deviceID: position.DeviceID, geofenceID: fence.ID}
		inside := fence.Contains(position)
		was, known := e.members[key]
		if !known {
			// First sighting only seeds the state, matching the Java handler
			// which needs a previous position to compare with.
			e.members[key] = inside
			continue
		}
		if was == inside {
			continue
		}
		e.members[key] = inside

		alertType := model.AlertGeofenceExit
		if inside {
			alertType = model.AlertGeofenceEnter
		}
		alert := e.newAlert(alertType, position)
		alert.GeofenceID = fence.ID
		alert.Set("geofence", fence.Name)
		alerts = append(alerts, alert)
	}
	return alerts
}

// newAlert snapshots the coordinates into the attributes: the alerts table has
// no position id yet, so a query alone has to be self contained.
func (e *Engine) newAlert(alertType string, position *model.Position) *model.Alert {
	eventTime := position.Time
	if eventTime.IsZero() {
		eventTime = e.now()
	}

	alert := &model.Alert{
		DeviceID: position.DeviceID,
		Type:     alertType,
		Time:     eventTime,
	}
	alert.Set("protocol", position.Protocol)
	if position.IsLocated() {
		alert.Set("latitude", position.Latitude)
		alert.Set("longitude", position.Longitude)
		alert.Set("altitude", position.Altitude)
	}
	alert.Set("speed", position.Speed)
	return alert
}

// asFloat converts the numeric attribute types the decoders produce.
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
	default:
		return 0, false
	}
}
