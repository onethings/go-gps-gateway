package gps056_test

import (
	"bytes"
	"encoding/hex"
	"math"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/protocol/gps056"
)

type resolver map[string]*codec.DeviceSession

func (r resolver) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	return r[uniqueID], nil
}

func newDecoder() *gps056.Decoder {
	return gps056.NewDecoder(codec.Base{Resolver: resolver{
		"862462033782946": {DeviceID: 11, UniqueID: "862462033782946"},
		"863071015045849": {DeviceID: 12, UniqueID: "863071015045849"},
	}})
}

// fixture is one vector from Gps056ProtocolDecoderTest. A fixture without a
// stamp carries no fix of its own.
type fixture struct {
	name       string
	frame      []byte
	positions  int
	device     int64
	stamp      string
	valid      bool
	latitude   float64
	longitude  float64
	speed      float64
	outdated   bool
	attributes map[string]any
	responses  []string
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
			name:      "logon",
			frame:     []byte("$$25LOGN_118624620337829462.1#"),
			positions: 0,
			responses: []string{"*LGSA_118624620337829461#"},
		},
		{
			name:       "location",
			frame:      mustHex(t, "242435314750534C5F30323836333037313031353034353834391D0A0E091A0A0B1112C34E1E23230A45FF00000000000000000000000023"),
			positions:  1,
			device:     12,
			stamp:      "2014-10-29T09:26:10Z",
			valid:      true,
			latitude:   28.558005,
			longitude:  77.26495,
			speed:      helper.KnotsFromKph(255),
			attributes: map[string]any{model.KeyInput: 0, model.KeyOutput: 0, "adc1": float64(0), model.KeySatellites: 0, model.KeyRSSI: 0},
			responses:  []string{"*GPSA_02863071015045849\x00\x00#"},
		},
		{
			name:       "sync",
			frame:      mustHex(t, "2424323853594E435F313138363330373130313530343538343900000000000023"),
			positions:  1,
			device:     12,
			outdated:   true,
			attributes: map[string]any{model.KeyInput: 0, model.KeyOutput: 0, "adc1": float64(0), model.KeySatellites: 0, model.KeyRSSI: 0},
			responses:  []string{"*SYSA_11863071015045849#"},
		},
	}

	decoder := newDecoder()
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			result, err := decoder.Decode(f.frame)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(result.Positions) != f.positions {
				t.Fatalf("positions = %d, want %d", len(result.Positions), f.positions)
			}
			if len(result.Responses) != len(f.responses) {
				t.Fatalf("responses = %q, want %q", result.Responses, f.responses)
			}
			for index, want := range f.responses {
				if string(result.Responses[index]) != want {
					t.Errorf("response %d = %q, want %q", index, result.Responses[index], want)
				}
			}
			if f.positions == 0 {
				return
			}

			position := result.Positions[0]
			if position.Protocol != gps056.ProtocolName {
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

func TestDecodeUnknownDeviceIsIgnored(t *testing.T) {
	decoder := gps056.NewDecoder(codec.Base{Resolver: resolver{}})
	result, err := decoder.Decode([]byte("$$25LOGN_118624620337829462.1#"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 0 || len(result.Responses) != 0 {
		t.Errorf("result = %+v", result)
	}
}

func TestDecodeTruncatedFrameIsIgnored(t *testing.T) {
	result, err := newDecoder().Decode([]byte("$$00GPSL_02#"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 0 || len(result.Responses) != 0 {
		t.Errorf("result = %+v", result)
	}
}

func TestFrameDecoderSplitsFrames(t *testing.T) {
	first := mustHex(t, "242435314750534C5F30323836333037313031353034353834391D0A0E091A0A0B1112C34E1E23230A45FF00000000000000000000000023")
	second := mustHex(t, "2424323853594E435F313138363330373130313530343538343900000000000023")
	garbage := []byte("junk")

	data := append(append(append([]byte{}, garbage...), first...), second...)
	decoder := gps056.NewFrameDecoder()

	frame, consumed := decoder.DecodeFrame(data)
	if !bytes.Equal(frame, first) || consumed != len(garbage)+len(first) {
		t.Fatalf("frame = %x, consumed = %d", frame, consumed)
	}

	sliced, consumed := decoder.DecodeFrame(data[consumed:])
	if !bytes.Equal(sliced, second) || consumed != len(second) {
		t.Fatalf("frame = %x, consumed = %d", sliced, consumed)
	}

	if frame, consumed := decoder.DecodeFrame(data[:10]); frame != nil || consumed != len(garbage) {
		t.Fatalf("partial frame = %x, consumed = %d", frame, consumed)
	}
	if frame, consumed := decoder.DecodeFrame([]byte("junk")); frame != nil || consumed != len("junk") {
		t.Fatalf("garbage = %x, consumed = %d", frame, consumed)
	}
}

// TestDecodeFrameDecoderSlice covers the declared length, which counts the
// payload without the trailing '#'.
func TestDecodeFrameDecoderSlice(t *testing.T) {
	full := mustHex(t, "242435314750534C5F30323836333037313031353034353834391D0A0E091A0A0B1112C34E1E23230A45FF00000000000000000000000023")
	slice, _ := gps056.NewFrameDecoder().DecodeFrame(full)

	decoder := newDecoder()
	want, err := decoder.Decode(full)
	if err != nil {
		t.Fatalf("decode full: %v", err)
	}
	got, err := decoder.Decode(slice)
	if err != nil {
		t.Fatalf("decode slice: %v", err)
	}
	if len(got.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(got.Positions))
	}
	if len(want.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(want.Positions))
	}
	if !got.Positions[0].Time.Equal(want.Positions[0].Time) ||
		got.Positions[0].Latitude != want.Positions[0].Latitude ||
		got.Positions[0].Longitude != want.Positions[0].Longitude ||
		got.Positions[0].Speed != want.Positions[0].Speed {
		t.Errorf("sliced position = %+v", got.Positions[0])
	}
	if len(got.Responses) != 1 || !bytes.Equal(got.Responses[0], want.Responses[0]) {
		t.Errorf("sliced responses = %q", got.Responses)
	}
}
