package handler

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

func runStage(t *testing.T, stage PositionHandler, position *model.Position) *model.Position {
	t.Helper()
	var seen *model.Position
	err := stage.HandlePosition(context.Background(), position,
		func(_ context.Context, forwarded *model.Position) error {
			seen = forwarded
			return nil
		})
	if err != nil {
		t.Fatalf("%s: %v", stage.Name(), err)
	}
	if seen == nil {
		t.Fatalf("%s dropped the position", stage.Name())
	}
	return seen
}

func TestTimeHandlerFillsMissingTimes(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	position := model.NewPosition("test")
	position.DeviceID = 1

	got := runStage(t, TimeHandler{Stage: StageDefaultData, Now: func() time.Time { return now }},
		position)
	if !got.ServerTime.Equal(now) {
		t.Fatalf("server time = %v, want %v", got.ServerTime, now)
	}
	if !got.Time.Equal(now) {
		t.Fatalf("device time = %v, want %v", got.Time, now)
	}
}

func TestTimeHandlerKeepsReportedTime(t *testing.T) {
	reported := time.Date(2025, 5, 6, 7, 8, 9, 0, time.UTC)
	position := model.NewPosition("test")
	position.Time = reported

	got := runStage(t, TimeHandler{Stage: StageDefaultData}, position)
	if !got.Time.Equal(reported) {
		t.Fatalf("device time = %v, want %v", got.Time, reported)
	}
}

func TestHemisphereHandlerSignsCoordinates(t *testing.T) {
	position := model.NewPosition("test")
	position.Latitude = 25.5
	position.Longitude = 51.25
	position.Set(KeyHemisphere, "SW")

	got := runStage(t, HemisphereHandler{Stage: StageDefaultData}, position)
	if got.Latitude != -25.5 || got.Longitude != -51.25 {
		t.Fatalf("coordinates = %v,%v, want -25.5,-51.25", got.Latitude, got.Longitude)
	}
	if got.HasAttribute(KeyHemisphere) {
		t.Fatal("hemisphere attribute must be removed once applied")
	}
}

func TestCopyAttributesHandlerCopiesMissingValues(t *testing.T) {
	previous := model.NewPosition("test")
	previous.Set(model.KeyIgnition, true)
	previous.Set(model.KeyDriverID, "old")

	position := model.NewPosition("test")
	position.DeviceID = 7
	position.Set(model.KeyDriverID, "new")

	got := runStage(t, CopyAttributesHandler{
		Stage:        StagePosition,
		Attributes:   []string{model.KeyIgnition, model.KeyDriverID},
		LastPosition: func(int64) *model.Position { return previous },
	}, position)

	if !got.Attributes.GetBoolean(model.KeyIgnition) {
		t.Fatal("ignition was not copied")
	}
	if got.Attributes.GetString(model.KeyDriverID, "") != "new" {
		t.Fatal("a reported attribute must not be overwritten")
	}
}

type stubGeocoder struct {
	address string
	err     error
	calls   int
}

func (g *stubGeocoder) Address(context.Context, float64, float64) (string, error) {
	g.calls++
	return g.address, g.err
}

func TestGeocoderHandlerSetsAddress(t *testing.T) {
	geocoder := &stubGeocoder{address: "1 Test Street"}
	position := model.NewPosition("test")
	position.Valid = true
	position.Latitude = 1
	position.Longitude = 2

	got := runStage(t, GeocoderHandler{Stage: StagePosition, Geocoder: geocoder}, position)
	if got.Attributes.GetString(KeyAddress, "") != "1 Test Street" {
		t.Fatalf("address = %q", got.Attributes.GetString(KeyAddress, ""))
	}
}

func TestGeocoderHandlerIgnoresInvalidPosition(t *testing.T) {
	geocoder := &stubGeocoder{address: "unused"}
	position := model.NewPosition("test")

	runStage(t, GeocoderHandler{Stage: StagePosition, Geocoder: geocoder}, position)
	if geocoder.calls != 0 {
		t.Fatalf("geocoder was called %d times for an invalid position", geocoder.calls)
	}
}

