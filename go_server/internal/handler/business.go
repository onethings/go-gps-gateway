package handler

import (
	"context"
	"errors"

	"traccar-server_go/go_server/internal/model"
)

// Processor consumes a position. gateway.Sink, the batch sink and the alert
// pipeline all fit it, which is what lets the chain hold them without each
// package knowing about the others.
type Processor interface {
	Handle(position *model.Position) error
}

// ContextProcessor is a Processor that can also see the processing context,
// which is where the event buffer lives. The alert pipeline implements it; a
// processor that does not is called through Handle and keeps its old behavior.
type ContextProcessor interface {
	HandleContext(ctx context.Context, position *model.Position) error
}

// BusinessHandler is the stage that runs a Processor. The gateway's own rules
// are the alert pipeline today; in the Java server the same work is spread over
// OverspeedHandler, GeofenceHandler and AlertEventHandler.
type BusinessHandler struct {
	// Stage is the stage name, StageBusiness for the alert pipeline.
	Stage string
	// Processor is the stage itself. A nil processor makes the stage a
	// placeholder that passes the position on.
	Processor Processor
}

// Name implements PositionHandler.
func (b BusinessHandler) Name() string { return b.Stage }

// HandlePosition implements PositionHandler. A failing processor does not stop
// the position: the alert pipeline reporting a full queue must not also cost the
// position its database row. Both outcomes are reported, so the caller logs the
// rule failure and the store failure instead of only the last one.
func (b BusinessHandler) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	var processorErr error
	if b.Processor != nil {
		if contextual, ok := b.Processor.(ContextProcessor); ok {
			processorErr = contextual.HandleContext(ctx, position)
		} else {
			processorErr = b.Processor.Handle(position)
		}
	}
	return errors.Join(processorErr, next(ctx, position))
}

// StoreOf adapts a position sink to the terminal of a chain.
func StoreOf(sink Processor) Next {
	if sink == nil {
		return nil
	}
	return func(_ context.Context, position *model.Position) error {
		return sink.Handle(position)
	}
}
