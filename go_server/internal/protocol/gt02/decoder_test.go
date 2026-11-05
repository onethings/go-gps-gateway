package gt02_test

import (
	"bytes"
	"encoding/hex"
	"math"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/protocol/gt02"
)

type resolver map[string]*codec.DeviceSession

func (r resolver) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	return r[uniqueID], nil
}

func newDecoder() *gt02.Decoder {
	return gt02.NewDecoder(codec.Base{Resolver: resolver{
		"123456789012345": {DeviceID: 1, UniqueID: "123456789012345"},
		"358899050003725": {DeviceID: 2, UniqueID: "358899050003725"},
		"358899051012766": {DeviceID: 3, UniqueID: "358899051012766"},
		"358899058314017": {DeviceID: 4, UniqueID: "358899058314017"},
		"358899058952584": {DeviceID: 5, UniqueID: "358899058952584"},
	}})
}

// fixture is one vector from Gt02ProtocolDecoderTest. A fixture without a stamp
// carries no fix of its own.
type fixture struct {
	name       string
	frame      []byte
	device     int64
	stamp      string
	valid      bool
	latitude   float64
	longitude  float64
	speed      float64
	course     float64
	outdated   bool
	attributes map[string]any
}

func mustHex(t *testing.T, text string) []byte {
	t.Helper()
	data, err := hex.DecodeString(text)
	if err != nil {
		t.Fatalf("fixture hex: %v", err)
	}
	return data
}

func TestDecodeFixtures(t *testing.T) {
	fixtures := []fixture{
		{
			name:       "command response accepted",
			frame:      mustHex(t, "6868150000035889905895258400831c07415045584f4b210d0a"),
			device:     5,
			outdated:   true,
			attributes: map[string]any{model.KeyIndex: 131, model.KeyResult: "APEXOK!"},
		},
		{
			name:       "command response rejected",
			frame:      mustHex(t, "68682d0000035889905895258400951c1f415045584572726f723a20506172616d65746572203120284f4e2f4f4646290d0a"),
			device:     5,
			outdated:   true,
			attributes: map[string]any{model.KeyIndex: 149, model.KeyResult: "APEXError: Parameter 1 (ON/OFF)"},
		},
		{
			name:       "heartbeat with status",
			frame:      mustHex(t, "68680f0504035889905831401700df1a00000d0a"),
			device:     4,
			outdated:   true,
			attributes: map[string]any{model.KeyIndex: 223, model.KeyPower: 5, model.KeyRSSI: 4},
		},
		{
			name:       "heartbeat with date",
			frame:      mustHex(t, "6868130504035889905831401700001a040423261e290d0a"),
			device:     4,
			outdated:   true,
			attributes: map[string]any{model.KeyIndex: 0, model.KeyPower: 5, model.KeyRSSI: 4},
		},
		{
			name:       "heartbeat with status and date",
			frame:      mustHex(t, "68681905040358899058314017000e1a010a2623211b2722252329000d0a"),
			device:     4,
			outdated:   true,
			attributes: map[string]any{model.KeyIndex: 14, model.KeyPower: 5, model.KeyRSSI: 4},
		},
		{
			name:       "heartbeat from another device",
			frame:      mustHex(t, "68681a060303588990500037252de91a010a171a191b171915191e10000d0a"),
			device:     2,
			outdated:   true,
			attributes: map[string]any{model.KeyIndex: 11753, model.KeyPower: 6, model.KeyRSSI: 3},
		},
		{
			name:      "location",
			frame:     mustHex(t, "68682500000123456789012345000110010101010101026B3F3E026B3F3E000000000000000000010D0A"),
			device:    1,
			stamp:     "2001-01-01T01:01:01Z",
			valid:     true,
			latitude:  -22.546097,
			longitude: -22.546097,
			course:    0,
			attributes: map[string]any{
				model.KeyIndex: 1,
			},
		},
		{
			name:       "heartbeat without date",
			frame:      mustHex(t, "6868110603035889905101276600001a0402292d0d0a"),
			device:     3,
			outdated:   true,
			attributes: map[string]any{model.KeyIndex: 0, model.KeyPower: 6, model.KeyRSSI: 3},
		},
		{
			name:      "location from another device",
			frame:     mustHex(t, "68682500a403588990510127660001100e09060a1d1b00ade1c90b79ea3000011b000000000000050d0a"),
			device:    3,
			stamp:     "2014-09-06T10:29:27Z",
			valid:     true,
			latitude:  -6.330849,
			longitude: 106.966213,
			course:    283,
			attributes: map[string]any{
				model.KeyIndex: 1,
			},
		},
	}

	decoder := newDecoder()
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			result, err := decoder.Decode(f.frame)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(result.Positions) != 1 {
				t.Fatalf("positions = %d, want 1", len(result.Positions))
			}
			position := result.Positions[0]
			if position.Protocol != gt02.ProtocolName {
				t.Errorf("protocol = %q", position.Protocol)
			}
			if position.DeviceID != f.device {
				t.Errorf("device = %d, want %d", position.DeviceID, f.device)
			}
			if position.Outdated != f.outdated {
				t.Errorf("outdated = %v, want %v", position.Outdated, f.outdated)
			}
			if f.stamp != "" {
				stamp, err := time.Parse(time.RFC3339, f.stamp)
				if err != nil {
					t.Fatalf("fixture stamp: %v", err)
				}
				if !position.Time.Equal(stamp) {
					t.Errorf("time = %s, want %s", position.Time.Format(time.RFC3339), f.stamp)
				}
				if position.Valid != f.valid {
					t.Errorf("valid = %v, want %v", position.Valid, f.valid)
				}
				if math.Abs(position.Latitude-f.latitude) > 1e-6 {
					t.Errorf("latitude = %.6f, want %.6f", position.Latitude, f.latitude)
				}
				if math.Abs(position.Longitude-f.longitude) > 1e-6 {
					t.Errorf("longitude = %.6f, want %.6f", position.Longitude, f.longitude)
				}
				if position.Speed != f.speed {
					t.Errorf("speed = %v, want %v", position.Speed, f.speed)
				}
				if position.Course != f.course {
					t.Errorf("course = %v, want %v", position.Course, f.course)
				}
			}
			for key, want := range f.attributes {
				got, ok := position.Get(key)
				if !ok {
					t.Errorf("attribute %q missing", key)
					continue
				}
				if got != want {
					t.Errorf("attribute %q = %#v, want %#v", key, got, want)
				}
			}
		})
	}
}

