package handler

import (
	"context"

	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
)

// GeofenceHandler writes the geofences containing a position back onto it, like
// the Java GeofenceHandler. The ids are what the web interface highlights and
// what the reports group by; the enter / exit events are raised separately by
// the alert pipeline.
type GeofenceHandler struct {
	// Stage is the stage name, StageGeofence.
	Stage string
	// Geofences returns the current geofence set. Nil leaves the position
	// untouched.
	Geofences func() []*geofence.Geofence
}

// Name implements PositionHandler.
func (h GeofenceHandler) Name() string { return h.Stage }

// HandlePosition implements PositionHandler.
func (h GeofenceHandler) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	if position != nil && h.Geofences != nil {
		ids := make([]int64, 0, 2)
		for _, fence := range h.Geofences() {
			if fence != nil && fence.Contains(position) {
				ids = append(ids, fence.ID)
			}
		}
		if len(ids) > 0 {
			position.GeofenceIDs = ids
		}
	}
	return next(ctx, position)
}