func TestGeocoderHandlerSurvivesFailure(t *testing.T) {
	geocoder := &stubGeocoder{err: errors.New("unavailable")}
	position := model.NewPosition("test")
	position.Valid = true
	position.Latitude = 1
	position.Longitude = 2

	got := runStage(t, GeocoderHandler{Stage: StagePosition, Geocoder: geocoder}, position)
	if got.HasAttribute(KeyAddress) {
		t.Fatal("a failed lookup must not write an address")
	}
}

type stubLocator struct {
	latitude  float64
	longitude float64
	ok        bool
	calls     int
}

func (l *stubLocator) Locate(context.Context, *model.Network) (float64, float64, bool) {
	l.calls++
	return l.latitude, l.longitude, l.ok
}

func TestGeolocationHandlerFillsMissingFix(t *testing.T) {
	locator := &stubLocator{latitude: 12.5, longitude: 34.5, ok: true}
	position := model.NewPosition("test")
	position.SetNetwork(model.NewNetworkWith(model.NewCellTower(1, 2, 3, 4)))

	got := runStage(t, GeolocationHandler{Stage: StagePosition, Locator: locator}, position)
	if !got.Valid || got.Latitude != 12.5 || got.Longitude != 34.5 {
		t.Fatalf("position = %v", got)
	}
	if !got.Attributes.GetBoolean(KeyApproximate) {
		t.Fatal("a cell tower location must be marked approximate")
	}
}

func TestEngineHoursHandlerAccumulatesWhileIgnitionIsOn(t *testing.T) {
	stage := &EngineHoursHandler{Stage: StagePosition}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	first := model.NewPosition("test")
	first.DeviceID = 3
	first.Time = start
	first.Set(model.KeyIgnition, true)
	runStage(t, stage, first)

	second := model.NewPosition("test")
	second.DeviceID = 3
	second.Time = start.Add(2 * time.Hour)
	second.Set(model.KeyIgnition, true)
	got := runStage(t, stage, second)

	if hours := got.Attributes.GetDouble(model.KeyHours, 0); math.Abs(hours-2) > 1e-9 {
		t.Fatalf("hours = %v, want 2", hours)
	}
}

func TestEngineHoursHandlerDoesNotAccumulateWhileOff(t *testing.T) {
	stage := &EngineHoursHandler{Stage: StagePosition}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for index := 0; index < 2; index++ {
		position := model.NewPosition("test")
		position.DeviceID = 4
		position.Time = start.Add(time.Duration(index) * time.Hour)
		runStage(t, stage, position)
	}

	position := model.NewPosition("test")
	position.DeviceID = 4
	position.Time = start.Add(5 * time.Hour)
	got := runStage(t, stage, position)
	if hours := got.Attributes.GetDouble(model.KeyHours, 0); hours != 0 {
		t.Fatalf("hours = %v, want 0", hours)
	}
}

func TestComputedAttributesHandlerEvaluatesInPriorityOrder(t *testing.T) {
	position := model.NewPosition("test")
	position.DeviceID = 1
	position.Set(model.KeyFuel, 10.0)

	stage := ComputedAttributesHandler{
		Stage: StageComputedAttributes,
		Attributes: func(int64) []*model.Attribute {
			return []*model.Attribute{
				{ID: 2, Attribute: "total", Expression: "fuel + 5", Type: "number", Priority: 2},
				{ID: 1, Attribute: "fuel", Expression: "fuel * 2", Type: "number", Priority: 1},
			}
		},
	}

	got := runStage(t, stage, position)
	if fuel := got.Attributes.GetDouble(model.KeyFuel, 0); fuel != 20 {
		t.Fatalf("fuel = %v, want 20", fuel)
	}
	if total := got.Attributes.GetDouble("total", 0); total != 25 {
		t.Fatalf("total = %v, want 25", total)
	}
}

func TestComputedAttributesHandlerSkipsBrokenExpression(t *testing.T) {
	position := model.NewPosition("test")
	var logged int
	stage := ComputedAttributesHandler{
		Stage: StageComputedAttributes,
		Attributes: func(int64) []*model.Attribute {
			return []*model.Attribute{{ID: 1, Attribute: "broken", Expression: "1 +"}}
		},
		Logger: func(string, ...any) { logged++ },
	}

	got := runStage(t, stage, position)
	if got.HasAttribute("broken") {
		t.Fatal("a broken expression must not write a value")
	}
	if logged != 1 {
		t.Fatalf("logged %d errors, want 1", logged)
	}
}
