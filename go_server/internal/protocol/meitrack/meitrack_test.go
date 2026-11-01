package meitrack_test

import (
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/protocol/meitrack"
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
			"123456789012345": {DeviceID: 11, UniqueID: "123456789012345"},
		}},
		Now: func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
	}
}

// frame builds a Meitrack frame with the XOR checksum of body.
func frame(body string) []byte {
	var checksum byte
	for index := 0; index < len(body); index++ {
		checksum ^= body[index]
	}
	return []byte("$$" + body + "*" + hex(checksum) + "\r\n")
}

func hex(value byte) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{digits[value>>4], digits[value&0x0f]})
}

func TestDecodePosition(t *testing.T) {
	decoder := meitrack.NewDecoder(newBase())
	body := "003,123456789012345,35,54.94535,24.01762,20230504123045,12.5,90,100,8"

	result, err := decoder.Decode(frame(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d", len(result.Positions))
	}
	position := result.Positions[0]
	if position.DeviceID != 11 {
		t.Errorf("device id = %d", position.DeviceID)
	}
	if position.Latitude != 54.94535 || position.Longitude != 24.01762 {
		t.Errorf("coordinates = %v,%v", position.Latitude, position.Longitude)
	}
	want := time.Date(2023, 5, 4, 12, 30, 45, 0, time.UTC)
	if !position.Time.Equal(want) {
		t.Errorf("time = %v, want %v", position.Time, want)
	}
	if position.Speed != 12.5 || position.Course != 90 || position.Altitude != 100 {
		t.Errorf("motion = %+v", position)
	}
	if satellites, _ := position.Get("sat"); satellites != 8.0 {
		t.Errorf("satellites = %v", satellites)
	}
	if alarm, _ := position.Get("alarm"); alarm != "35" {
		t.Errorf("event = %v", alarm)
	}
}

func TestDecodeRejectsABadChecksum(t *testing.T) {
	decoder := meitrack.NewDecoder(newBase())
	broken := []byte("$$003,123456789012345,35,54.94535,24.01762,20230504123045,12.5,90,100,8*00\r\n")

	if _, err := decoder.Decode(broken); err == nil {
		t.Fatal("a wrong checksum was accepted")
	}
}

func TestFrameDecoderSplitsFrames(t *testing.T) {
	decoder := &meitrack.FrameDecoder{}
	first := frame("003,1,35,1,2,20230504123045,0,0,0,0")
	second := frame("003,2,35,1,2,20230504123045,0,0,0,0")

	got, consumed := decoder.DecodeFrame(append(append([]byte(nil), first...), second...))
	if string(got) != string(first[:len(first)-2]) {
		t.Fatalf("frame = %q", got)
	}
	if consumed != len(first) {
		t.Fatalf("consumed = %d, want %d", consumed, len(first))
	}
}

func TestFrameDecoderDropsGarbage(t *testing.T) {
	decoder := &meitrack.FrameDecoder{}
	if _, consumed := decoder.DecodeFrame([]byte("garbage")); consumed != len("garbage") {
		t.Fatalf("consumed = %d", consumed)
	}
}