func TestDecodeHeartbeatAnswers(t *testing.T) {
	result, err := newDecoder().Decode(mustHex(t, "68680f0504035889905831401700df1a00000d0a"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Responses) != 1 || !bytes.Equal(result.Responses[0], []byte{0x54, 0x68, 0x1A, 0x0D, 0x0A}) {
		t.Errorf("responses = %x", result.Responses)
	}
}

func TestDecodeUnknownDeviceIsIgnored(t *testing.T) {
	decoder := gt02.NewDecoder(codec.Base{Resolver: resolver{}})
	result, err := decoder.Decode(mustHex(t, "68682500000123456789012345000110010101010101026B3F3E026B3F3E000000000000000000010D0A"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 0 || len(result.Responses) != 0 {
		t.Errorf("result = %+v", result)
	}
}

func TestDecodeTruncatedFrameIsIgnored(t *testing.T) {
	result, err := newDecoder().Decode(mustHex(t, "6868000d0a"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 0 || len(result.Responses) != 0 {
		t.Errorf("result = %+v", result)
	}
}

func TestFrameDecoderSplitsFrames(t *testing.T) {
	first := mustHex(t, "68680f0504035889905831401700df1a00000d0a")
	second := mustHex(t, "68682500000123456789012345000110010101010101026B3F3E026B3F3E000000000000000000010D0A")
	garbage := []byte("junk")

	data := append(append(append([]byte{}, garbage...), first...), second...)
	decoder := gt02.NewFrameDecoder()

	frame, consumed := decoder.DecodeFrame(data)
	if !bytes.Equal(frame, first) || consumed != len(garbage)+len(first) {
		t.Fatalf("frame = %x, consumed = %d", frame, consumed)
	}

	frame, consumed = decoder.DecodeFrame(data[consumed:])
	if !bytes.Equal(frame, second) || consumed != len(second) {
		t.Fatalf("frame = %x, consumed = %d", frame, consumed)
	}

	if frame, consumed := decoder.DecodeFrame(data[:10]); frame != nil || consumed != len(garbage) {
		t.Fatalf("partial frame = %x, consumed = %d", frame, consumed)
	}
	if frame, consumed := decoder.DecodeFrame([]byte("junk")); frame != nil || consumed != len("junk") {
		t.Fatalf("garbage = %x, consumed = %d", frame, consumed)
	}
}
