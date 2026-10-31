package alert

import (
	"testing"
	"time"

	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
)

func mustArea(t *testing.T, wkt string) geofence.Area {
	t.Helper()
	area, err := geofence.Parse(wkt)
	if err != nil {
		t.Fatalf("parse %q: %v", wkt, err)
	}
	return area
}

func depotFence(t *testing.T) *geofence.Geofence {
	t.Helper()
	return &geofence.Geofence{
		ID:   5,
		Name: "depot",
		// The unit square (0,0)-(1,1) in the Traccar "lat lon" order.
		Area: mustArea(t, "POLYGON ((0 0, 0 1, 1 1, 1 0, 0 0))"),
	}
}

func locatedPosition(deviceID int64, latitude, longitude float64) *model.Position {
	position := model.NewPosition("gt06")
	position.DeviceID = deviceID
	position.Time = time.Date(2026, 10, 31, 13, 2, 10, 0, time.UTC)
	position.Valid = true
	position.Latitude = latitude
	position.Longitude = longitude
	return position
}

func TestOverspeed(t *testing.T) {
	// GT06 reports 27 knots for ~50 km/h; the threshold is in knots as well.
	engine := New(Config{OverspeedKnots: 30}, nil)

	slow := locatedPosition(1, 54.9, 24.0)
	slow.Speed = 27
	if alerts := engine.Evaluate(slow); len(alerts) != 0 {
		t.Fatalf("alerts below the limit: %v", alerts)
	}

	fast := locatedPosition(1, 54.9, 24.0)
	fast.Speed = 31.5
	alerts := engine.Evaluate(fast)
	if len(alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(alerts))
	}
	alert := alerts[0]
	if alert.Type != model.AlertOverspeed || alert.DeviceID != 1 {
		t.Errorf("alert = %+v", alert)
	}
	if alert.Attributes["speed"] != 31.5 || alert.Attributes["limit"] != 30.0 {
		t.Errorf("attributes = %#v", alert.Attributes)
	}
	if !alert.Time.Equal(fast.Time) {
		t.Errorf("time = %v, want %v", alert.Time, fast.Time)
	}
}

func TestOverspeedDisabledAndInvalidFix(t *testing.T) {
	if alerts := New(Config{}, nil).Evaluate(speed(100)); len(alerts) != 0 {
		t.Errorf("a zero threshold should disable the rule, got %v", alerts)
	}

	invalid := speed(100)
	invalid.Valid = false
	if alerts := New(Config{OverspeedKnots: 30}, nil).Evaluate(invalid); len(alerts) != 0 {
		t.Errorf("an invalid fix should not raise overspeed, got %v", alerts)
	}
}

func speed(knots float64) *model.Position {
	position := locatedPosition(1, 54.9, 24.0)
	position.Speed = knots
	return position
}

func TestPowerCut(t *testing.T) {
	// GT06 0x94 and Teltonika report volts: 12.5 V, not 12500.
	engine := New(Config{PowerCutVolts: 10}, nil)

	healthy := locatedPosition(1, 54.9, 24.0)
	healthy.Set(model.KeyPower, 12.5)
	if alerts := engine.Evaluate(healthy); len(alerts) != 0 {
		t.Fatalf("12.5 V is above the threshold, got %v", alerts)
	}

	cut := locatedPosition(1, 54.9, 24.0)
	cut.Set(model.KeyPower, 0.0)
	alerts := engine.Evaluate(cut)
	if len(alerts) != 1 || alerts[0].Type != model.AlertPowerCut {
		t.Fatalf("alerts = %v", alerts)
	}
	if alerts[0].Attributes["power"] != 0.0 || alerts[0].Attributes["threshold"] != 10.0 {
		t.Errorf("attributes = %#v", alerts[0].Attributes)
	}

	// Protocols that never report power must not raise the rule.
	if alerts := engine.Evaluate(locatedPosition(1, 54.9, 24.0)); len(alerts) != 0 {
		t.Errorf("a missing power attribute raised %v", alerts)
	}
}

