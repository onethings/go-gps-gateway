// Package eventctx carries the events a processing stage raised for one
// position to the stage that writes the position. The Java server lets every
// event handler call a callback that DatabaseHandler collects; the chain here
// passes a buffer through the context instead, so a stage stays a plain
// function of the position.
package eventctx

import (
	"context"

	"traccar-server_go/go_server/internal/model"
)

type bufferKey struct{}

// buffer collects the events of one position in processing order.
type buffer struct {
	events []*model.Event
}

// WithBuffer returns a context that can collect events. Chain creates it once
// per position.
func WithBuffer(ctx context.Context) context.Context {
	return context.WithValue(ctx, bufferKey{}, &buffer{})
}

// Append adds events to the buffer of ctx. A context without a buffer (a stage
// used outside a chain) drops them, which keeps a rule harmless on its own.
func Append(ctx context.Context, events ...*model.Event) {
	if len(events) == 0 {
		return
	}
	buf, ok := ctx.Value(bufferKey{}).(*buffer)
	if !ok || buf == nil {
		return
	}
	buf.events = append(buf.events, events...)
}

// Events returns the events collected so far, in the order they were raised.
func Events(ctx context.Context) []*model.Event {
	buf, ok := ctx.Value(bufferKey{}).(*buffer)
	if !ok || buf == nil {
		return nil
	}
	return buf.events
}
