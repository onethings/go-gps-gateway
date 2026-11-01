package notify

import (
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

func TestRenderTemplateVariables(t *testing.T) {
	values := map[string]any{
		"event":  map[string]any{"type": "deviceOverspeed"},
		"device": map[string]any{"name": "Van 7"},
		"speed":  41.5,
	}
	got := RenderTemplate("$device.name: ${event.type} at $speed", values)
	if got != "Van 7: deviceOverspeed at 41.5" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderTemplateMissingFieldIsEmpty(t *testing.T) {
	if got := RenderTemplate("[$missing.field]", nil); got != "[]" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderTemplateIfElse(t *testing.T) {
	cases := []struct {
		name     string
		template string
		values   map[string]any
		want     string
	}{
		{"true", "#if($online)yes#end", map[string]any{"online": true}, "yes"},
		{"false", "#if($online)yes#end", map[string]any{"online": false}, ""},
		{"else", "#if($online)yes#else no#end", map[string]any{"online": false}, " no"},
		{"nested", "#if($a)A#if($b)B#end#end", map[string]any{"a": true, "b": false}, "A"},
		{"zero", "#if($count)yes#else no#end", map[string]any{"count": 0}, " no"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := RenderTemplate(test.template, test.values); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestRenderTemplateDollarEscape(t *testing.T) {
	if got := RenderTemplate("$$5 and $name", map[string]any{"name": "x"}); got != "$5 and x" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatterReachesStructFields(t *testing.T) {
	event := &model.Event{
		DeviceID: 7,
		Type:     model.EventDeviceOverspeed,
		Time:     time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
		Attributes: model.Attributes{
			"speed": 41.5,
		},
	}
	device := &model.Device{ID: 7, Name: "Van"}
	got := TextTemplateFormatter{}.Format("${device.name}: ${event.type} ${event.attributes.speed}", event, device, nil)
	if got != "Van: deviceOverspeed 41.5" {
		t.Fatalf("got %q", got)
	}
}
