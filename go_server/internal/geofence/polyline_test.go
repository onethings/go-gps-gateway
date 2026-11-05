package geofence

import (
	"testing"

	"traccar-server_go/go_server/internal/model"
)

// equator is a one degree leg along the equator, which puts the metric and the
// degree distances on the same scale for the corridor tests.
const equator = "LINESTRING (0 0, 0 1)"

func TestParseLinestring(t *testing.T) {
	area := mustParse(t, equator)

	if got, want := area.WKT(), "LINESTRING (0 0, 0 1)"; got != want {
		t.Errorf("WKT() = %q, want %q", got, want)
	}
	if !area.Contains(0.0001, 0.5) {
		t.Error("a point 11 m from the line should be inside the default 25 m corridor")
	}
	if area.Contains(0.001, 0.5) {
		t.Error("a point 111 m from the line should be outside the default 25 m corridor")
	}
}

func TestParseLinestringWithoutSpaces(t *testing.T) {
	area := mustParse(t, "LINESTRING(0 0,0 1)")
	if got, want := area.WKT(), "LINESTRING (0 0, 0 1)"; got != want {
		t.Errorf("WKT() = %q, want %q", got, want)
	}
}

func TestPolylineContains(t *testing.T) {
	area := mustParse(t, equator)

	tests := []struct {
		name      string
		latitude  float64
		longitude float64
		want      bool
	}{
		{"on the line", 0, 0.5, true},
		{"just north of the line", 0.0001, 0.5, true},
		{"just south of the line", -0.0001, 0.5, true},
		{"111 m north of the line", 0.001, 0.5, false},
		{"near the east end", 0.0001, 0.9999, true},
		{"beyond the east end", 0.0001, 2, false},
		{"beyond the west end", 0.0001, -1, false},
		{"far away", 40.7, -74, false},
	}
	for _, tc := range tests {
		if got := area.Contains(tc.latitude, tc.longitude); got != tc.want {
			t.Errorf("%s: Contains(%v, %v) = %v, want %v",
				tc.name, tc.latitude, tc.longitude, got, tc.want)
		}
	}
}

func TestPolylineCrossesSegment(t *testing.T) {
	parsed, err := Parse(equator)
	if err != nil {
		t.Fatalf("Parse(%q): %v", equator, err)
	}
	area, ok := parsed.(*Polyline)
	if !ok {
		t.Fatalf("Parse(%q) = %T, want *Polyline", equator, parsed)
	}

	if !area.CrossesSegment(0.001, 0.5, -0.001, 0.5) {
		t.Error("a segment from north to south across the line should cross it")
	}
	if area.CrossesSegment(0.001, 0.5, 0.001, 0.6) {
		t.Error("a segment that stays north of the line should not cross it")
	}
	if area.CrossesSegment(0.001, 2, 0.001, 3) {
		t.Error("a segment beyond the end of the line should not cross it")
	}
}

func TestPolylineDistanceFromAttributes(t *testing.T) {
	fence := &Geofence{
		Attributes: model.Attributes{"polylineDistance": 200.0},
	}
	if got := fence.PolylineDistance(); got != 200 {
		t.Fatalf("PolylineDistance() = %v, want 200", got)
	}

	area, err := ParseWithDistance(equator, fence.PolylineDistance())
	if err != nil {
		t.Fatalf("ParseWithDistance: %v", err)
	}
	if !area.Contains(0.001, 0.5) {
		t.Error("a point 111 m away should be inside a 200 m corridor")
	}
}

func TestPolylineDistanceDefaults(t *testing.T) {
	fence := &Geofence{}
	if got := fence.PolylineDistance(); got != DefaultPolylineDistance {
		t.Errorf("PolylineDistance() = %v, want %v", got, DefaultPolylineDistance)
	}
}

func TestParseRejectsBadLinestring(t *testing.T) {
	for _, wkt := range []string{
		"LINESTRING (0 0)",
		"LINESTRING (0 0, 0)",
		"LINESTRING (0 0, one 1)",
		"LINESTRING ()",
	} {
		if _, err := Parse(wkt); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", wkt)
		}
	}
}
