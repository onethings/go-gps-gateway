// Package handler runs the ordered position pipeline the Java server calls
// ProcessingHandler. The protocol servers stay pure decoders and hand every
// position to a Chain; the chain is the only place where business rules that
// need more than one frame - device state, geofences, rules - may live.
package handler

import (
	"context"

	"traccar-server_go/go_server/internal/model"
)

// Stage names, in the order the Java server's ProcessingHandler declares them.
// A stage the gateway has no implementation for yet is still declared, because
// a stage inserted later in the middle of the list is the kind of change that
// silently reorders everything after it.
const (
	// StageFilter drops positions that must not reach the rest of the chain.
	StageFilter = "filter"
	// StageDefaultData fills the fields every position needs before the rules
	// look at it.
	StageDefaultData = "defaultData"
	// StagePosition keeps the per-device last location and validity.
	StagePosition = "position"
	// StageMotion turns position deltas into moving / stopped state.
	StageMotion = "motion"
	// StageGeofence matches the position against the geofences.
	StageGeofence = "geofence"
	// StageComputedAttributes evaluates the attributes configured on the device.
	StageComputedAttributes = "computedAttributes"
	// StageSpeedLimit applies the speed limit of the geofences containing the
	// position.
	StageSpeedLimit = "speedLimit"
	// StageAlertEvent raises the events a device reports itself, such as alarm
	// and ignition.
	StageAlertEvent = "alertEvent"
	// StageBusiness is the gateway's own rule stage: the alert pipeline.
	StageBusiness = "business"
	// StageDistance accumulates the trip distance.
	StageDistance = "distance"
	// StageFuel applies the fuel calculations.
	StageFuel = "fuel"
	// StageDriver attaches the driver to the position.
	StageDriver = "driver"
	// StageMaintenance evaluates the maintenance intervals.
	StageMaintenance = "maintenance"
	// StageMedia follows the media index attached to the device.
	StageMedia = "media"
)

// Order is the processing order of the chain, which is the order of the Java
// server's ProcessingHandler. TestHandlerOrder pins it.
var Order = []string{
	StageFilter,
	StageDefaultData,
	StagePosition,
	StageMotion,
	StageGeofence,
	StageComputedAttributes,
	StageSpeedLimit,
	StageAlertEvent,
	StageBusiness,
	StageDistance,
	StageFuel,
	StageDriver,
	StageMaintenance,
	StageMedia,
}

// Next is the rest of the chain: a stage calls it to hand the position on.
type Next func(ctx context.Context, position *model.Position) error

// PositionHandler is one stage of the chain. A stage that decides the position
// must not be processed any further simply does not call next, which is how the
// Java server's FilterHandler drops a position.
type PositionHandler interface {
	// Name is the stage name, one of the Stage constants.
	Name() string
	// HandlePosition processes one position and hands it on.
	HandlePosition(ctx context.Context, position *model.Position, next Next) error
}

// HandlerFunc adapts a function to PositionHandler.
type HandlerFunc struct {
	Stage string
	Fn    func(ctx context.Context, position *model.Position, next Next) error
}

// Name implements PositionHandler.
func (h HandlerFunc) Name() string { return h.Stage }

// HandlePosition implements PositionHandler.
func (h HandlerFunc) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	return h.Fn(ctx, position, next)
}

// Placeholder is a declared stage with no implementation yet. It passes the
// position straight on, so declaring it changes nothing but the order.
type Placeholder struct{ Stage string }

// Name implements PositionHandler.
func (p Placeholder) Name() string { return p.Stage }

// HandlePosition implements PositionHandler.
func (p Placeholder) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	return next(ctx, position)
}

// Chain runs the stages in order and then the terminal. It implements
// gateway.Sink, so a protocol server can be handed the chain directly.
type Chain struct {
	stages   []PositionHandler
	terminal Next
	names    []string
}

// NewChain builds a chain. terminal runs after the last stage and is normally
// the position store; a nil terminal ends the chain.
func NewChain(terminal Next, stages ...PositionHandler) *Chain {
	chain := &Chain{
		stages:   append([]PositionHandler(nil), stages...),
		terminal: terminal,
		names:    make([]string, 0, len(stages)),
	}
	for _, stage := range chain.stages {
		chain.names = append(chain.names, stage.Name())
	}
	return chain
}

// Names lists the stages in the order they run.
func (c *Chain) Names() []string {
	return append([]string(nil), c.names...)
}

// Handle implements gateway.Sink. The sink contract carries no context, so the
// chain runs on the background one: processing a position must not outlive the
// frame that produced it anyway.
func (c *Chain) Handle(position *model.Position) error {
	return c.HandlePosition(context.Background(), position)
}

// HandlePosition runs the chain for one position.
func (c *Chain) HandlePosition(ctx context.Context, position *model.Position) error {
	return c.step(ctx, 0, position)
}

// step runs the stage at index and, through the next function it is given, the
// stages after it.
func (c *Chain) step(ctx context.Context, index int, position *model.Position) error {
	if index == len(c.stages) {
		if c.terminal == nil {
			return nil
		}
		return c.terminal(ctx, position)
	}

	stage := c.stages[index]
	return stage.HandlePosition(ctx, position, func(ctx context.Context, position *model.Position) error {
		return c.step(ctx, index+1, position)
	})
}
