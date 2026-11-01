package gps103_test

import (
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/protocol/gps103"
)

type resolver struct {
	sessions map[string]*codec.DeviceSession
}

func (r resolver) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	return r.sessions[uniqueID], nil
}

func newBase() codec.Base {
	clock := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	return codec.Base{
		Resolver: resolver{sessions: map[string]*codec.DeviceSession{
			"123456789012345": {DeviceID: 9, UniqueID: "123456789012345"},
		}},
		Now: func() time.Time { return clock },
	}
}

func TestDecodePosition(t *testing.T) {
	decoder := gps103.NewDecoder(newBase())
	frame := []byte("imei:123456789012345,tracker,180504121933,,F,120411.000,A,54.94535,N,24.01762,E,12.5,90,100,")

	result, err := decoder.Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d", len(result.Positions))
	}
	position := result.Positions[0]
	if position.DeviceID != 9 || position.Protocol != gps103.ProtocolName {
		t.Errorf("identity = %+v", position)
	}
	if position.Latitude != 54.94535 || position.Longitude != 24.01762 {
		t.Errorf("coordinates = %v,%v", position.Latitude, position.Longitude)
	}
	if position.Speed != 12.5 || position.Course != 90 || position.Altitude != 100 {
		t.Errorf("motion = %+v", position)
	}
	if !position.Valid {
		t.Error("position is not valid")
	}
	// The stamp is ddmmyyhhmmss, so 180504121933 is 2004-05-18 12:19:33.
	want := time.Date(2004, 5, 18, 12, 19, 33, 0, time.UTC)
	if !position.Time.Equal(want) {
		t.Errorf("time = %v, want %v", position.Time, want)
	}
}

func TestDecodeInvalidFixKeepsThePosition(t *testing.T) {
	decoder := gps103.NewDecoder(newBase())
	frame := []byte("imei:123456789012345,tracker,180504121933,,L,120411.000,V,54.94535,N,24.01762,E,0,0,0,")

	result, err := decoder.Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d", len(result.Positions))
	}
	if result.Positions[0].Valid {
		t.Error("a V frame is marked valid")
	}
	if signal, _ := result.Positions[0].Get("gpsSignal"); signal != "L" {
		t.Errorf("signal = %v", signal)
	}
}

func TestDecodeHeartbeatAnswers(t *testing.T) {
	decoder := gps103.NewDecoder(newBase())
	result, err := decoder.Decode([]byte("imei:123456789012345,heartbeat,180504121933,,F,120411.000,A,0.0,N,0.0,E,0,0,0,"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 0 {
		t.Errorf("positions = %d, want none", len(result.Positions))
	}
	if len(result.Responses) != 1 || string(result.Responses[0]) != "**,imei:123456789012345;B" {
		t.Errorf("responses = %q", result.Responses)
	}
}

func TestDecodeUnknownDeviceIsIgnored(t *testing.T) {
	decoder := gps103.NewDecoder(newBase())
	result, err := decoder.Decode([]byte("imei:999,tracker,180504121933,,F,120411.000,A,1,N,2,E,0,0,0,"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 0 {
		t.Errorf("positions = %d", len(result.Positions))
	}
}

func TestFrameDecoderSplitsSentences(t *testing.T) {
	decoder := &gps103.FrameDecoder{}

	frame, consumed := decoder.DecodeFrame([]byte("imei:1,tracker;imei:2"))
	if string(frame) != "imei:1,tracker" || consumed != len("imei:1,tracker;") {
		t.Fatalf("frame = %q, consumed = %d", frame, consumed)
	}

	frame, consumed = decoder.DecodeFrame([]byte("imei:1,tracker\r\n"))
	if string(frame) != "imei:1,tracker" || consumed != len("imei:1,tracker\r\n") {
		t.Fatalf("frame = %q, consumed = %d", frame, consumed)
	}

	if frame, consumed = decoder.DecodeFrame([]byte("imei:1,trac")); frame != nil || consumed != 0 {
		t.Fatalf("partial frame = %q, consumed = %d", frame, consumed)
	}
}
