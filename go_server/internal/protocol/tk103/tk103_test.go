package tk103_test

import (
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/protocol/tk103"
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
			"359586015829802": {DeviceID: 4, UniqueID: "359586015829802"},
		}},
		Now: func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
	}
}

func TestDecodePosition(t *testing.T) {
	decoder := tk103.NewDecoder(newBase())
	frame := []byte("imei:359586015829802,180504121933,,F,120411.000,A,5456.7210,N,02401.0572,E,12.5,90")

	result, err := decoder.Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d", len(result.Positions))
	}
	position := result.Positions[0]
	if position.DeviceID != 4 {
		t.Errorf("device id = %d", position.DeviceID)
	}
	// 54 degrees plus 56.721 minutes.
	if !closeTo(position.Latitude, 54.94535) || !closeTo(position.Longitude, 24.01762) {
		t.Errorf("coordinates = %v,%v", position.Latitude, position.Longitude)
	}
	if position.Speed != 12.5 || position.Course != 90 {
		t.Errorf("motion = %+v", position)
	}
	if !position.Valid {
		t.Error("position is not valid")
	}
	want := time.Date(2004, 5, 18, 12, 19, 33, 0, time.UTC)
	if !position.Time.Equal(want) {
		t.Errorf("time = %v, want %v", position.Time, want)
	}
}

func TestDecodeLowSignalIsInvalid(t *testing.T) {
	decoder := tk103.NewDecoder(newBase())
	frame := []byte("imei:359586015829802,180504121933,,L,120411.000,V,5456.7210,N,02401.0572,E,0,0")

	result, err := decoder.Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d", len(result.Positions))
	}
	if result.Positions[0].Valid {
		t.Error("a low signal frame is marked valid")
	}
}

func TestFrameDecoderSplitsLines(t *testing.T) {
	decoder := &tk103.FrameDecoder{}

	frame, consumed := decoder.DecodeFrame([]byte("imei:1,2\r\nimei:3"))
	if string(frame) != "imei:1,2" || consumed != len("imei:1,2\r\n") {
		t.Fatalf("frame = %q, consumed = %d", frame, consumed)
	}
	if frame, consumed = decoder.DecodeFrame([]byte("imei:1,2")); frame != nil || consumed != 0 {
		t.Fatalf("partial frame = %q", frame)
	}
}

func closeTo(got, want float64) bool {
	delta := got - want
	return delta < 1e-5 && delta > -1e-5
}
