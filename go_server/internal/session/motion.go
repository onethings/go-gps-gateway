package session

import (
	"context"
	"math"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/handler"
	"traccar-server_go/go_server/internal/model"
)

// AttrMotion is the attribute a protocol uses when it reports the motion sensor
// itself. When it is present it wins over the computed state, which is what the
// Java server's MotionHandler does with Position.KEY_MOTION.
const AttrMotion = "motion"

// Default thresholds of the state machine.
const (
	// DefaultSpeedThreshold is in knots: faster than this means moving. The
	// value is the Java server's speedThreshold default, low enough that a
	// walking device counts.
	DefaultSpeedThreshold = 0.01
	// DefaultDistanceThreshold is in meters: a device that moves farther than
	// this within Interval counts as moving even when its speed reading lags,
	// which is the Java server's motionThreshold default.
	DefaultDistanceThreshold = 500
	// DefaultMotionInterval is how long a device has to be quiet before it
	// counts as stopped, the Java server's motionInterval default.
	DefaultMotionInterval = 60 * time.Second
)

// MotionEvent is what the state machine decided for one position.
type MotionEvent int

const (
	// MotionNone means the device kept the state it had.
	MotionNone MotionEvent = iota
	// MotionStarted means the device was standing still and is moving now.
	MotionStarted
	// MotionStopped means the device was moving and is standing still now.
	MotionStopped
)

// String implements fmt.Stringer.
func (e MotionEvent) String() string {
	switch e {
	case MotionStarted:
		return "started"
	case MotionStopped:
		return "stopped"
	default:
		return "none"
	}
}

// MotionConfig holds the thresholds and the event sink of the state machine.
type MotionConfig struct {
	// SpeedThreshold is in knots; zero uses DefaultSpeedThreshold.
	SpeedThreshold float64
	// DistanceThreshold is in meters; zero uses DefaultDistanceThreshold.
	DistanceThreshold float64
	// Interval is how long the device has to be quiet; zero uses
	// DefaultMotionInterval.
	Interval time.Duration
	// Events records the deviceMoving and deviceStopped rows. Optional.
	Events Events
	// Logger reports a failed event write.
	Logger func(format string, args ...any)
}

// Motion is the moving / stopped state machine. It is the Go counterpart of the
// Java server's session/state/MotionProcessor: the state per device lives here,
// the handler stage that uses it is HandlePosition.
type Motion struct {
	config MotionConfig
	logf   func(format string, args ...any)

	mu     sync.Mutex
	states map[int64]*motionState
}

// motionState is what the machine remembers about one device.
type motionState struct {
	moving bool
	// since is when the current state began, zero until the first transition.
	since time.Time
	// movedAt is the time of the last position that looked like movement.
	movedAt time.Time
	// anchor is the position the distance test measures from.
	anchor *motionAnchor
}

type motionAnchor struct {
	time                time.Time
	latitude, longitude float64
}

// NewMotion creates the state machine. Every threshold has a default, so the
// zero config is usable.
func NewMotion(config MotionConfig) *Motion {
	if config.SpeedThreshold <= 0 {
		config.SpeedThreshold = DefaultSpeedThreshold
	}
	if config.DistanceThreshold <= 0 {
		config.DistanceThreshold = DefaultDistanceThreshold
	}
	if config.Interval <= 0 {
		config.Interval = DefaultMotionInterval
	}
	if config.Logger == nil {
		config.Logger = func(string, ...any) {}
	}
	return &Motion{
		config: config,
		logf:   config.Logger,
		states: make(map[int64]*motionState),
	}
}

// Name implements handler.PositionHandler, which is what lets the gateway plug
// the state machine into the motion stage of the processing chain.
func (m *Motion) Name() string { return handler.StageMotion }

// HandlePosition runs the state machine, records the transition it caused and
// hands the position on.
func (m *Motion) HandlePosition(ctx context.Context, position *model.Position, next handler.Next) error {
	if event := m.Update(position); event != MotionNone {
		m.record(ctx, event, position)
	}
	return next(ctx, position)
}

// Update processes one position and reports the transition it caused.
func (m *Motion) Update(position *model.Position) MotionEvent {
	if position == nil || position.DeviceID == 0 || position.Time.IsZero() {
		return MotionNone
	}
	at := position.Time

	m.mu.Lock()
	defer m.mu.Unlock()

	state := m.states[position.DeviceID]
	if state == nil {
		state = &motionState{}
		m.states[position.DeviceID] = state
	}

	speeding := position.Speed > m.config.SpeedThreshold

	farAway := false
	if anchor := state.anchor; anchor != nil && !speeding {
		elapsed := at.Sub(anchor.time)
		farAway = elapsed > 0 && elapsed <= m.config.Interval &&
			distanceMeters(anchor.latitude, anchor.longitude, position.Latitude, position.Longitude) >
				m.config.DistanceThreshold
	}

	moved := speeding || farAway
	if value, ok := position.Attributes[AttrMotion]; ok {
		if reported, ok := value.(bool); ok {
			moved = reported
		}
	}
	if moved {
		state.movedAt = at
	}

	// The anchor moves with the device so the distance test keeps measuring a
	// recent interval, and is re-taken after a quiet gap so a device that comes
	// back somewhere else is not mistaken for one that travelled the distance.
	if anchor := state.anchor; anchor == nil || moved || at.Sub(anchor.time) > m.config.Interval {
		state.anchor = &motionAnchor{
			time:      at,
			latitude:  position.Latitude,
			longitude: position.Longitude,
		}
	}

	previous := state.moving
	switch {
	case moved:
		state.moving = true
	case state.moving && at.Sub(state.movedAt) > m.config.Interval:
		state.moving = false
	}

	event := MotionNone
	if state.moving != previous {
		event = MotionStopped
		if state.moving {
			event = MotionStarted
		}
		state.since = at
		m.logf("session: device %d %s", position.DeviceID, event)
	}
	return event
}

// Moving reports whether the device is moving according to the last position.
func (m *Motion) Moving(deviceID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := m.states[deviceID]
	return state != nil && state.moving
}

// record writes the event of a transition.
func (m *Motion) record(ctx context.Context, event MotionEvent, position *model.Position) {
	if m.config.Events == nil {
		return
	}

	eventType := model.EventDeviceMoving
	if event == MotionStopped {
		eventType = model.EventDeviceStopped
	}

	row := model.NewEvent(eventType, position.DeviceID, position.Time)
	row.Set("speed", position.Speed)
	if err := m.config.Events.InsertEvents(ctx, []*model.Event{row}); err != nil {
		m.logf("session: %s event for device %d: %v", event, position.DeviceID, err)
	}
}

// distanceMeters is the great circle distance between two points, which is what
// the Java server's DistanceCalculator returns for the motion threshold.
func distanceMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadius = 6371000.0

	phi1 := lat1 * math.Pi / 180
	phi2 := lat2 * math.Pi / 180
	deltaPhi := (lat2 - lat1) * math.Pi / 180
	deltaLambda := (lon2 - lon1) * math.Pi / 180

	a := math.Sin(deltaPhi/2)*math.Sin(deltaPhi/2) +
		math.Cos(phi1)*math.Cos(phi2)*math.Sin(deltaLambda/2)*math.Sin(deltaLambda/2)
	return 2 * earthRadius * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
