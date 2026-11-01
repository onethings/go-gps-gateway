package handler

import (
	"context"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/eventctx"
	"traccar-server_go/go_server/internal/model"
)

func positionAt(deviceID int64, at time.Time) *model.Position {
	position := model.NewPosition("gt06")
	position.DeviceID = deviceID
	position.Time = at
	position.Latitude = 54.0
	position.Longitude = 25.0
	position.Valid = true
	return position
}

func TestAlarmRule(t *testing.T) {
	rule := &AlarmRule{IgnoreDuplicates: true}
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	first := positionAt(1, at)
	first.AddAlarm(model.AlarmSOS)
	events := rule.Events(first)
	if len(events) != 1 || events[0].Type != model.EventAlarm {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Attributes.GetString(model.KeyAlarm, "") != model.AlarmSOS {
		t.Errorf("alarm = %v", events[0].Attributes)
	}

	// The same alarm on the next position is suppressed.
	second := positionAt(1, at.Add(time.Minute))
	second.AddAlarm(model.AlarmSOS)
	if events := rule.Events(second); len(events) != 0 {
		t.Fatalf("duplicate alarm = %+v", events)
	}

	// A new alarm still raises.
	third := positionAt(1, at.Add(2*time.Minute))
	third.AddAlarm(model.AlarmSOS)
	third.AddAlarm(model.AlarmTow)
	events = rule.Events(third)
	if len(events) != 1 || events[0].Attributes.GetString(model.KeyAlarm, "") != model.AlarmTow {
		t.Fatalf("events = %+v", events)
	}
}

func TestIgnitionRule(t *testing.T) {
	rule := &IgnitionRule{}
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	first := positionAt(1, at)
	first.Set(model.KeyIgnition, false)
	if events := rule.Events(first); len(events) != 0 {
		t.Fatalf("first position = %+v", events)
	}

	second := positionAt(1, at.Add(time.Minute))
	second.Set(model.KeyIgnition, true)
	events := rule.Events(second)
	if len(events) != 1 || events[0].Type != model.EventIgnitionOn {
		t.Fatalf("events = %+v", events)
	}

	third := positionAt(1, at.Add(2*time.Minute))
	third.Set(model.KeyIgnition, false)
	events = rule.Events(third)
	if len(events) != 1 || events[0].Type != model.EventIgnitionOff {
		t.Fatalf("events = %+v", events)
	}
}

func TestFuelRule(t *testing.T) {
	rule := &FuelRule{Config: EventConfig{FuelDropThreshold: 10, FuelIncreaseThreshold: 5}}
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	first := positionAt(1, at)
	first.Set(model.KeyFuel, 80.0)
	rule.Events(first)

	// A three litre drop is under the threshold.
	small := positionAt(1, at.Add(time.Minute))
	small.Set(model.KeyFuel, 77.0)
	if events := rule.Events(small); len(events) != 0 {
		t.Fatalf("small drop = %+v", events)
	}

	drop := positionAt(1, at.Add(2*time.Minute))
	drop.Set(model.KeyFuel, 60.0)
	events := rule.Events(drop)
	if len(events) != 1 || events[0].Type != model.EventDeviceFuelDrop {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Attributes.GetDouble("before", 0) != 77 || events[0].Attributes.GetDouble("after", 0) != 60 {
		t.Errorf("attributes = %v", events[0].Attributes)
	}

	increase := positionAt(1, at.Add(3*time.Minute))
	increase.Set(model.KeyFuel, 70.0)
	events = rule.Events(increase)
	if len(events) != 1 || events[0].Type != model.EventDeviceFuelIncrease {
		t.Fatalf("events = %+v", events)
	}
}

func TestFuelRuleDeviceOverride(t *testing.T) {
	rule := &FuelRule{
		Config: EventConfig{FuelDropThreshold: 50},
		Providers: EventProviders{Attributes: func(int64) map[string]any {
			return map[string]any{AttrFuelDropThreshold: 5.0}
		}},
	}
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	first := positionAt(1, at)
	first.Set(model.KeyFuel, 80.0)
	rule.Events(first)
	second := positionAt(1, at.Add(time.Minute))
	second.Set(model.KeyFuel, 70.0)
	if events := rule.Events(second); len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
}

func TestBehaviorRule(t *testing.T) {
	rule := &BehaviorRule{Config: EventConfig{AccelerationThreshold: 2, BrakingThreshold: 2}}
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	first := positionAt(1, at)
	first.Speed = 0
	rule.Events(first)

	// 20 knots in 2 seconds is about 5.1 m/s².
	fast := positionAt(1, at.Add(2*time.Second))
	fast.Speed = 20
	events := rule.Events(fast)
	if len(events) != 1 || events[0].Attributes.GetString(model.KeyAlarm, "") != model.AlarmHardAcceleration {
		t.Fatalf("events = %+v", events)
	}

	slow := positionAt(1, at.Add(4*time.Second))
	slow.Speed = 0
	events = rule.Events(slow)
	if len(events) != 1 || events[0].Attributes.GetString(model.KeyAlarm, "") != model.AlarmHardBraking {
		t.Fatalf("events = %+v", events)
	}
}

func TestDriverRule(t *testing.T) {
	rule := &DriverRule{}
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	first := positionAt(1, at)
	first.Set(model.KeyDriverID, "CARD1")
	if events := rule.Events(first); len(events) != 1 || events[0].Type != model.EventDriverChanged {
		t.Fatalf("first driver = %+v", events)
	}

	same := positionAt(1, at.Add(time.Minute))
	same.Set(model.KeyDriverID, "CARD1")
	if events := rule.Events(same); len(events) != 0 {
		t.Fatalf("same driver = %+v", events)
	}

	other := positionAt(1, at.Add(2*time.Minute))
	other.Set(model.KeyDriverID, "CARD2")
	if events := rule.Events(other); len(events) != 1 {
		t.Fatalf("changed driver = %+v", events)
	}
}

func TestMediaAndCommandResultRules(t *testing.T) {
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	position := positionAt(1, at)
	position.Set(model.KeyImage, "photo.jpg")
	position.Set(model.KeyResult, "OK")

	media := (&MediaRule{}).Events(position)
	if len(media) != 1 || media[0].Type != model.EventMedia ||
		media[0].Attributes.GetString("file", "") != "photo.jpg" {
		t.Fatalf("media = %+v", media)
	}
	result := (&CommandResultRule{}).Events(position)
	if len(result) != 1 || result[0].Type != model.EventCommandResult {
		t.Fatalf("result = %+v", result)
	}
}

func TestMaintenanceRule(t *testing.T) {
	rule := &MaintenanceRule{Maintenances: func(int64) []*model.Maintenance {
		return []*model.Maintenance{{ID: 9, Type: model.KeyOdometer, Start: 100, Period: 100}}
	}}
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	first := positionAt(1, at)
	first.Set(model.KeyOdometer, 150.0)
	if events := rule.Events(first); len(events) != 0 {
		t.Fatalf("first = %+v", events)
	}

	crossed := positionAt(1, at.Add(time.Minute))
	crossed.Set(model.KeyOdometer, 205.0)
	events := rule.Events(crossed)
	if len(events) != 1 || events[0].Type != model.EventMaintenance {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Attributes.GetLong("maintenanceId", 0) != 9 {
		t.Errorf("attributes = %v", events[0].Attributes)
	}

	// Still inside the same interval: no second event.
	inside := positionAt(1, at.Add(2*time.Minute))
	inside.Set(model.KeyOdometer, 250.0)
	if events := rule.Events(inside); len(events) != 0 {
		t.Fatalf("inside = %+v", events)
	}
}

func TestProximityRule(t *testing.T) {
	linked := model.NewPosition("gt06")
	linked.DeviceID = 2
	linked.Latitude = 54.0009 // about 100 m north
	linked.Longitude = 25.0

	rule := &ProximityRule{
		Config: EventConfig{ProximityEnterDistance: 200, ProximityExitDistance: 300},
		Providers: EventProviders{
			LinkedDevices: func(int64) []int64 { return []int64{2} },
			LastPosition:  func(int64) *model.Position { return linked },
		},
	}
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	far := positionAt(1, at)
	far.Latitude = 54.01
	rule.Events(far)

	near := positionAt(1, at.Add(time.Minute))
	near.Latitude = 54.001
	events := rule.Events(near)
	if len(events) != 1 || events[0].Type != model.EventProximityEnter {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Attributes.GetLong("linkedDeviceId", 0) != 2 {
		t.Errorf("attributes = %v", events[0].Attributes)
	}
}

func TestUnaccompaniedMotion(t *testing.T) {
	linked := model.NewPosition("gt06")
	linked.DeviceID = 2
	linked.Latitude = 10
	linked.Longitude = 10

	rule := &ProximityRule{
		Config: EventConfig{UnaccompaniedDistance: 100},
		Providers: EventProviders{
			LinkedDevices: func(int64) []int64 { return []int64{2} },
			LastPosition:  func(int64) *model.Position { return linked },
		},
	}
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	stopped := positionAt(1, at)
	stopped.Set(model.KeyMotion, false)
	rule.Events(stopped)

	moving := positionAt(1, at.Add(time.Minute))
	moving.Set(model.KeyMotion, true)
	events := rule.Events(moving)
	if len(events) != 1 || events[0].Type != model.EventUnaccompaniedMotion {
		t.Fatalf("events = %+v", events)
	}
}

// TestRuleStageFeedsTheTerminal pins the buffer contract: the events a stage
// raises reach the terminal of the chain, which is where they are written.
func TestRuleStageFeedsTheTerminal(t *testing.T) {
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	var collected []*model.Event
	chain := NewChain(func(ctx context.Context, position *model.Position) error {
		collected = append(collected, eventctx.Events(ctx)...)
		return nil
	},
		RuleStage{Stage: StageAlertEvent, Rules: []EventRule{&IgnitionRule{}}},
	)

	first := positionAt(1, at)
	first.Set(model.KeyIgnition, false)
	if err := chain.Handle(first); err != nil {
		t.Fatalf("handle: %v", err)
	}
	second := positionAt(1, at.Add(time.Minute))
	second.Set(model.KeyIgnition, true)
	if err := chain.Handle(second); err != nil {
		t.Fatalf("handle: %v", err)
	}

	if len(collected) != 1 || collected[0].Type != model.EventIgnitionOn {
		t.Fatalf("collected = %+v", collected)
	}
}
