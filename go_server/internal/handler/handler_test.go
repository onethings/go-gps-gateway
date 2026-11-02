package handler

import (
	"context"
	"errors"
	"testing"

	"traccar-server_go/go_server/internal/model"
)

// TestHandlerOrder pins the stage order to the Java server's ProcessingHandler.
// The expected list is written out on purpose: comparing against Order itself
// would pass after any reorder.
func TestHandlerOrder(t *testing.T) {
	want := []string{
		"distance",
		"filter",
		"defaultData",
		"position",
		"motion",
		"mapMatcher",
		"geofence",
		"computedAttributes",
		"speedLimit",
		"alertEvent",
		"business",
		"fuel",
		"driver",
		"maintenance",
		"media",
	}

	chain, err := New(Config{Alerts: &recorder{}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	got := chain.Names()
	if len(got) != len(want) {
		t.Fatalf("chain has %d stages (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("stage %d is %q, want %q (full order %v)", index, got[index], want[index], got)
		}
	}
}

// TestBusinessStageSitsWhereTheAlertsBelong checks the one stage that is real:
// it must run after the geofence and speed limit work and before the stages
// that only annotate the position.
func TestBusinessStageSitsWhereTheAlertsBelong(t *testing.T) {
	chain, err := New(Config{Alerts: &recorder{}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	position := func(name string) int {
		for index, stage := range chain.Names() {
			if stage == name {
				return index
			}
		}
		t.Fatalf("stage %q is missing from %v", name, chain.Names())
		return -1
	}

	if position(StageGeofence) > position(StageBusiness) {
		t.Error("business runs before the geofence stage")
	}
	if position(StageAlertEvent) > position(StageBusiness) {
		t.Error("business runs before the device reported alerts")
	}
	if position(StageBusiness) > position(StageMedia) {
		t.Error("business runs after the media stage")
	}
}

func TestChainRunsEveryStageThenTheStore(t *testing.T) {
	var seen []string

	stage := func(name string) PositionHandler {
		return HandlerFunc{Stage: name, Fn: func(
			ctx context.Context, position *model.Position, next Next,
		) error {
			seen = append(seen, name)
			return next(ctx, position)
		}}
	}

	store := ProcessorFunc(func(position *model.Position) error {
		seen = append(seen, "store")
		return nil
	})

	chain, err := New(Config{
		Store: store,
		Stages: map[string]PositionHandler{
			StageMotion:   stage(StageMotion),
			StageGeofence: stage(StageGeofence),
		},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	if err := chain.Handle(&model.Position{DeviceID: 1}); err != nil {
		t.Fatalf("handle: %v", err)
	}

	want := []string{StageMotion, StageGeofence, "store"}
	if len(seen) != len(want) {
		t.Fatalf("ran %v, want %v", seen, want)
	}
	for index := range want {
		if seen[index] != want[index] {
			t.Fatalf("ran %v, want %v", seen, want)
		}
	}
}

func TestStageCanDropThePosition(t *testing.T) {
	storeCalled := false
	store := ProcessorFunc(func(position *model.Position) error {
		storeCalled = true
		return nil
	})

	chain, err := New(Config{
		Store: store,
		Stages: map[string]PositionHandler{
			StageFilter: HandlerFunc{Stage: StageFilter, Fn: func(
				context.Context, *model.Position, Next,
			) error {
				return nil
			}},
		},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	if err := chain.Handle(&model.Position{DeviceID: 1}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if storeCalled {
		t.Error("a dropped position reached the store")
	}
}

// TestBusinessStageDoesNotSwallowTheStoreError checks that a queue that reports
// itself full still surfaces: the position after all is what the caller logs.
func TestBusinessStageDoesNotSwallowTheStoreError(t *testing.T) {
	full := errors.New("sink full")
	alerts := recorder{}

	chain, err := New(Config{Alerts: &alerts, Store: ProcessorFunc(
		func(*model.Position) error { return full },
	)})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	if err := chain.Handle(&model.Position{DeviceID: 1}); !errors.Is(err, full) {
		t.Fatalf("handle returned %v, want %v", err, full)
	}
	if !alerts.called {
		t.Error("the alert stage was skipped")
	}
}

func TestUnknownStageNameIsRejected(t *testing.T) {
	_, err := New(Config{Stages: map[string]PositionHandler{
		"motionn": Placeholder{Stage: "motionn"},
	}})
	if err == nil {
		t.Fatal("want an error for a stage that is not declared")
	}
}

func TestOverriddenStageKeepsItsSlot(t *testing.T) {
	var ran string

	chain, err := New(Config{Stages: map[string]PositionHandler{
		StageMotion: HandlerFunc{Stage: StageMotion, Fn: func(
			ctx context.Context, position *model.Position, next Next,
		) error {
			ran = StageMotion
			return next(ctx, position)
		}},
	}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// The override must not be appended after the placeholders.
	if chain.Names()[4] != StageMotion {
		t.Fatalf("stage 4 is %q, want %q", chain.Names()[4], StageMotion)
	}
	if err := chain.Handle(&model.Position{DeviceID: 1}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if ran != StageMotion {
		t.Errorf("override ran %q, want %q", ran, StageMotion)
	}
}

// recorder is the alert stage stand-in: it remembers that it was asked.
type recorder struct{ called bool }

func (r *recorder) Handle(*model.Position) error {
	r.called = true
	return nil
}

// ProcessorFunc adapts a function to Processor.
type ProcessorFunc func(position *model.Position) error

func (f ProcessorFunc) Handle(position *model.Position) error { return f(position) }
