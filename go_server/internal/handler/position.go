package handler

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/expr"
	"traccar-server_go/go_server/internal/model"
)

// Position attribute keys the stages below read and write, matching the
// Position.KEY_* constants of the Java model that have no Go counterpart yet.
const (
	KeyHemisphere  = "hemisphere"
	KeyAddress     = "address"
	KeyApproximate = "approximate"
)

// TimeHandler stamps a position with the server time and falls back to that
// time when the frame carried no device time. It mirrors the Java TimeHandler.
type TimeHandler struct {
	// Stage is the stage name, StageDefaultData.
	Stage string
	// Now is the clock. Nil uses time.Now.
	Now func() time.Time
}

// Name implements PositionHandler.
func (h TimeHandler) Name() string { return h.Stage }

// HandlePosition implements PositionHandler.
func (h TimeHandler) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	if position != nil {
		now := time.Now()
		if h.Now != nil {
			now = h.Now()
		}
		if position.ServerTime.IsZero() {
			position.ServerTime = now
		}
		if position.Time.IsZero() {
			position.Time = position.ServerTime
		}
	}
	return next(ctx, position)
}

// HemisphereHandler turns the hemisphere a protocol reports as a letter into
// the sign of the coordinate. Protocols that send absolute values plus N/S and
// E/W otherwise store every southern or western fix in the wrong place.
type HemisphereHandler struct {
	// Stage is the stage name, StageDefaultData.
	Stage string
}

// Name implements PositionHandler.
func (h HemisphereHandler) Name() string { return h.Stage }

// HandlePosition implements PositionHandler.
func (h HemisphereHandler) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	if position != nil && position.Attributes != nil {
		if value, ok := position.Attributes[KeyHemisphere]; ok {
			hemisphere := strings.ToUpper(fmt.Sprint(value))
			latitude := math.Abs(position.Latitude)
			longitude := math.Abs(position.Longitude)
			if strings.Contains(hemisphere, "S") {
				latitude = -latitude
			}
			if strings.Contains(hemisphere, "W") {
				longitude = -longitude
			}
			position.Latitude = latitude
			position.Longitude = longitude
			delete(position.Attributes, KeyHemisphere)
		}
	}
	return next(ctx, position)
}

// CopyAttributesHandler copies attributes the device did not report this time
// from its previous position. A protocol that only sends an alarm on the frame
// where it changed, for example, keeps the ignition state readable on every
// later position.
type CopyAttributesHandler struct {
	// Stage is the stage name this handler is installed in.
	Stage string
	// Attributes lists the keys to copy.
	Attributes []string
	// LastPosition returns the newest stored position of a device.
	LastPosition func(deviceID int64) *model.Position
}

// Name implements PositionHandler.
func (h CopyAttributesHandler) Name() string { return h.Stage }

// HandlePosition implements PositionHandler.
func (h CopyAttributesHandler) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	if position != nil && h.LastPosition != nil && len(h.Attributes) > 0 {
		previous := h.LastPosition(position.DeviceID)
		if previous != nil {
			for _, key := range h.Attributes {
				if position.HasAttribute(key) {
					continue
				}
				if value, ok := previous.Attributes[key]; ok {
					position.Set(key, value)
				}
			}
		}
	}
	return next(ctx, position)
}

// AddressResolver resolves a coordinate to a human readable address. The
// geographic package implements it.
type AddressResolver interface {
	Address(ctx context.Context, latitude, longitude float64) (string, error)
}

// GeocoderHandler stores the address of a valid position. A position with no
// fix, or one whose address is already known, is left alone.
type GeocoderHandler struct {
	// Stage is the stage name this handler is installed in.
	Stage string
	// Geocoder resolves the address. Nil disables the stage.
	Geocoder AddressResolver
	// Timeout caps one lookup. Zero uses the caller's context unchanged.
	Timeout time.Duration
	// Logger reports a failed lookup. Nil discards it.
	Logger func(format string, args ...any)
}

// Name implements PositionHandler.
func (h GeocoderHandler) Name() string { return h.Stage }

