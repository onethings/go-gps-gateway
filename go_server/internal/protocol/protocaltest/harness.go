// Package protocaltest provides the shared assertions used by the protocol
// decoder tests. It is a Go port of org.traccar.ProtocolTest so the same test
// vectors from the Java suite can be reused.
package protocaltest

import (
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
)

// StubResolver resolves every unique id to the same device, matching the
// mocked connection manager used by the Java tests.
type StubResolver struct {
	DeviceID int64
	// Reject makes the resolver behave as if no device is known.
	Reject bool
}

// NewStubResolver returns a resolver that always yields device id 1.
func NewStubResolver() *StubResolver {
	return &StubResolver{DeviceID: 1}
}

// Resolve implements codec.DeviceResolver.
func (r *StubResolver) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	if r.Reject {
		return nil, nil
	}
	return &codec.DeviceSession{DeviceID: r.DeviceID, UniqueID: uniqueID}, nil
}

// StubUniqueIDs reports a fixed unique id, matching BaseTest's mock.
type StubUniqueIDs struct {
	ID string
}

// NewStubUniqueIDs returns a provider yielding 123456789012345.
func NewStubUniqueIDs() *StubUniqueIDs {
	return &StubUniqueIDs{ID: "123456789012345"}
}

// UniqueID implements codec.UniqueIDProvider.
func (s *StubUniqueIDs) UniqueID(deviceID int64) (string, error) {
	return s.ID, nil
}

// Base returns decoder dependencies wired to the stubs.
func Base() codec.Base {
	return codec.Base{Resolver: NewStubResolver()}
}

// Checks selects how strictly a position is validated.
type Checks int

// Validation modes.
const (
	ChecksAll Checks = iota
	ChecksAttributes
	ChecksNone
)

type assertion func(position *model.Position) error

// Expectation describes the expected content of one decoded position.
type Expectation struct {
	mode       Checks
	assertions []assertion
}

// Position creates an expectation that applies the standard sanity checks.
func Position() *Expectation {
	return &Expectation{mode: ChecksAll}
}

// PositionWith creates an expectation with a specific check mode.
func PositionWith(mode Checks) *Expectation {
	return &Expectation{mode: mode}
}

// Location asserts the fix time, validity and coordinates.
func (e *Expectation) Location(rfc3339 string, valid bool, latitude, longitude float64) *Expectation {
	e.assertions = append(e.assertions, func(p *model.Position) error {
		want, err := time.Parse(time.RFC3339Nano, rfc3339)
		if err != nil {
			return err
		}
		if !p.Time.Equal(want) {
			return fmt.Errorf("time = %s, want %s", p.Time.UTC().Format(time.RFC3339Nano), rfc3339)
		}
		if p.Valid != valid {
			return fmt.Errorf("valid = %v, want %v", p.Valid, valid)
		}
		if math.Abs(p.Latitude-latitude) > 1e-5 {
			return fmt.Errorf("latitude = %.6f, want %.6f", p.Latitude, latitude)
		}
		if math.Abs(p.Longitude-longitude) > 1e-5 {
			return fmt.Errorf("longitude = %.6f, want %.6f", p.Longitude, longitude)
		}
		return nil
	})
	return e
}

// Attribute asserts one attribute value, comparing numbers numerically.
func (e *Expectation) Attribute(key string, want any) *Expectation {
	e.assertions = append(e.assertions, func(p *model.Position) error {
		got, ok := p.Get(key)
		if !ok {
			return fmt.Errorf("attribute %q missing", key)
		}
		if !valuesEqual(got, want) {
			return fmt.Errorf("attribute %q = %#v, want %#v", key, got, want)
		}
		return nil
	})
	return e
}

// AttributeAbsent asserts an attribute is not present.
func (e *Expectation) AttributeAbsent(key string) *Expectation {
	e.assertions = append(e.assertions, func(p *model.Position) error {
		if _, ok := p.Get(key); ok {
			return fmt.Errorf("attribute %q unexpectedly present", key)
		}
		return nil
	})
	return e
}

