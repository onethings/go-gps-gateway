package notify

import (
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

func testDevice() *model.Device {
	return &model.Device{ID: 7, UniqueID: "864717003283581", Name: "van-7"}
}

func testAlert(alertType string, attributes map[string]any) *model.Event {
	return &model.Event{
		DeviceID:   7,
		Type:       alertType,
		Time:       time.Date(2026, 10, 31, 7, 32, 10, 0, time.UTC),
		Attributes: attributes,
	}
}

func TestFormatOverspeed(t *testing.T) {
	alert := testAlert(model.EventDeviceOverspeed, map[string]any{
		"protocol":  "gt06",
		"latitude":  54.943,
		"longitude": 25.206,
		"altitude":  float64(100),
		"speed":     31.5,
		"limit":     float64(30),
	})

	message := Format(alert, testDevice())

	if want := "[traccar] deviceOverspeed: van-7 (864717003283581)"; message.Subject != want {
		t.Errorf("subject = %q, want %q", message.Subject, want)
	}

	wantBody := strings.Join([]string{
		"Device: van-7 (864717003283581)",
		"Type: deviceOverspeed",
		"Time: 2026-10-31T07:32:10Z",
		"Event: Speed 31.5 kn above limit 30 kn",
		"Location: 54.943000, 25.206000 (altitude 100 m)",
		"Attributes:",
		"  altitude: 100",
		"  latitude: 54.943",
		"  limit: 30",
		"  longitude: 25.206",
		"  protocol: gt06",
		"  speed: 31.5",
	}, "\n")
	if message.Body != wantBody {
		t.Errorf("body:\n%s\nwant:\n%s", message.Body, wantBody)
	}
}

func TestFormatWithoutDevice(t *testing.T) {
	message := Format(testAlert(model.EventPowerCut, map[string]any{"power": 9.5, "threshold": float64(12)}), nil)

	if want := "[traccar] powerCut"; message.Subject != want {
		t.Errorf("subject = %q, want %q", message.Subject, want)
	}
	if !strings.HasPrefix(message.Body, "Device: 7\n") {
		t.Errorf("body should name the device id:\n%s", message.Body)
	}
	if !strings.Contains(message.Body, "Event: Power 9.5 V below threshold 12 V") {
		t.Errorf("missing power summary:\n%s", message.Body)
	}
}

func TestFormatNilAlert(t *testing.T) {
	message := Format(nil, nil)
	if message.Subject != "" || message.Body != "" {
		t.Errorf("expected empty message, got %+v", message)
	}
}

func TestSummary(t *testing.T) {
	tests := []struct {
		name  string
		alert *model.Event
		want  string
	}{
		{
			name:  "overspeed",
			alert: testAlert(model.EventDeviceOverspeed, map[string]any{"speed": 31.5, "limit": float64(30)}),
			want:  "Speed 31.5 kn above limit 30 kn",
		},
		{
			name:  "powerCut",
			alert: testAlert(model.EventPowerCut, map[string]any{"power": 9.5, "threshold": float64(12)}),
			want:  "Power 9.5 V below threshold 12 V",
		},
		{
			name:  "powerRestored",
			alert: testAlert(model.EventPowerRestored, map[string]any{"power": 13.25}),
			want:  "Power restored at 13.25 V",
		},
		{
			name:  "geofenceEnter",
			alert: testAlert(model.EventGeofenceEnter, map[string]any{"geofence": "倉庫"}),
			want:  "Entered geofence 倉庫",
		},
		{
			name:  "geofenceExit",
			alert: testAlert(model.EventGeofenceExit, map[string]any{"geofence": "Warehouse"}),
			want:  "Exited geofence Warehouse",
		},
		{
			name:  "missing attributes",
			alert: testAlert(model.EventDeviceOverspeed, nil),
			want:  "",
		},
		{
			name:  "unknown type",
			alert: testAlert("deviceOnline", map[string]any{"x": 1}),
			want:  "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Summary(test.alert); got != test.want {
				t.Errorf("summary = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSplitList(t *testing.T) {
	got := SplitList(" a@example.com, b@example.com;c@example.com  d@example.com ")
	want := []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com"}

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if len(SplitList("  , ; ")) != 0 {
		t.Errorf("expected no tokens for separators only")
	}
}