// HandlePosition implements PositionHandler.
func (h GeocoderHandler) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	if position != nil && h.Geocoder != nil && position.Valid &&
		position.Latitude != 0 && position.Longitude != 0 &&
		!position.HasAttribute(KeyAddress) {
		lookupCtx := ctx
		if h.Timeout > 0 {
			var cancel context.CancelFunc
			lookupCtx, cancel = context.WithTimeout(ctx, h.Timeout)
			defer cancel()
		}
		address, err := h.Geocoder.Address(lookupCtx, position.Latitude, position.Longitude)
		if err != nil {
			if h.Logger != nil {
				h.Logger("geocoder: %v", err)
			}
		} else if address != "" {
			position.Set(KeyAddress, address)
		}
	}
	return next(ctx, position)
}

// CellLocator resolves the network a device reported to a coordinate. The
// geographic package implements it.
type CellLocator interface {
	Locate(ctx context.Context, network *model.Network) (latitude, longitude float64, ok bool)
}

// GeolocationHandler fills the location of a position that carries only a cell
// tower description. The result is marked approximate, so a report can tell it
// apart from a real fix.
type GeolocationHandler struct {
	// Stage is the stage name this handler is installed in.
	Stage string
	// Locator resolves the network. Nil disables the stage.
	Locator CellLocator
}

// Name implements PositionHandler.
func (h GeolocationHandler) Name() string { return h.Stage }

// HandlePosition implements PositionHandler.
func (h GeolocationHandler) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	if position != nil && h.Locator != nil && !position.Valid && position.Network != nil &&
		!position.Network.IsEmpty() {
		if latitude, longitude, ok := h.Locator.Locate(ctx, position.Network); ok {
			position.Latitude = latitude
			position.Longitude = longitude
			position.Valid = true
			position.Set(KeyApproximate, true)
		}
	}
	return next(ctx, position)
}

// engineHours is the per-device accumulator of EngineHoursHandler.
type engineHours struct {
	lastTime     time.Time
	lastIgnition bool
	hours        float64
}

// EngineHoursHandler accumulates the time a device's ignition is on and
// writes it to the hours attribute. It is the Go counterpart of the Java
// EngineHoursHandler: a device that reports a counter keeps its value, and one
// that does not gets the accumulated total.
type EngineHoursHandler struct {
	// Stage is the stage name this handler is installed in.
	Stage string

	mu     sync.Mutex
	states map[int64]*engineHours
}

// Name implements PositionHandler.
func (h *EngineHoursHandler) Name() string { return h.Stage }

// HandlePosition implements PositionHandler.
func (h *EngineHoursHandler) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	if position != nil && position.DeviceID != 0 {
		h.accumulate(position)
	}
	return next(ctx, position)
}

func (h *EngineHoursHandler) accumulate(position *model.Position) {
	now := position.Time
	if now.IsZero() {
		now = position.ServerTime
	}
	ignition := position.Attributes.GetBoolean(model.KeyIgnition)

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.states == nil {
		h.states = make(map[int64]*engineHours)
	}
	state := h.states[position.DeviceID]
	if state == nil {
		state = &engineHours{
			hours:        position.Attributes.GetDouble(model.KeyHours, 0),
			lastIgnition: ignition,
			lastTime:     now,
		}
		h.states[position.DeviceID] = state
		return
	}

	if state.lastIgnition && ignition && !now.IsZero() && !state.lastTime.IsZero() {
		if elapsed := now.Sub(state.lastTime).Hours(); elapsed > 0 {
			state.hours += elapsed
		}
	}
	state.lastTime = now
	state.lastIgnition = ignition

	// A device that reports its own counter is authoritative; otherwise the
	// accumulated value is written so the reports and the accumulator route see
	// the same number.
	if position.HasAttribute(model.KeyHours) {
		state.hours = position.Attributes.GetDouble(model.KeyHours, state.hours)
		return
	}
	position.Set(model.KeyHours, state.hours)
}

