package handler

import (
	"testing"

	"traccar-server_go/go_server/internal/model"
)

func TestDistanceAccumulatesFromThePreviousPosition(t *testing.T) {
	last := filterPosition()
	last.Set(model.KeyTotalDist, 1000.0)

	handler := DistanceHandler{
		Stage:        StageDistance,
		LastPosition: func(int64) *model.Position { return last },
	}

	position := filterPosition()
	// Roughly 1.11 km north of the previous position.
	position.Latitude += 0.01

	if !passes(t, handler, position) {
		t.Fatal("the distance handler dropped the position")
	}

	distance, _ := asFloat(mustAttribute(t, position, model.KeyDistance))
	if distance < 1100 || distance > 1120 {
		t.Errorf("distance = %v, want about 1113 m", distance)
	}
	total, _ := asFloat(mustAttribute(t, position, model.KeyTotalDist))
	if total < 2100 || total > 2120 {
		t.Errorf("totalDistance = %v, want about 2113 m", total)
	}
}

func TestDistanceKeepsTheReportedDistance(t *testing.T) {
	last := filterPosition()
	last.Set(model.KeyTotalDist, 500.0)

	handler := DistanceHandler{
		Stage:        StageDistance,
		LastPosition: func(int64) *model.Position { return last },
	}

	position := filterPosition()
	position.Set(model.KeyDistance, 42.0)

	if !passes(t, handler, position) {
		t.Fatal("the distance handler dropped the position")
	}

	if value, _ := asFloat(mustAttribute(t, position, model.KeyDistance)); value != 42 {
		t.Errorf("distance = %v, want the value the decoder reported", value)
	}
	if value, _ := asFloat(mustAttribute(t, position, model.KeyTotalDist)); value != 542 {
		t.Errorf("totalDistance = %v, want 542", value)
	}
}

func TestDistanceStartsATripWithoutHistory(t *testing.T) {
	handler := DistanceHandler{Stage: StageDistance}

	position := filterPosition()
	if !passes(t, handler, position) {
		t.Fatal("the distance handler dropped the position")
	}

	if value, _ := asFloat(mustAttribute(t, position, model.KeyDistance)); value != 0 {
		t.Errorf("distance = %v, want zero for the first position", value)
	}
	if value, _ := asFloat(mustAttribute(t, position, model.KeyTotalDist)); value != 0 {
		t.Errorf("totalDistance = %v, want zero for the first position", value)
	}
}

func mustAttribute(t *testing.T, position *model.Position, key string) any {
	t.Helper()
	value, ok := position.Get(key)
	if !ok {
		t.Fatalf("position has no %q attribute", key)
	}
	return value
}
