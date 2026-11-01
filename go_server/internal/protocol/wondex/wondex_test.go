package wondex_test

import (
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/protocol/wondex"
)

type resolver struct {
	sessions map[string]*codec.DeviceSession
}

func (r resolver) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	return r.sessions[uniqueID], nil
}

func newBase() codec.Base {
	return codec.Base{
		Resolver: resolver{sessions: map[string]*codec.DeviceSession{
			"1234567890": {DeviceID: 3, UniqueID: "1234567890"},
		}},
		Now: func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
	}
}

func TestDecodePosition(t *testing.T) {
	decoder := wondex.NewDecoder(newBase())
	frame := []byte("#DD#1234567890;20230504123045;54.94535;24.01762;12.5;90;100;8;1.2\r")

	result, err := decoder.Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d", len(result.Positions))
	}
	position := result.Positions[0]
	if position.DeviceID != 3 {
		t.Errorf("device id = %d", position.DeviceID)
	}
	if position.Latitude != 54.94535 || position.Longitude != 24.01762 {
		t.Errorf("coordinates = %v,%v", position.Latitude, position.Longitude)
	}
	if position.Speed != 12.5 || position.Course != 90 || position.Altitude != 100 {
		t.Errorf("motion = %+v", position)
	}
	if satellites, _ := position.Get("sat"); satellites != 8.0 {
		t.Errorf("satellites = %v", satellites)
	}
	if hdop, _ := position.Get("hdop"); hdop != 1.2 {
		t.Errorf("hdop = %v", hdop)
	}
	want := time.Date(2023, 5, 4, 12, 30, 45, 0, time.UTC)
	if !position.Time.Equal(want) {
		t.Errorf("time = %v, want %v", position.Time, want)
	}
}

func TestDecodeIgnoresOtherFrames(t *testing.T) {
	decoder := wondex.NewDecoder(newBase())
	for _, frame := range []string{"#P#1234567890", "#DD#1234567890", ""} {
		result, err := decoder.Decode([]byte(frame))
		if err != nil {
			t.Fatalf("decode %q: %v", frame, err)
		}
		if len(result.Positions) != 0 {
			t.Errorf("frame %q produced a position", frame)
		}
	}
}

func TestFrameDecoderSplitsLines(t *testing.T) {
	decoder := &wondex.FrameDecoder{}

	frame, consumed := decoder.DecodeFrame([]byte("#DD#1;a\r\n#DD#2"))
	if string(frame) != "#DD#1;a" || consumed != len("#DD#1;a\r\n") {
		t.Fatalf("frame = %q, consumed = %d", frame, consumed)
	}
	if frame, consumed = decoder.DecodeFrame([]byte("#DD#1;a")); frame != nil || consumed != 0 {
		t.Fatalf("partial frame = %q", frame)
	}
}
