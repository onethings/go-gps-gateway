// Package handler runs the ordered position pipeline the Java server calls
// ProcessingHandler. The protocol servers stay pure decoders and hand every
// position to a Chain; the chain is the only place where business rules that
// need more than one frame - device state, geofences, rules - may live.
package handler

import (
	"context"

	"traccar-server_go/go_server/internal/eventctx"
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
	// StageMapMatcher snaps a position's coordinates onto the nearest road, so
	// geofence, distance and the reports work on the road instead of the raw
	// fix.
	StageMapMatcher = "mapMatcher"
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
	// StageDistance accumulates the trip distance. It runs before the filter so
	// the filter's distance and speed limits see the fresh value.
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
// server's ProcessingHandler. Distance runs before Filter because the Java
// server keeps it that way: the filter's distance and speed limits compare the
// value the distance stage has just written. TestHandlerOrder pins it.
var Order = []string{
	StageDistance,
	StageFilter,
	StageDefaultData,
	StagePosition,
	StageMotion,
	StageMapMatcher,
	StageGeofence,
	StageComputedAttributes,
	StageSpeedLimit,
	StageAlertEvent,
	StageBusiness,
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

// Pipeline runs several handlers in order inside one stage. The Java server
// splits some stages over several handlers - the filter stage runs
// OutdatedHandler before FilterHandler - and this keeps them in one slot of
// Order instead of inventing stage names the Java server does not have.
type Pipeline struct {
	// Stage is the stage name, one of the Stage constants.
	Stage string
	// Handlers run in order; the last one hands the position to the rest of the
	// chain.
	Handlers []PositionHandler
}

// Name implements PositionHandler.
func (p Pipeline) Name() string { return p.Stage }

// HandlePosition implements PositionHandler. A handler that stops the position
// also stops the pipeline, which is how a filter inside a pipeline drops it.
func (p Pipeline) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	return p.run(ctx, 0, position, next)
}

func (p Pipeline) run(ctx context.Context, index int, position *model.Position, next Next) error {
	if index == len(p.Handlers) {
		return next(ctx, position)
	}
	return p.Handlers[index].HandlePosition(ctx, position, func(ctx context.Context, position *model.Position) error {
		return p.run(ctx, index+1, position, next)
	})
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

// HandlePosition runs the chain for one position. The context carries the event
// buffer the stages append to and the terminal drains.
func (c *Chain) HandlePosition(ctx context.Context, position *model.Position) error {
	return c.step(eventctx.WithBuffer(ctx), 0, position)
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
