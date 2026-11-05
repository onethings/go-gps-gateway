package session

import (
	"context"
	"math"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/handler"
	"traccar-server_go/go_server/internal/model"
)

// Defaults of the new motion logic, matching Keys.REPORT_TRIP_* in the
// reference: 200 m of movement starts a trip, 180 s without it ends one, and a
// gap longer than an hour is split by average speed.
const (
	DefaultMovementDistance = 200.0
	DefaultMovementDuration = 180 * time.Second
	DefaultMovementStopGap  = 3600 * time.Second
)

// MovementConfig holds the thresholds and the event sink of the new logic.
type MovementConfig struct {
	// MinDistance is how far the device must travel to count as moving, in
	// metres. Zero uses DefaultMovementDistance.
	MinDistance float64
	// MinDuration is how long the device must stay within MinDistance to count
	// as stopped. Zero uses DefaultMovementDuration.
	MinDuration time.Duration
	// StopGap is the gap that is split by average speed instead of by distance.
	// Zero uses DefaultMovementStopGap.
	StopGap time.Duration
	// UseIgnition reads the ignition input instead of the travelled distance
	// whenever a position carries it.
	UseIgnition bool
	// Events records the deviceMoving and deviceStopped rows. Optional.
	Events Events
	// Logger reports a failed event write.
	Logger func(format string, args ...any)
}

// Movement is the moving / stopped state machine behind report.trip.newLogic.
// It ports NewMotionProcessor, which the reference uses both for live events and
// for trip reports: an ignition edge wins, and otherwise a movement streak
// decided by distance and duration does.
//
// It implements handler.PositionHandler for the motion stage, like Motion does,
// so the gateway runs one or the other without touching the chain.
type Movement struct {
	config MovementConfig
	logf   func(format string, args ...any)

	mu     sync.Mutex
	states map[int64]*movementState
}

// movementState is what the machine remembers about one device.
type movementState struct {
	// streak is true while the device counts as moving.
	streak bool
	// eventTime and eventLatitude/Longitude are where the current streak
	// started, which is what the retroactive start position is measured from.
	eventTime                     time.Time
	eventLatitude, eventLongitude float64
	// positions is the recent history, oldest first.
	positions []*model.Position
}

// MovementTransition is one event the state machine raised, with the position
// that belongs in it. A gap can raise three of them from a single position.
type MovementTransition struct {
	Type     string
	Time     time.Time
	Position *model.Position
}

// NewMovement creates the state machine. Every threshold has a default, so the
// zero config is usable.
func NewMovement(config MovementConfig) *Movement {
	if config.MinDistance <= 0 {
		config.MinDistance = DefaultMovementDistance
	}
	if config.MinDuration <= 0 {
		config.MinDuration = DefaultMovementDuration
	}
	if config.StopGap <= 0 {
		config.StopGap = DefaultMovementStopGap
	}
	if config.Logger == nil {
		config.Logger = func(string, ...any) {}
	}
	return &Movement{
		config: config,
		logf:   config.Logger,
		states: make(map[int64]*movementState),
	}
}

// Name implements handler.PositionHandler.
func (m *Movement) Name() string { return handler.StageMotion }

// HandlePosition runs the state machine, records the transitions it caused and
// hands the position on.
func (m *Movement) HandlePosition(ctx context.Context, position *model.Position, next handler.Next) error {
	m.record(ctx, m.Update(position))
	return next(ctx, position)
}

