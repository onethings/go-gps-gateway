package handler

import (
	"context"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// fixedNow is the clock the filter tests compare against.
var fixedNow = time.Date(2024, 3, 5, 12, 0, 0, 0, time.UTC)

// filterDevice is one position device with attributes and a last position.
type filterDevice struct {
	attributes map[string]any
	last       *model.Position
}

func (d filterDevice) handler() FilterHandler {
	return FilterHandler{
		Stage: StageFilter,
		Attributes: func(int64) map[string]any {
			return d.attributes
		},
		LastPosition: func(int64) *model.Position {
			return d.last
		},
		Now: func() time.Time { return fixedNow },
	}
}

// passes reports whether the position survived the filter stage.
func passes(t *testing.T, handler PositionHandler, position *model.Position) bool {
	t.Helper()
	delivered := false
	next := func(context.Context, *model.Position) error {
		delivered = true
		return nil
	}
	if err := handler.HandlePosition(context.Background(), position, next); err != nil {
		t.Fatalf("handle: %v", err)
	}
	return delivered
}

func filterPosition() *model.Position {
	position := model.NewPosition("gt06")
	position.DeviceID = 7
	position.Time = fixedNow.Add(-time.Minute)
	position.Valid = true
	position.Latitude = 22.123456
	position.Longitude = 114.123456
	position.Speed = 20
	return position
}

func TestFilterPassesWhenNothingIsEnabled(t *testing.T) {
	position := filterPosition()
	if !passes(t, filterDevice{}.handler(), position) {
		t.Fatal("a position was dropped without a filter enabled")
	}
}

func TestFilterDropsFutureByDefault(t *testing.T) {
	position := filterPosition()
	position.Time = fixedNow.Add(2 * 24 * time.Hour)

	if passes(t, filterDevice{}.handler(), position) {
		t.Fatal("a position two days in the future was not dropped")
	}
}

func TestFilterFutureCanBeDisabled(t *testing.T) {
	position := filterPosition()
	position.Time = fixedNow.Add(2 * 24 * time.Hour)
	device := filterDevice{attributes: map[string]any{AttrFilterFuture: 0}}

	if !passes(t, device.handler(), position) {
		t.Fatal("filter.future = 0 must disable the future filter")
	}
}

func TestFilterInvalid(t *testing.T) {
	device := filterDevice{attributes: map[string]any{AttrFilterInvalid: "true"}}

	valid := filterPosition()
	if !passes(t, device.handler(), valid) {
		t.Fatal("a valid position was dropped")
	}

	invalid := filterPosition()
	invalid.Valid = false
	if passes(t, device.handler(), invalid) {
		t.Fatal("an invalid position was not dropped")
	}

	outOfRange := filterPosition()
	outOfRange.Latitude = 91
	if passes(t, device.handler(), outOfRange) {
		t.Fatal("a position with latitude 91 was not dropped")
	}
}

func TestFilterZero(t *testing.T) {
	device := filterDevice{attributes: map[string]any{AttrFilterZero: true}}

	position := filterPosition()
	position.Latitude = 0
	position.Longitude = 0
	if passes(t, device.handler(), position) {
		t.Fatal("a zero coordinate position was not dropped")
	}
}

func TestFilterOutdated(t *testing.T) {
	device := filterDevice{attributes: map[string]any{AttrFilterOutdated: true}}

	position := filterPosition()
	position.Outdated = true
	if passes(t, device.handler(), position) {
		t.Fatal("an outdated position was not dropped")
	}
}

func TestFilterAccuracy(t *testing.T) {
	device := filterDevice{attributes: map[string]any{AttrFilterAccuracy: 10}}

	position := filterPosition()
	position.Accuracy = 25
	if passes(t, device.handler(), position) {
		t.Fatal("a position above the accuracy limit was not dropped")
	}
}

func TestFilterDuplicate(t *testing.T) {
	last := filterPosition()
	last.Attributes["sat"] = 9
	device := filterDevice{
		attributes: map[string]any{AttrFilterDuplicate: true},
		last:       last,
	}

	duplicate := filterPosition()
	duplicate.Attributes["sat"] = 9
	if passes(t, device.handler(), duplicate) {
		t.Fatal("a duplicate position was not dropped")
	}

	// The Java duplicate check only looks at the keys: a key the previous
	// position lacks makes the new one distinct.
	fresh := filterPosition()
	fresh.Attributes["ignition"] = true
	if !passes(t, device.handler(), fresh) {
		t.Fatal("a position with a new attribute key was dropped")
	}
}

func TestFilterDistance(t *testing.T) {
	last := filterPosition()
	device := filterDevice{
		attributes: map[string]any{AttrFilterDistance: 100},
		last:       last,
	}

	// About eleven metres north of the previous position.
	near := filterPosition()
	near.Latitude += 0.0001
	if passes(t, device.handler(), near) {
		t.Fatal("a position closer than filter.distance was not dropped")
	}

	far := filterPosition()
	far.Latitude += 0.01
	if !passes(t, device.handler(), far) {
		t.Fatal("a position beyond filter.distance was dropped")
	}
}

func TestFilterMaxSpeedDropsADrift(t *testing.T) {
	last := filterPosition()
	last.Time = fixedNow.Add(-time.Minute)
	device := filterDevice{
		attributes: map[string]any{AttrFilterMaxSpeed: 500},
		last:       last,
	}

	jump := filterPosition()
	jump.Time = fixedNow
	jump.Latitude += 1
	if passes(t, device.handler(), jump) {
		t.Fatal("a jump faster than filter.maxSpeed was not dropped")
	}
}

func TestFilterMinPeriod(t *testing.T) {
	last := filterPosition()
	last.Time = fixedNow.Add(-10 * time.Second)
	device := filterDevice{
		attributes: map[string]any{AttrFilterMinPeriod: 60},
		last:       last,
	}

	soon := filterPosition()
	soon.Time = fixedNow
	if passes(t, device.handler(), soon) {
		t.Fatal("a position inside filter.minPeriod was not dropped")
	}
}

func TestFilterSkipAttributesLetsThePositionThrough(t *testing.T) {
	last := filterPosition()
	last.Attributes["ignition"] = false
	device := filterDevice{
		attributes: map[string]any{
			AttrFilterDuplicate: true,
			AttrFilterSkipAttr:  "ignition, door",
		},
		last: last,
	}

	ignited := filterPosition()
	ignited.Attributes["ignition"] = true
	if !passes(t, device.handler(), ignited) {
		t.Fatal("an attribute in filter.skipAttributes did not bypass the filter")
	}
}

func TestOutdatedHandlerCopiesTheLastLocation(t *testing.T) {
	last := filterPosition()
	last.Latitude = 1.5
	last.Longitude = 2.5
	last.Valid = true
	last.Speed = 12

	handler := OutdatedHandler{
		Stage:        StageFilter,
		LastPosition: func(int64) *model.Position { return last },
	}

	position := filterPosition()
	position.Outdated = true
	position.Latitude = 0
	position.Longitude = 0
	position.Valid = false
	position.Speed = 0

	if !passes(t, handler, position) {
		t.Fatal("the outdated handler dropped the position")
	}
	if position.Latitude != 1.5 || position.Longitude != 2.5 || !position.Valid || position.Speed != 12 {
		t.Errorf("outdated position = %s", position)
	}
}

func TestOutdatedHandlerWithoutHistoryKeepsTheFrameTime(t *testing.T) {
	handler := OutdatedHandler{Stage: StageFilter}

	position := filterPosition()
	position.Outdated = true
	position.Time = time.Time{}
	position.ServerTime = fixedNow

	handler.fill(position)
	if !position.Time.Equal(fixedNow) {
		t.Errorf("time = %v, want the server time", position.Time)
	}
	if position.Latitude != 22.123456 {
		t.Errorf("latitude = %v, want it untouched", position.Latitude)
	}
}

func TestPipelineStopsWhenAHandlerDrops(t *testing.T) {
	var ran []string

	pipeline := Pipeline{
		Stage: StageFilter,
		Handlers: []PositionHandler{
			HandlerFunc{Stage: StageFilter, Fn: func(
				ctx context.Context, position *model.Position, next Next,
			) error {
				ran = append(ran, "first")
				return next(ctx, position)
			}},
			HandlerFunc{Stage: StageFilter, Fn: func(
				context.Context, *model.Position, Next,
			) error {
				ran = append(ran, "drop")
				return nil
			}},
			HandlerFunc{Stage: StageFilter, Fn: func(
				ctx context.Context, position *model.Position, next Next,
			) error {
				ran = append(ran, "last")
				return next(ctx, position)
			}},
		},
	}

	if passes(t, pipeline, filterPosition()) {
		t.Fatal("a dropped position reached the chain after the pipeline")
	}
	if len(ran) != 2 || ran[0] != "first" || ran[1] != "drop" {
		t.Fatalf("ran %v, want the pipeline to stop at the dropping handler", ran)
	}
}
