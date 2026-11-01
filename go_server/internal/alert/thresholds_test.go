package alert

import (
	"testing"

	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
)

func speedFor(deviceID int64, knots float64) *model.Position {
	position := locatedPosition(deviceID, 54.9, 24.0)
	position.Speed = knots
	return position
}

func fenceWithLimit(t *testing.T, id int64, limit float64) *geofence.Geofence {
	t.Helper()
	fence := depotFence(t)
	fence.ID = id
	fence.Attributes = map[string]any{AttrSpeedLimit: limit}
	return fence
}

func alertsOfType(alerts []*model.Event, alertType string) []*model.Event {
	var matches []*model.Event
	for _, alert := range alerts {
		if alert.Type == alertType {
			matches = append(matches, alert)
		}
	}
	return matches
}

func TestOverspeedPerDeviceThreshold(t *testing.T) {
	engine := New(Config{OverspeedKnots: 30}, nil)
	engine.SetDeviceAttributes(map[int64]map[string]any{
		1: {AttrSpeedLimit: 50.0},
	})

	// Above the global threshold but below the device one.
	if alerts := engine.Evaluate(speedFor(1, 40)); len(alerts) != 0 {
		t.Fatalf("device limit 50 raised at 40 knots: %v", alerts)
	}

	alerts := engine.Evaluate(speedFor(1, 55))
	if len(alerts) != 1 {
		t.Fatalf("alerts = %v, want one overspeed", alerts)
	}
	if alerts[0].Attributes["limit"] != 50.0 {
		t.Errorf("limit = %v, want the device override 50", alerts[0].Attributes["limit"])
	}

	// A device without the attribute keeps the configured default.
	alerts = engine.Evaluate(speedFor(3, 40))
	if len(alerts) != 1 || alerts[0].Attributes["limit"] != 30.0 {
		t.Fatalf("alerts = %#v, want the global limit 30", alerts)
	}
}

func TestOverspeedDeviceThresholdParsesStrings(t *testing.T) {
	engine := New(Config{OverspeedKnots: 30}, nil)
	engine.SetDeviceAttributes(map[int64]map[string]any{
		1: {AttrSpeedLimit: "50"},
	})

	if alerts := engine.Evaluate(speedFor(1, 40)); len(alerts) != 0 {
		t.Fatalf("string attribute ignored: %v", alerts)
	}
}

func TestOverspeedDeviceThresholdZeroDisables(t *testing.T) {
	engine := New(Config{OverspeedKnots: 30}, nil)
	engine.SetDeviceAttributes(map[int64]map[string]any{
		1: {AttrSpeedLimit: 0.0},
	})

	if alerts := engine.Evaluate(speedFor(1, 100)); len(alerts) != 0 {
		t.Fatalf("a zero device limit should disable the rule, got %v", alerts)
	}
}

func TestDeviceThresholdWithoutGlobalRule(t *testing.T) {
	engine := New(Config{}, nil)
	engine.SetDeviceAttributes(map[int64]map[string]any{
		1: {AttrSpeedLimit: 20.0},
	})

	alerts := engine.Evaluate(speedFor(1, 30))
	if len(alerts) != 1 || alerts[0].Attributes["limit"] != 20.0 {
		t.Fatalf("alerts = %#v, want the device limit 20", alerts)
	}
	if alerts := engine.Evaluate(speedFor(2, 30)); len(alerts) != 0 {
		t.Errorf("device 2 has no threshold and should stay idle, got %v", alerts)
	}
}

func TestOverspeedPositionOverride(t *testing.T) {
	engine := New(Config{OverspeedKnots: 30}, nil)
	engine.SetDeviceAttributes(map[int64]map[string]any{
		1: {AttrSpeedLimit: 50.0},
	})

	position := speedFor(1, 40)
	position.Set(model.KeySpeedLim, 45.0)
	if alerts := engine.Evaluate(position); len(alerts) != 0 {
		t.Fatalf("position limit 45 raised at 40 knots: %v", alerts)
	}

	position = speedFor(1, 48)
	position.Set(model.KeySpeedLim, 45.0)
	alerts := engine.Evaluate(position)
	if len(alerts) != 1 || alerts[0].Attributes["limit"] != 45.0 {
		t.Fatalf("alerts = %#v, want the position limit 45", alerts)
	}
}

func TestOverspeedGeofenceThresholdUsesHighestByDefault(t *testing.T) {
	engine := New(Config{OverspeedKnots: 30}, []*geofence.Geofence{
		fenceWithLimit(t, 1, 40),
		fenceWithLimit(t, 2, 50),
	})

	inside := func(knots float64) *model.Position {
		position := locatedPosition(1, 0.5, 0.5)
		position.Speed = knots
		return position
	}

	if alerts := engine.Evaluate(inside(45)); len(alerts) != 0 {
		t.Fatalf("45 knots is below the highest geofence limit: %v", alerts)
	}

	alerts := alertsOfType(engine.Evaluate(inside(55)), model.EventDeviceOverspeed)
	if len(alerts) != 1 || alerts[0].Attributes["limit"] != 50.0 {
		t.Fatalf("alerts = %#v, want the highest geofence limit 50", alerts)
	}
}

