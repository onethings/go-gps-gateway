package geofence

import (
	"testing"

	"traccar-server_go/go_server/internal/model"
)

// square is the unit square (0,0)-(1,1) in the Traccar "lat lon" order.
const square = "POLYGON ((0 0, 0 1, 1 1, 1 0, 0 0))"

func mustParse(t *testing.T, wkt string) Area {
	t.Helper()
	area, err := Parse(wkt)
	if err != nil {
		t.Fatalf("Parse(%q): %v", wkt, err)
	}
	return area
}

func TestPolygonContains(t *testing.T) {
	area := mustParse(t, square)

	tests := []struct {
		name      string
		latitude  float64
		longitude float64
		want      bool
	}{
		{"centre", 0.5, 0.5, true},
		{"north east", 0.9, 0.9, true},
		{"north of the square", 1.5, 0.5, false},
		{"west of the square", 0.5, -0.5, false},
		{"far away", 40.7, -74.0, false},
	}
	for _, tc := range tests {
		if got := area.Contains(tc.latitude, tc.longitude); got != tc.want {
			t.Errorf("%s: Contains(%v, %v) = %v, want %v",
				tc.name, tc.latitude, tc.longitude, got, tc.want)
		}
	}
}

// TestPolygonBoundary pins the Java behaviour: the ray casting test is not
// symmetric on the edges of the ring (the west edge counts as outside, the
// east edge as inside). Changing this changes results for existing geofences.
func TestPolygonBoundary(t *testing.T) {
	area := mustParse(t, square)

	if !area.Contains(0.5, 1) {
		t.Error("east edge should be inside, matching GeofencePolygon")
	}
	if area.Contains(0.5, 0) {
		t.Error("west edge should be outside, matching GeofencePolygon")
	}
}

func TestPolygonAcrossAntimeridian(t *testing.T) {
	// 10..12 latitude, 179..-179 longitude.
	area := mustParse(t, "POLYGON ((10 179, 10 -179, 12 -179, 12 179, 10 179))")

	tests := []struct {
		latitude  float64
		longitude float64
		want      bool
	}{
		{11, 180, true},
		{11, 179.5, true},
		{11, -179.5, true},
		{11, 0, false},
		{15, 180, false},
	}
	for _, tc := range tests {
		if got := area.Contains(tc.latitude, tc.longitude); got != tc.want {
			t.Errorf("Contains(%v, %v) = %v, want %v", tc.latitude, tc.longitude, got, tc.want)
		}
	}
}

func TestCircleContains(t *testing.T) {
	area := mustParse(t, "CIRCLE (55 25, 1000)")

	// 500 m north of the centre, then 2 km north of it.
	north500 := 55 + 500.0/111320
	north2000 := 55 + 2000.0/111320

	if !area.Contains(north500, 25) {
		t.Error("500 m north of the centre should be inside a 1000 m circle")
	}
	if area.Contains(north2000, 25) {
		t.Error("2 km north of the centre should be outside a 1000 m circle")
	}
}

func TestWKTRoundTrip(t *testing.T) {
	for _, wkt := range []string{
		square,
		"CIRCLE (55.5 25.25, 250)",
		"POLYGON ((10 179, 10 -179, 12 -179, 12 179, 10 179))",
	} {
		area := mustParse(t, wkt)
		rendered := area.WKT()
		if rendered != wkt {
			t.Errorf("WKT() = %q, want %q", rendered, wkt)
		}
		if _, err := Parse(rendered); err != nil {
			t.Errorf("Parse(%q): %v", rendered, err)
		}
	}
}

func TestParsePolygonWithoutSpaces(t *testing.T) {
	compact := mustParse(t, "POLYGON((0 0,0 1,1 1,1 0,0 0))")
	if !compact.Contains(0.5, 0.5) {
		t.Error("compact WKT should describe the same square")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []string{
		"",
		"UNKNOWN ((0 0))",
		"POLYGON ((0 0, 0 1))",
		"POLYGON ((0 0, 0 1, 1))",
		"POLYGON ((0 0, 0 1, one 1))",
		"POLYGON ()",
		"CIRCLE (55 25)",
		"CIRCLE (55 25, 100, 200)",
		"CIRCLE (55 25, -1)",
	}
	for _, wkt := range tests {
		if _, err := Parse(wkt); err == nil {
			t.Errorf("Parse(%q) accepted an invalid geometry", wkt)
		}
	}
}

func TestGeofenceAltitudeLimits(t *testing.T) {
	area := mustParse(t, square)

	g := &Geofence{ID: 1, Area: area, Attributes: map[string]any{"floor": 100.0, "ceiling": 200.0}}

	position := model.NewPosition("gt06")
	position.Latitude, position.Longitude, position.Altitude = 0.5, 0.5, 150
	if !g.Contains(position) {
		t.Error("altitude 150 m is between the floor and the ceiling")
	}

	position.Altitude = 50
	if g.Contains(position) {
		t.Error("altitude 50 m is below the floor")
	}

	position.Altitude = 250
	if g.Contains(position) {
		t.Error("altitude 250 m is above the ceiling")
	}

	// Without attributes the altitude is ignored.
	if !(&Geofence{Area: area}).Contains(position) {
		t.Error("a geofence without limits should ignore the altitude")
	}
}