// ComputedAttributesHandler evaluates the expressions configured in the
// attributes table and writes their results onto the position. Attributes are
// evaluated in priority order, so a later row can read the result of an
// earlier one.
type ComputedAttributesHandler struct {
	// Stage is the stage name, StageComputedAttributes.
	Stage string
	// Attributes returns the computed attributes to evaluate. Nil makes the
	// stage a no-op.
	Attributes func(deviceID int64) []*model.Attribute
	// Logger reports an expression that could not be evaluated. Nil discards
	// the message.
	Logger func(format string, args ...any)
}

// Name implements PositionHandler.
func (h ComputedAttributesHandler) Name() string { return h.Stage }

// HandlePosition implements PositionHandler.
func (h ComputedAttributesHandler) HandlePosition(ctx context.Context, position *model.Position, next Next) error {
	if position == nil || h.Attributes == nil {
		return next(ctx, position)
	}
	attributes := h.Attributes(position.DeviceID)
	if len(attributes) == 0 {
		return next(ctx, position)
	}
	ordered := append([]*model.Attribute(nil), attributes...)
	sort.SliceStable(ordered, func(left, right int) bool {
		if ordered[left] == nil || ordered[right] == nil {
			return ordered[right] == nil
		}
		if ordered[left].Priority != ordered[right].Priority {
			return ordered[left].Priority < ordered[right].Priority
		}
		return ordered[left].ID < ordered[right].ID
	})

	variables := PositionVariables(position)
	for _, attribute := range ordered {
		if attribute == nil || strings.TrimSpace(attribute.Attribute) == "" ||
			strings.TrimSpace(attribute.Expression) == "" {
			continue
		}
		value, err := expr.Eval(attribute.Expression, expr.Variables(variables))
		if err != nil {
			if h.Logger != nil {
				h.Logger("computed attribute %q: %v", attribute.Attribute, err)
			}
			continue
		}
		value = convertAttribute(value, attribute.Type)
		position.Set(attribute.Attribute, value)
		variables[attribute.Attribute] = value
	}
	return next(ctx, position)
}

// PositionVariables exposes the fields of a position and its attributes to an
// expression. It is exported so the computed attribute test route evaluates an
// expression exactly the way the processing chain does.
func PositionVariables(position *model.Position) map[string]any {
	variables := make(map[string]any, len(position.Attributes)+12)
	for key, value := range position.Attributes {
		variables[key] = value
	}
	variables["id"] = float64(position.ID)
	variables["deviceId"] = float64(position.DeviceID)
	variables["protocol"] = position.Protocol
	variables["valid"] = position.Valid
	variables["latitude"] = position.Latitude
	variables["longitude"] = position.Longitude
	variables["altitude"] = position.Altitude
	variables["speed"] = position.Speed
	variables["course"] = position.Course
	variables["accuracy"] = position.Accuracy
	if !position.Time.IsZero() {
		variables["time"] = float64(position.Time.UnixMilli())
	}
	if !position.ServerTime.IsZero() {
		variables["serverTime"] = float64(position.ServerTime.UnixMilli())
	}
	return variables
}

// EvaluateExpression evaluates one computed attribute against a position. The
// REST test route uses it, so a stored expression is checked with the same
// evaluator the chain runs.
func EvaluateExpression(source, attributeType string, position *model.Position) (any, error) {
	value, err := expr.Eval(source, expr.Variables(PositionVariables(position)))
	if err != nil {
		return nil, err
	}
	return convertAttribute(value, attributeType), nil
}

// convertAttribute turns an expression result into the type the attribute row
// declares. An unknown or empty type keeps the value as it is.
func convertAttribute(value any, attributeType string) any {
	switch strings.ToLower(strings.TrimSpace(attributeType)) {
	case "number", "double", "float":
		if number, ok := asFloat(value); ok {
			return number
		}
		return value
	case "integer", "int", "long":
		if number, ok := asFloat(value); ok {
			return int64(math.Round(number))
		}
		return value
	case "boolean", "bool":
		if number, ok := asFloat(value); ok {
			return number != 0
		}
		if text, ok := value.(string); ok {
			parsed, err := strconv.ParseBool(strings.TrimSpace(text))
			return err == nil && parsed
		}
		return value == true
	case "string":
		return fmt.Sprint(value)
	default:
		return value
	}
}
