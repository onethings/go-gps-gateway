package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

type eventSink struct {
	mu     sync.Mutex
	events []*model.Event
}

func (s *eventSink) InsertEvents(_ context.Context, events []*model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, events...)
	return nil
}

func (s *eventSink) snapshot() []*model.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*model.Event(nil), s.events...)
}

func TestMovementStartsAfterMinDistance(t *testing.T) {
	movement := NewMovement(MovementConfig{MinDistance: 200, MinDuration: 180 * time.Second})

	if got := movement.Update(position(1, 0, 0, 0, 0)); len(got) != 0 {
		t.Fatalf("the first position raised %v", got)
	}
	// 0.002 degrees of longitude at the equator is about 222 m.
	transitions := movement.Update(position(1, time.Minute, 0, 0.002, 10))
	if len(transitions) != 1 || transitions[0].Type != model.EventDeviceMoving {
		t.Fatalf("transitions = %+v, want one deviceMoving", transitions)
	}
	if !movement.Moving(1) {
		t.Error("device is not moving")
	}
}

func TestMovementIgnoresShortHop(t *testing.T) {
	movement := NewMovement(MovementConfig{MinDistance: 200, MinDuration: 180 * time.Second})

	movement.Update(position(1, 0, 0, 0, 0))
	// 0.001 degrees is about 111 m, under the 200 m threshold.
	if got := movement.Update(position(1, time.Minute, 0, 0.001, 10)); len(got) != 0 {
		t.Fatalf("a short hop raised %v", got)
	}
	if movement.Moving(1) {
		t.Error("device should still be stopped")
	}
}

func TestMovementStopsAfterMinDuration(t *testing.T) {
	movement := NewMovement(MovementConfig{MinDistance: 200, MinDuration: 180 * time.Second})

	movement.Update(position(1, 0, 0, 0, 0))
	movement.Update(position(1, time.Minute, 0, 0.002, 10))

	// Positions that stay put: each is within 200 m of every recent position.
	movement.Update(position(1, 160*time.Second, 0, 0.002, 0))
	movement.Update(position(1, 200*time.Second, 0, 0.002, 0))
	transitions := movement.Update(position(1, 250*time.Second, 0, 0.002, 0))
	if len(transitions) != 1 || transitions[0].Type != model.EventDeviceStopped {
		t.Fatalf("transitions = %+v, want one deviceStopped", transitions)
	}
	if movement.Moving(1) {
		t.Error("device should be stopped now")
	}
}

func TestMovementSplitsALongGapByAverageSpeed(t *testing.T) {
	movement := NewMovement(MovementConfig{
		MinDistance: 200,
		MinDuration: 180 * time.Second,
		StopGap:     3600 * time.Second,
	})

	movement.Update(position(1, 0, 0, 0, 0))
	moving := position(1, time.Minute, 0, 0.002, 10)
	if got := movement.Update(moving); len(got) != 1 {
		t.Fatalf("the movement did not start: %+v", got)
	}

	// A little over an hour later, far enough that the average speed says the
	// device travelled: the reference closes the old trip and opens a new one.
	after := position(1, 62*time.Minute, 0, 0.1, 10)
	after.Set(model.KeyDistance, 10000.0)
	transitions := movement.Update(after)
	if len(transitions) != 3 {
		t.Fatalf("transitions = %+v, want stopped, moving, stopped", transitions)
	}
	wantTypes := []string{model.EventDeviceStopped, model.EventDeviceMoving, model.EventDeviceStopped}
	for index, want := range wantTypes {
		if transitions[index].Type != want {
			t.Errorf("transition %d = %q, want %q", index, transitions[index].Type, want)
		}
	}
	if !transitions[0].Time.Equal(moving.Time) || !transitions[1].Time.Equal(moving.Time) {
		t.Error("the closed trip should be stamped with the last position before the gap")
	}
	if !transitions[2].Time.Equal(after.Time) {
		t.Error("the new trip should be stamped with the position after the gap")
	}
	if movement.Moving(1) {
		t.Error("the gap should have left the device stopped")
	}
}

func TestMovementIGapWithoutSpeedKeepsTheStreak(t *testing.T) {
	movement := NewMovement(MovementConfig{
		MinDistance: 200,
		MinDuration: 180 * time.Second,
		StopGap:     3600 * time.Second,
	})

	movement.Update(position(1, 0, 0, 0, 0))
	movement.Update(position(1, time.Minute, 0, 0.002, 10))

	// The distance attribute is missing, so the gap cannot be judged by speed
	// and the streak survives.
	after := position(1, 62*time.Minute, 0, 0.1, 10)
	if got := movement.Update(after); len(got) != 0 {
		t.Fatalf("transitions = %+v, want none", got)
	}
	if !movement.Moving(1) {
		t.Error("the streak should survive a gap nothing can judge")
	}
}

func TestMovementUsesIgnitionWhenAvailable(t *testing.T) {
	movement := NewMovement(MovementConfig{
		MinDistance: 200,
		MinDuration: 180 * time.Second,
		UseIgnition: true,
	})

	on := position(1, 0, 0, 0, 0)
	on.Set(model.KeyIgnition, true)
	transitions := movement.Update(on)
	if len(transitions) != 1 || transitions[0].Type != model.EventDeviceMoving {
		t.Fatalf("ignition on raised %+v, want one deviceMoving", transitions)
	}

	off := position(1, time.Minute, 0, 0, 0)
	off.Set(model.KeyIgnition, false)
	transitions = movement.Update(off)
	if len(transitions) != 1 || transitions[0].Type != model.EventDeviceStopped {
		t.Fatalf("ignition off raised %+v, want one deviceStopped", transitions)
	}
}

func TestMovementFallsBackToDistanceWithoutIgnition(t *testing.T) {
	movement := NewMovement(MovementConfig{
		MinDistance: 200,
		MinDuration: 180 * time.Second,
		UseIgnition: true,
	})

	movement.Update(position(1, 0, 0, 0, 0))
	if got := movement.Update(position(1, time.Minute, 0, 0.002, 10)); len(got) != 1 {
		t.Fatalf("a position without ignition raised %+v, want the distance rule", got)
	}
}

func TestMovementRecordsEveryTransition(t *testing.T) {
	sink := &eventSink{}
	movement := NewMovement(MovementConfig{
		MinDistance: 200,
		MinDuration: 180 * time.Second,
		Events:      sink,
	})

	movement.Update(position(1, 0, 0, 0, 0))
	transitions := movement.Update(position(1, time.Minute, 0, 0.002, 10))
	if len(transitions) != 1 {
		t.Fatalf("transitions = %+v", transitions)
	}
	movement.record(context.Background(), transitions)

	events := sink.snapshot()
	if len(events) != 1 || events[0].Type != model.EventDeviceMoving || events[0].DeviceID != 1 {
		t.Fatalf("events = %+v", events)
	}
}
