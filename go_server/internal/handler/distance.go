package handler

import (
	"context"

	"traccar-server_go/go_server/internal/model"
)

// DistanceHandler accumulates the trip distance of a device, which is what the
// trip, route and summary reports read. It ports the Java DistanceHandler: the
// distance to the previous position is added to the total the previous position
// carried, so a report can sum a single attribute instead of walking the whole
// history.
//
// The Java coordinates.filter smoothing (coordinates.minError / maxError) is not
// ported: it is off by default there and only matters once the map matcher is
// enabled.
type DistanceHandler struct {
	// Stage is the stage name, StageDistance.
	Stage string
	// LastPosition returns the newest stored position of a device. Nil makes
	// every position the start of a trip.
	LastPosition func(deviceID int64) *model.Position
}

// Name implements PositionHandler.
func (h DistanceHandler) Name() string { return h.Stage }

// HandlePosition implements PositionHandler.
func (h DistanceHandler) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	if position != nil {
		h.accumulate(position)
	}
	return next(ctx, position)
}

// accumulate writes the distance since the previous position and the running
// total onto the position. A device that reports its own odometer-based distance
// keeps it; the total is accumulated either way.
func (h DistanceHandler) accumulate(position *model.Position) {
	meters, reported := positionDistance(position)

	var total float64
	last := h.lastPosition(position.DeviceID)
	if last != nil {
		if value, ok := last.Get(model.KeyTotalDist); ok {
			if parsed, ok := asFloat(value); ok {
				total = parsed
			}
		}
		if !reported {
			meters = distance(position.Latitude, position.Longitude, last.Latitude, last.Longitude)
		}
	}

	position.Set(model.KeyDistance, meters)
	position.Set(model.KeyTotalDist, total+meters)
}

func (h DistanceHandler) lastPosition(deviceID int64) *model.Position {
	if h.LastPosition == nil {
		return nil
	}
	return h.LastPosition(deviceID)
}

// positionDistance reads the distance the decoder already put on the position,
// which is the case for devices that count it themselves.
func positionDistance(position *model.Position) (float64, bool) {
	value, ok := position.Get(model.KeyDistance)
	if !ok {
		return 0, false
	}
	parsed, ok := asFloat(value)
	if !ok {
		return 0, false
	}
	return parsed, true
}
