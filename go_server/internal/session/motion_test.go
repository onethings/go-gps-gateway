package session

import (
	"context"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/handler"
	"traccar-server_go/go_server/internal/model"
)

var baseTime = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// position builds a located position at a given offset from baseTime.
func position(deviceID int64, offset time.Duration, latitude, longitude, speed float64) *model.Position {
	return &model.Position{
		DeviceID:   deviceID,
		Time:       baseTime.Add(offset),
		Valid:      true,
		Latitude:   latitude,
		Longitude:  longitude,
		Speed:      speed,
		Attributes: map[string]any{},
	}
}

func TestMotionStartsOnSpeedAndStopsAfterTheInterval(t *testing.T) {
	motion := NewMotion(MotionConfig{Interval: time.Minute})

	if event := motion.Update(position(1, 0, 0, 0, 0)); event != MotionNone {
		t.Fatalf("a standing device reported %v", event)
	}
	if event := motion.Update(position(1, time.Second, 0, 0, 12)); event != MotionStarted {
		t.Fatalf("a fast position reported %v, want %v", event, MotionStarted)
	}
	if event := motion.Update(position(1, 2*time.Second, 0, 0, 12)); event != MotionNone {
		t.Fatalf("a still fast position reported %v, want none", event)
	}
	if !motion.Moving(1) {
		t.Error("device is not moving")
	}

	// Half the interval after the last movement it is still moving.
	if event := motion.Update(position(1, time.Minute, 0, 0, 0)); event != MotionNone {
		t.Fatalf("a short stop reported %v, want none", event)
	}
	// Past the interval it stops.
	if event := motion.Update(position(1, 2*time.Minute, 0, 0, 0)); event != MotionStopped {
		t.Fatalf("a long stop reported %v, want %v", event, MotionStopped)
	}
	if motion.Moving(1) {
		t.Error("device is still moving")
	}
}

// TestMotionDistanceRule covers the Java server's motionThreshold: a device
// below the speed threshold that clearly changed place still counts as moving,
// which is what a poor GPS speed reading looks like.
func TestMotionDistanceRule(t *testing.T) {
	motion := NewMotion(MotionConfig{Interval: time.Minute, DistanceThreshold: 100})

	motion.Update(position(2, 0, 10.0000, 20.0000, 0))
	// About 111 meters north after ten seconds, reported speed still zero.
	event := motion.Update(position(2, 10*time.Second, 10.0010, 20.0000, 0))

	if event != MotionStarted {
		t.Fatalf("a device that moved 111 m reported %v, want %v", event, MotionStarted)
	}
}

func TestMotionIgnoresSmallJitter(t *testing.T) {
	motion := NewMotion(MotionConfig{Interval: time.Minute, DistanceThreshold: 100})

	motion.Update(position(3, 0, 10.0000, 20.0000, 0))
	// About 11 meters: a standing device with a noisy fix.
	event := motion.Update(position(3, 10*time.Second, 10.0001, 20.0000, 0))

	if event != MotionNone {
		t.Fatalf("11 m of jitter reported %v, want none", event)
	}
}

func TestMotionPrefersTheDeviceReportedFlag(t *testing.T) {
	motion := NewMotion(MotionConfig{Interval: time.Minute})

	parked := position(4, 0, 0, 0, 30)
	parked.Set(AttrMotion, false)
	if event := motion.Update(parked); event != MotionNone {
		t.Fatalf("a device reporting no motion reported %v", event)
	}

	rolling := position(4, time.Second, 0, 0, 0)
	rolling.Set(AttrMotion, true)
	if event := motion.Update(rolling); event != MotionStarted {
		t.Fatalf("a device reporting motion reported %v, want %v", event, MotionStarted)
	}
}

func TestMotionIgnoresPositionsWithoutTime(t *testing.T) {
	motion := NewMotion(MotionConfig{})

	if event := motion.Update(&model.Position{DeviceID: 5, Speed: 40}); event != MotionNone {
		t.Errorf("a position without a time reported %v", event)
	}
	if event := motion.Update(nil); event != MotionNone {
		t.Errorf("nil reported %v", event)
	}
}

// TestMotionStageRecordsEventsAndPassesThePositionOn is the chain contract: the
// stage writes the event and the next stage still sees the position.
func TestMotionStageRecordsEventsAndPassesThePositionOn(t *testing.T) {
	events := &eventRecorder{}
	motion := NewMotion(MotionConfig{Interval: time.Minute, Events: events})

	chain, err := handler.New(handler.Config{
		Stages: map[string]handler.PositionHandler{handler.StageMotion: motion},
		Store:  recorderStore{},
	})
	if err != nil {
		t.Fatalf("new chain: %v", err)
	}

	for _, p := range []*model.Position{
		position(6, 0, 0, 0, 0),
		position(6, time.Second, 0, 0, 15),
		position(6, 2*time.Minute, 0, 0, 0),
	} {
		if err := chain.HandlePosition(context.Background(), p); err != nil {
			t.Fatalf("handle: %v", err)
		}
	}

	types := events.types()
	if len(types) != 2 {
		t.Fatalf("recorded %v, want a moving and a stopped event", types)
	}
	if types[0] != model.EventDeviceMoving || types[1] != model.EventDeviceStopped {
		t.Errorf("recorded %v, want moving then stopped", types)
	}
}

// recorderStore swallows the positions the chain passes on.
type recorderStore struct{}

func (recorderStore) Handle(*model.Position) error { return nil }
