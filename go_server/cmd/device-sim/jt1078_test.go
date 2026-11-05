package main

import (
	"bytes"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/protocol/jt1078"
)

func TestAccessUnitPacketsReassemble(t *testing.T) {
	unit := bytes.Repeat([]byte{0x00, 0x00, 0x00, 0x01, 0x65, 0xaa}, 1000)
	at := time.UnixMilli(1700000000000)
	identifier, err := parseJT1078ID("013512345006")
	if err != nil {
		t.Fatalf("identifier: %v", err)
	}

	packets := accessUnitPackets(identifier, 1, 0, 98, at, unit)
	if len(packets) < 3 {
		t.Fatalf("packets = %d, want the unit split over several", len(packets))
	}

	sink := &frameSink{}
	decoder := jt1078.NewDecoder(codec.Base{
		Resolver: staticResolver{deviceID: 7, uniqueID: "013512345006"},
		Media:    sink,
	})
	framer := jt1078.NewFrameDecoder()

	var stream []byte
	for _, packet := range packets {
		stream = append(stream, packet...)
	}
	for {
		frame, consumed := framer.DecodeFrame(stream)
		if frame == nil {
			break
		}
		stream = stream[consumed:]
		if _, err := decoder.Decode(frame); err != nil {
			t.Fatalf("Decode: %v", err)
		}
	}

	if len(sink.frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(sink.frames))
	}
	if !bytes.Equal(sink.frames[0].Payload, unit) {
		t.Fatal("reassembled payload differs from the access unit")
	}
	if !sink.frames[0].KeyFrame {
		t.Fatal("an IDR slice should be a key frame")
	}
}

func TestParseJT1078IDRejectsShortValues(t *testing.T) {
	if _, err := parseJT1078ID("123"); err == nil {
		t.Fatal("a short identifier was accepted")
	}
}

type staticResolver struct {
	deviceID int64
	uniqueID string
}

func (r staticResolver) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	if uniqueID != r.uniqueID {
		return nil, nil
	}
	return &codec.DeviceSession{DeviceID: r.deviceID, UniqueID: uniqueID}, nil
}

type frameSink struct {
	frames []codec.MediaFrame
}

func (s *frameSink) WriteFrame(frame codec.MediaFrame) { s.frames = append(s.frames, frame) }