func TestOverspeedGeofenceThresholdPreferLowest(t *testing.T) {
	engine := New(Config{OverspeedKnots: 30, PreferLowest: true}, []*geofence.Geofence{
		fenceWithLimit(t, 1, 40),
		fenceWithLimit(t, 2, 50),
	})

	position := locatedPosition(1, 0.5, 0.5)
	position.Speed = 45
	alerts := alertsOfType(engine.Evaluate(position), model.EventDeviceOverspeed)
	if len(alerts) != 1 || alerts[0].Attributes["limit"] != 40.0 {
		t.Fatalf("alerts = %#v, want the lowest geofence limit 40", alerts)
	}
}

func TestOverspeedGeofenceThresholdAppliesOnlyInside(t *testing.T) {
	engine := New(Config{OverspeedKnots: 30}, []*geofence.Geofence{
		fenceWithLimit(t, 1, 50),
	})

	inside := locatedPosition(1, 0.5, 0.5)
	inside.Speed = 40
	if alerts := alertsOfType(engine.Evaluate(inside), model.EventDeviceOverspeed); len(alerts) != 0 {
		t.Fatalf("inside the geofence the limit is 50: %v", alerts)
	}

	outside := locatedPosition(1, 5, 5)
	outside.Speed = 40
	alerts := alertsOfType(engine.Evaluate(outside), model.EventDeviceOverspeed)
	if len(alerts) != 1 || alerts[0].Attributes["limit"] != 30.0 {
		t.Fatalf("outside the geofence the global limit 30 applies: %#v", alerts)
	}
}

func TestOverspeedGeofenceThresholdRespectsAltitude(t *testing.T) {
	fence := fenceWithLimit(t, 1, 50)
	fence.Attributes["ceiling"] = 100.0
	engine := New(Config{OverspeedKnots: 30}, []*geofence.Geofence{fence})

	// Inside the polygon but above the ceiling, so the geofence is not
	// entered and its speed limit does not apply.
	position := locatedPosition(1, 0.5, 0.5)
	position.Altitude = 150
	position.Speed = 40
	alerts := alertsOfType(engine.Evaluate(position), model.EventDeviceOverspeed)
	if len(alerts) != 1 || alerts[0].Attributes["limit"] != 30.0 {
		t.Fatalf("alerts = %#v, want the global limit 30 above the ceiling", alerts)
	}
}

func TestPowerCutPerDeviceThreshold(t *testing.T) {
	engine := New(Config{PowerCutVolts: 10}, nil)
	engine.SetDeviceAttributes(map[int64]map[string]any{
		1: {AttrPowerCutVolts: 8.0},
		2: {AttrPowerCutVolts: 0.0},
	})

	// 9 V is below the global threshold but above the device one.
	position := locatedPosition(1, 54.9, 24.0)
	position.Set(model.KeyPower, 9.0)
	if alerts := engine.Evaluate(position); len(alerts) != 0 {
		t.Fatalf("9 V is above the device threshold 8: %v", alerts)
	}

	position = locatedPosition(1, 54.9, 24.0)
	position.Set(model.KeyPower, 7.0)
	alerts := engine.Evaluate(position)
	if len(alerts) != 1 || alerts[0].Type != model.EventPowerCut {
		t.Fatalf("alerts = %#v, want powerCut", alerts)
	}
	if alerts[0].Attributes["threshold"] != 8.0 {
		t.Errorf("threshold = %v, want the device override 8", alerts[0].Attributes["threshold"])
	}

	// A zero device threshold disables the rule even when the flag is set.
	disabled := locatedPosition(2, 54.9, 24.0)
	disabled.Set(model.KeyPower, 0.0)
	if alerts := engine.Evaluate(disabled); len(alerts) != 0 {
		t.Fatalf("device 2 should have the rule disabled, got %v", alerts)
	}
}

func TestSetDeviceAttributesCopiesInput(t *testing.T) {
	values := map[string]any{AttrSpeedLimit: 50.0}
	engine := New(Config{OverspeedKnots: 30}, nil)
	engine.SetDeviceAttributes(map[int64]map[string]any{1: values})

	values[AttrSpeedLimit] = 10.0

	if alerts := engine.Evaluate(speedFor(1, 40)); len(alerts) != 0 {
		t.Fatalf("the engine kept a reference to the caller's map: %v", alerts)
	}
}