// verify runs the expectation against a decoded position.
func (e *Expectation) verify(p *model.Position) error {
	if e.mode == ChecksAll {
		if p.DeviceID <= 0 {
			return fmt.Errorf("deviceId = %d, want > 0", p.DeviceID)
		}
		if p.Protocol == "" {
			return fmt.Errorf("protocol is empty")
		}
		if p.Time.Before(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) {
			return fmt.Errorf("fix time %s is before 2000", p.Time)
		}
		if p.Time.After(time.Now().Add(25 * time.Hour)) {
			return fmt.Errorf("fix time %s is more than 25h in the future", p.Time)
		}
		if p.Latitude < -90 || p.Latitude > 90 {
			return fmt.Errorf("latitude %f out of range", p.Latitude)
		}
		if p.Longitude < -180 || p.Longitude > 180 {
			return fmt.Errorf("longitude %f out of range", p.Longitude)
		}
		if p.Altitude < -12262 || p.Altitude > 18000 {
			return fmt.Errorf("altitude %f out of range", p.Altitude)
		}
		if p.Speed < 0 || p.Speed > 869 {
			return fmt.Errorf("speed %f out of range", p.Speed)
		}
		if p.Course < 0 || p.Course > 360 {
			return fmt.Errorf("course %f out of range", p.Course)
		}
	}
	if e.mode == ChecksAttributes && len(p.Attributes) == 0 {
		return fmt.Errorf("no attributes decoded")
	}
	for _, check := range e.assertions {
		if err := check(p); err != nil {
			return err
		}
	}
	return nil
}

// VerifyDecode decodes one frame and compares the result with the expectations.
func VerifyDecode(t *testing.T, decoder codec.Decoder, frame []byte, expected ...*Expectation) *codec.Result {
	t.Helper()

	result, err := decoder.Decode(frame)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if result == nil {
		t.Fatalf("decode returned nil result")
	}

	if len(result.Positions) != len(expected) {
		t.Fatalf("positions = %d, want %d", len(result.Positions), len(expected))
	}

	for i, expectation := range expected {
		if err := expectation.verify(result.Positions[i]); err != nil {
			t.Fatalf("position[%d]: %v (%s)", i, err, result.Positions[i])
		}
	}
	return result
}

// VerifyObject asserts that the decoder rejects a frame.
func VerifyObject(t *testing.T, decoder codec.Decoder, frame []byte) {
	t.Helper()
	result, err := decoder.Decode(frame)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if result != nil && len(result.Positions) != 0 {
		t.Fatalf("expected no positions, got %d", len(result.Positions))
	}
}

// VerifyResponse asserts the single response payload produced by the decode.
func VerifyResponse(t *testing.T, result *codec.Result, want string) {
	t.Helper()
	if len(result.Responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(result.Responses))
	}
	if got := string(result.Responses[0]); got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
}

// DecodeString is a convenience wrapper for text based protocols.
func DecodeString(t *testing.T, decoder codec.Decoder, text string) *codec.Result {
	t.Helper()
	result, err := decoder.Decode([]byte(text))
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	return result
}

// Hex decodes a hex string into bytes, failing the test when it is malformed.
func Hex(t *testing.T, value string) []byte {
	t.Helper()
	clean := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			return -1
		}
		return r
	}, value)
	data, err := hex.DecodeString(clean)
	if err != nil {
		t.Fatalf("invalid hex %q: %v", value, err)
	}
	return data
}

// Lines splits a hex dump that contains one frame per line.
func Lines(t *testing.T, dump string) [][]byte {
	t.Helper()
	var frames [][]byte
	for _, line := range strings.Split(dump, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		frames = append(frames, Hex(t, line))
	}
	return frames
}

func valuesEqual(got, want any) bool {
	if gf, ok := asFloat(got); ok {
		if wf, ok := asFloat(want); ok {
			return math.Abs(gf-wf) < 1e-9
		}
		return false
	}
	return reflect.DeepEqual(got, want)
}

func asFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	}
	return 0, false
}
