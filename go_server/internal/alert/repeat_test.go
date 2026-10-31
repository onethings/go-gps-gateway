package alert

import (
	"testing"
	"time"

	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
)

func alertTypes(alerts []*model.Alert) []string {
	types := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		types = append(types, alert.Type)
	}
	return types
}

func poweredPosition(deviceID int64, volts float64) *model.Position {
	position := model.NewPosition("gt06")
	position.DeviceID = deviceID
	position.Time = time.Date(2026, 10, 31, 13, 2, 10, 0, time.UTC)
	position.Set(model.KeyPower, volts)
	return position
}

func wantTypes(t *testing.T, step string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", step, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", step, got, want)
		}
	}
}

func TestNotRepeatSuppressesRepeats(t *testing.T) {
	base := time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC)
	engine := New(Config{OverspeedKnots: 20, NotRepeat: time.Hour}, nil)

	now := base
	engine.now = func() time.Time { return now }

	wantTypes(t, "first", alertTypes(engine.Evaluate(speed(30))), []string{model.AlertOverspeed})

	now = base.Add(time.Minute)
	wantTypes(t, "repeat within the window", alertTypes(engine.Evaluate(speed(31))), nil)

	now = base.Add(2 * time.Hour)
	wantTypes(t, "repeat after the window", alertTypes(engine.Evaluate(speed(32))), []string{model.AlertOverspeed})
}

func TestNotRepeatIsPerDevice(t *testing.T) {
	base := time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC)
	engine := New(Config{OverspeedKnots: 20, NotRepeat: time.Hour}, nil)
	engine.now = func() time.Time { return base }

	first := speed(30)
	first.DeviceID = 7
	wantTypes(t, "device 7", alertTypes(engine.Evaluate(first)), []string{model.AlertOverspeed})

	other := speed(30)
	other.DeviceID = 8
	wantTypes(t, "device 8", alertTypes(engine.Evaluate(other)), []string{model.AlertOverspeed})

	repeat := speed(30)
	repeat.DeviceID = 7
	wantTypes(t, "device 7 repeat", alertTypes(engine.Evaluate(repeat)), nil)
}

func TestNotRepeatDisabledByDefault(t *testing.T) {
	base := time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC)
	engine := New(Config{OverspeedKnots: 20}, nil)
	engine.now = func() time.Time { return base }

	for i := 0; i < 3; i++ {
		wantTypes(t, "unsuppressed evaluation", alertTypes(engine.Evaluate(speed(30))), []string{model.AlertOverspeed})
	}
}

func TestNotRepeatKeepsGeofenceTransitions(t *testing.T) {
	base := time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC)
	engine := New(Config{NotRepeat: time.Hour}, []*geofence.Geofence{depotFence(t)})
	engine.now = func() time.Time { return base }

	inside := locatedPosition(7, 0.5, 0.5)
	outside := locatedPosition(7, 2, 2)

	// The first sighting only seeds the membership state.
	wantTypes(t, "seed", alertTypes(engine.Evaluate(inside)), nil)
	wantTypes(t, "exit", alertTypes(engine.Evaluate(outside)), []string{model.AlertGeofenceExit})
	wantTypes(t, "enter", alertTypes(engine.Evaluate(inside)), []string{model.AlertGeofenceEnter})
}

func TestPowerCutAndRestore(t *testing.T) {
	engine := New(Config{PowerCutVolts: 12}, nil)

	cases := []struct {
		name  string
		volts float64
		want  []string
	}{
		{"cut", 11.5, []string{model.AlertPowerCut}},
		{"still cut", 11.4, []string{model.AlertPowerCut}},
		{"restored", 12.5, []string{model.AlertPowerRestored}},
		{"healthy", 13, nil},
		{"cut again", 11.8, []string{model.AlertPowerCut}},
	}
	for _, test := range cases {
		wantTypes(t, test.name, alertTypes(engine.Evaluate(poweredPosition(7, test.volts))), test.want)
	}
}

func TestPowerRestoreIsNotSuppressed(t *testing.T) {
	base := time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC)
	engine := New(Config{PowerCutVolts: 12, NotRepeat: time.Hour}, nil)
	engine.now = func() time.Time { return base }

	wantTypes(t, "cut", alertTypes(engine.Evaluate(poweredPosition(7, 11))), []string{model.AlertPowerCut})
	wantTypes(t, "repeat cut", alertTypes(engine.Evaluate(poweredPosition(7, 11))), nil)
	wantTypes(t, "restore", alertTypes(engine.Evaluate(poweredPosition(7, 13))), []string{model.AlertPowerRestored})
}

func TestPowerRestoreNeedsAPreviousCut(t *testing.T) {
	engine := New(Config{PowerCutVolts: 12}, nil)

	// A device that has always been powered must not report a restore.
	wantTypes(t, "healthy", alertTypes(engine.Evaluate(poweredPosition(7, 13))), nil)
	wantTypes(t, "healthy again", alertTypes(engine.Evaluate(poweredPosition(7, 14))), nil)
}