// Update processes one position and returns the transitions it caused, oldest
// first. It takes ownership of nothing: the caller keeps its position.
func (m *Movement) Update(position *model.Position) []MovementTransition {
	if position == nil || position.DeviceID == 0 || position.Time.IsZero() {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state := m.states[position.DeviceID]
	if state == nil {
		state = &movementState{eventTime: position.Time}
		state.eventLatitude = position.Latitude
		state.eventLongitude = position.Longitude
		m.states[position.DeviceID] = state
	}
	defer m.remember(state, position)

	var transitions []MovementTransition

	if m.config.UseIgnition {
		if raw := position.Attributes[model.KeyIgnition]; raw != nil {
			ignition, ok := raw.(bool)
			if ok {
				if state.streak != ignition {
					state.streak = ignition
					transitions = append(transitions, m.transition(state, ignition, position))
				}
				return transitions
			}
		}
	}

	if len(state.positions) == 0 {
		return nil
	}

	minAverageSpeed := m.config.MinDistance / m.config.MinDuration.Seconds()

	last := state.positions[len(state.positions)-1]
	gap := position.Time.Sub(last.Time)
	gapDistance := position.Attributes.GetDouble(model.KeyDistance, 0)
	gapHasSpeed := gap > 0
	gapAverageSpeed := math.NaN()
	if gapHasSpeed {
		gapAverageSpeed = gapDistance / gap.Seconds()
	}
	if gap > m.config.StopGap && gapDistance >= m.config.MinDistance &&
		gapHasSpeed && gapAverageSpeed > minAverageSpeed {
		if state.streak {
			transitions = append(transitions, m.transition(state, false, last))
		}
		transitions = append(transitions, m.transition(state, true, last))
		transitions = append(transitions, m.transition(state, false, position))
		state.streak = false
		return transitions
	}

	if state.streak {
		for index := len(state.positions) - 1; index >= 0; index-- {
			candidate := state.positions[index]
			if distanceMeters(candidate.Latitude, candidate.Longitude,
				position.Latitude, position.Longitude) >= m.config.MinDistance {
				return transitions
			}
		}

		oldest := state.positions[0]
		if position.Time.Sub(oldest.Time) >= m.config.MinDuration {
			state.streak = false
			transitions = append(transitions, m.transition(state, false, position))
		}
		return transitions
	}

	if distanceMeters(state.eventLatitude, state.eventLongitude,
		position.Latitude, position.Longitude) < m.config.MinDistance {
		return transitions
	}

	state.streak = true
	start := position
	next := position
	for index := len(state.positions) - 1; index >= 0; index-- {
		candidate := state.positions[index]
		beforeStop := !candidate.Time.After(state.eventTime)
		legDuration := next.Time.Sub(candidate.Time)
		legMoving := legDuration > 0 &&
			distanceMeters(candidate.Latitude, candidate.Longitude,
				next.Latitude, next.Longitude)/legDuration.Seconds() > minAverageSpeed
		if beforeStop || (next != position && !legMoving) {
			break
		}
		start = candidate
		next = candidate
	}
	transitions = append(transitions, m.transition(state, true, start))
	return transitions
}

// Moving reports whether the device is moving according to the last position.
func (m *Movement) Moving(deviceID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := m.states[deviceID]
	return state != nil && state.streak
}

// transition builds one event and moves the streak anchor to its position.
func (m *Movement) transition(
	state *movementState, moving bool, position *model.Position,
) MovementTransition {
	eventType := model.EventDeviceMoving
	if !moving {
		eventType = model.EventDeviceStopped
	}
	state.eventTime = position.Time
	state.eventLatitude = position.Latitude
	state.eventLongitude = position.Longitude
	return MovementTransition{Type: eventType, Time: position.Time, Position: position}
}

// remember appends the position and drops history older than MinDuration, which
// is the window the streak tests can look back into.
func (m *Movement) remember(state *movementState, position *model.Position) {
	state.positions = append(state.positions, position)

	cutoff := position.Time.Add(-m.config.MinDuration)
	first := 0
	for first < len(state.positions)-1 && state.positions[first].Time.Before(cutoff) {
		first++
	}
	if first > 0 {
		state.positions = append([]*model.Position(nil), state.positions[first:]...)
	}
}

// record writes the transitions as events.
func (m *Movement) record(ctx context.Context, transitions []MovementTransition) {
	if len(transitions) == 0 || m.config.Events == nil {
		return
	}

	rows := make([]*model.Event, 0, len(transitions))
	for _, transition := range transitions {
		row := model.NewEvent(transition.Type, transition.Position.DeviceID, transition.Time)
		row.PositionID = transition.Position.ID
		row.Set("speed", transition.Position.Speed)
		rows = append(rows, row)
	}
	if err := m.config.Events.InsertEvents(ctx, rows); err != nil {
		m.logf("session: motion events for device %d: %v", transitions[0].Position.DeviceID, err)
	}
}