func TestGeofenceTransitions(t *testing.T) {
	engine := New(Config{}, []*geofence.Geofence{depotFence(t)})

	// The first sighting only seeds the state, like the Java handler.
	if alerts := engine.Evaluate(locatedPosition(1, 0.5, 0.5)); len(alerts) != 0 {
		t.Fatalf("first position: %v", alerts)
	}
	if alerts := engine.Evaluate(locatedPosition(1, 0.6, 0.6)); len(alerts) != 0 {
		t.Fatalf("still inside: %v", alerts)
	}

	alerts := engine.Evaluate(locatedPosition(1, 5, 5))
	if len(alerts) != 1 {
		t.Fatalf("exit alerts = %d, want 1", len(alerts))
	}
	if alerts[0].Type != model.AlertGeofenceExit || alerts[0].GeofenceID != 5 {
		t.Errorf("alert = %+v", alerts[0])
	}
	if alerts[0].Attributes["geofence"] != "depot" {
		t.Errorf("attributes = %#v", alerts[0].Attributes)
	}

	alerts = engine.Evaluate(locatedPosition(1, 0.4, 0.4))
	if len(alerts) != 1 || alerts[0].Type != model.AlertGeofenceEnter {
		t.Fatalf("enter alerts = %v", alerts)
	}

	// Transitions are tracked per device.
	if alerts := engine.Evaluate(locatedPosition(2, 5, 5)); len(alerts) != 0 {
		t.Errorf("another device should only seed its own state, got %v", alerts)
	}
}

func TestSetGeofencesReseedsState(t *testing.T) {
	engine := New(Config{}, []*geofence.Geofence{depotFence(t)})
	engine.Evaluate(locatedPosition(1, 0.5, 0.5))

	engine.SetGeofences(nil)
	engine.SetGeofences([]*geofence.Geofence{depotFence(t)})

	if alerts := engine.Evaluate(locatedPosition(1, 5, 5)); len(alerts) != 0 {
		t.Errorf("state should have been reset, got %v", alerts)
	}
}

func TestGeofenceNeedsLocation(t *testing.T) {
	engine := New(Config{}, []*geofence.Geofence{depotFence(t)})

	withoutFix := model.NewPosition("gt06")
	withoutFix.DeviceID = 1
	if alerts := engine.Evaluate(withoutFix); len(alerts) != 0 {
		t.Errorf("a position without a fix raised %v", alerts)
	}
}

func TestAlertSnapshot(t *testing.T) {
	engine := New(Config{OverspeedKnots: 20}, []*geofence.Geofence{depotFence(t)})
	engine.Evaluate(locatedPosition(1, 0.5, 0.5))

	position := locatedPosition(1, 5, 5)
	position.Speed = 40
	position.Altitude = 120
	alerts := engine.Evaluate(position)
	if len(alerts) != 2 {
		t.Fatalf("alerts = %d, want overspeed and geofenceExit", len(alerts))
	}

	for _, alert := range alerts {
		if alert.Attributes["latitude"] != 5.0 || alert.Attributes["longitude"] != 5.0 {
			t.Errorf("%s: attributes = %#v", alert.Type, alert.Attributes)
		}
		if alert.Attributes["protocol"] != "gt06" {
			t.Errorf("%s: protocol = %v", alert.Type, alert.Attributes["protocol"])
		}
	}
}

func TestEvaluateIgnoresUnknownDevice(t *testing.T) {
	engine := New(Config{OverspeedKnots: 20}, nil)
	position := speed(100)
	position.DeviceID = 0
	if alerts := engine.Evaluate(position); alerts != nil {
		t.Errorf("alerts = %v", alerts)
	}
	if alerts := engine.Evaluate(nil); alerts != nil {
		t.Errorf("nil position produced %v", alerts)
	}
}

func TestEvaluateWithNoRulesIsIdle(t *testing.T) {
	engine := New(Config{}, nil)
	if alerts := engine.Evaluate(speed(100)); alerts != nil {
		t.Errorf("an engine without rules or geofences returned %v", alerts)
	}
}

func TestAlertUsesCurrentTimeWhenPositionHasNone(t *testing.T) {
	engine := New(Config{OverspeedKnots: 20}, nil)
	fixed := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return fixed }

	position := speed(100)
	position.Time = time.Time{}
	alerts := engine.Evaluate(position)
	if len(alerts) != 1 || !alerts[0].Time.Equal(fixed) {
		t.Fatalf("alerts = %v", alerts)
	}
}
