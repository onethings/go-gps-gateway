package handler

import (
	"testing"

	"traccar-server_go/go_server/internal/geofence"
)

func fence(t *testing.T, id int64, wkt string) *geofence.Geofence {
	t.Helper()
	area, err := geofence.Parse(wkt)
	if err != nil {
		t.Fatalf("parse %s: %v", wkt, err)
	}
	return &geofence.Geofence{ID: id, Name: "fence", Area: area}
}

func TestGeofenceHandlerWritesTheContainingFences(t *testing.T) {
	depot := fence(t, 5, "CIRCLE (22.123456 114.123456, 500)")
	far := fence(t, 9, "CIRCLE (-33.86 151.21, 500)")

	handler := GeofenceHandler{
		Stage: StageGeofence,
		Geofences: func() []*geofence.Geofence {
			return []*geofence.Geofence{depot, far}
		},
	}

	position := filterPosition()
	if !passes(t, handler, position) {
		t.Fatal("the geofence handler dropped the position")
	}
	if len(position.GeofenceIDs) != 1 || position.GeofenceIDs[0] != 5 {
		t.Fatalf("geofence ids = %v, want [5]", position.GeofenceIDs)
	}
}

func TestGeofenceHandlerLeavesAnOutsidePositionAlone(t *testing.T) {
	handler := GeofenceHandler{
		Stage: StageGeofence,
		Geofences: func() []*geofence.Geofence {
			return []*geofence.Geofence{fence(t, 5, "CIRCLE (-33.86 151.21, 500)")}
		},
	}

	position := filterPosition()
	if !passes(t, handler, position) {
		t.Fatal("the geofence handler dropped the position")
	}
	if position.GeofenceIDs != nil {
		t.Fatalf("geofence ids = %v, want none", position.GeofenceIDs)
	}
}

func TestGeofenceHandlerWithoutASetPassesThrough(t *testing.T) {
	position := filterPosition()
	if !passes(t, GeofenceHandler{Stage: StageGeofence}, position) {
		t.Fatal("an unconfigured geofence stage dropped the position")
	}
	if position.GeofenceIDs != nil {
		t.Fatalf("geofence ids = %v, want none", position.GeofenceIDs)
	}
}
