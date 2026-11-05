package jt1078

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
)

type packetSpec struct {
	idBytes     []byte
	channel     byte
	dataType    byte
	subpackage  byte
	payloadType byte
	timestamp   time.Time
	body        []byte
}

func buildPacket(spec packetSpec) []byte {
	idBytes := spec.idBytes
	if len(idBytes) == 0 {
		idBytes = []byte{0x01, 0x35, 0x12, 0x34, 0x50, 0x06}
	}

	out := make([]byte, 0, 32+len(spec.body))
	out = append(out, Magic...)
	out = append(out, 0x80)
	out = append(out, 0x80|spec.payloadType)
	out = append(out, 0x00, 0x01)
	out = append(out, idBytes...)
	out = append(out, spec.channel)
	out = append(out, spec.dataType<<4|spec.subpackage)

	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], uint64(spec.timestamp.UnixMilli()))
	out = append(out, stamp[:]...)

	if spec.dataType <= 2 {
		out = append(out, 0x00, 0x00, 0x00, 0x00)
	}

	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(spec.body)))
	out = append(out, length[:]...)
	return append(out, spec.body...)
}

func keyFrame(body []byte, at time.Time) []byte {
	return buildPacket(packetSpec{
		channel: 1, dataType: typeIFrame, subpackage: subpackageAtomic,
		payloadType: payloadH264, timestamp: at, body: body,
	})
}

type stubResolver struct {
	deviceID int64
	uniqueID string
}

func (r stubResolver) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	if uniqueID != r.uniqueID {
		return nil, nil
	}
	return &codec.DeviceSession{DeviceID: r.deviceID, UniqueID: uniqueID}, nil
}

type captureSink struct {
	frames []codec.MediaFrame
}

func (s *captureSink) WriteFrame(frame codec.MediaFrame) {
	s.frames = append(s.frames, frame)
}

func TestFrameDecoderSplitsPackets(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	first := keyFrame([]byte("first"), now)
	second := keyFrame([]byte("second"), now.Add(time.Second))

	decoder := NewFrameDecoder()
	stream := append(append([]byte("junk"), first...), second...)

	got, consumed := decoder.DecodeFrame(stream)
	if !bytes.Equal(got, first) {
		t.Fatalf("first packet mismatch: %x", got)
	}
	if consumed != 4+len(first) {
		t.Fatalf("consumed = %d, want %d", consumed, 4+len(first))
	}

	got, consumed = decoder.DecodeFrame(stream[consumed:])
	if !bytes.Equal(got, second) {
		t.Fatalf("second packet mismatch: %x", got)
	}
	if consumed != len(second) {
		t.Fatalf("consumed = %d, want %d", consumed, len(second))
	}
}

func TestFrameDecoderWaitsForTheRest(t *testing.T) {
	packet := keyFrame([]byte("payload"), time.UnixMilli(1700000000000))
	decoder := NewFrameDecoder()

	for _, size := range []int{3, 12, len(packet) - 1} {
		frame, consumed := decoder.DecodeFrame(packet[:size])
		if frame != nil || consumed != 0 {
			t.Fatalf("at %d bytes: frame %x, consumed %d", size, frame, consumed)
		}
	}

	frame, _ := decoder.DecodeFrame(packet)
	if !bytes.Equal(frame, packet) {
		t.Fatal("complete packet was not returned")
	}
}

func TestFrameDecoderKeepsASplitMagic(t *testing.T) {
	packet := keyFrame([]byte("payload"), time.UnixMilli(1700000000000))
	decoder := NewFrameDecoder()

	frame, consumed := decoder.DecodeFrame(append([]byte("junk"), Magic[:2]...))
	if frame != nil {
		t.Fatal("returned a frame without a complete header")
	}
	if consumed != 4 {
		t.Fatalf("consumed = %d, want the four junk bytes", consumed)
	}

	frame, _ = decoder.DecodeFrame(append(append([]byte(nil), Magic[:2]...), packet[2:]...))
	if !bytes.Equal(frame, packet) {
		t.Fatal("split magic lost the packet")
	}
}

func TestDecoderForwardsAnAtomicFrame(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	sink := &captureSink{}
	decoder := NewDecoder(codec.Base{
		Resolver: stubResolver{deviceID: 7, uniqueID: "013512345006"},
		Media:    sink,
	})

	result, err := decoder.Decode(keyFrame([]byte("idr"), now))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !result.Consumed || result.UniqueID != "013512345006" {
		t.Fatalf("result = %+v", result)
	}
	if len(sink.frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(sink.frames))
	}

	frame := sink.frames[0]
	if frame.DeviceID != 7 || frame.Channel != 1 {
		t.Fatalf("frame key = %d/%d", frame.DeviceID, frame.Channel)
	}
	if frame.Codec != codec.MediaCodecH264 || !frame.KeyFrame {
		t.Fatalf("frame codec = %v key = %v", frame.Codec, frame.KeyFrame)
	}
	if !frame.Timestamp.Equal(now) {
		t.Fatalf("timestamp = %v, want %v", frame.Timestamp, now)
	}
	if string(frame.Payload) != "idr" {
		t.Fatalf("payload = %q", frame.Payload)
	}
}

func TestDecoderReassemblesSubpackages(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	sink := &captureSink{}
	decoder := NewDecoder(codec.Base{
		Resolver: stubResolver{deviceID: 7, uniqueID: "013512345006"},
		Media:    sink,
	})

	parts := []packetSpec{
		{dataType: typeIFrame, subpackage: subpackageFirst, payloadType: payloadH265, timestamp: now, body: []byte("one-")},
		{dataType: typeIFrame, subpackage: subpackageMiddle, payloadType: payloadH265, timestamp: now, body: []byte("two-")},
		{dataType: typeIFrame, subpackage: subpackageLast, payloadType: payloadH265, timestamp: now, body: []byte("three")},
	}
	for index, part := range parts {
		part.channel = 1
		part.idBytes = []byte{0x01, 0x35, 0x12, 0x34, 0x50, 0x06}
		if _, err := decoder.Decode(buildPacket(part)); err != nil {
			t.Fatalf("part %d: %v", index, err)
		}
		if index < len(parts)-1 && len(sink.frames) != 0 {
			t.Fatalf("part %d produced a frame too early", index)
		}
	}

	if len(sink.frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(sink.frames))
	}
	if string(sink.frames[0].Payload) != "one-two-three" {
		t.Fatalf("payload = %q", sink.frames[0].Payload)
	}
	if sink.frames[0].Codec != codec.MediaCodecH265 {
		t.Fatalf("codec = %v", sink.frames[0].Codec)
	}
}

func TestDecoderIgnoresNonVideoPackets(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	sink := &captureSink{}
	decoder := NewDecoder(codec.Base{
		Resolver: stubResolver{deviceID: 7, uniqueID: "013512345006"},
		Media:    sink,
	})

	audio := buildPacket(packetSpec{
		channel: 1, dataType: 3, subpackage: subpackageAtomic,
		payloadType: 100, timestamp: now, body: []byte("audio"),
	})
	unknown := buildPacket(packetSpec{
		channel: 1, dataType: typeIFrame, subpackage: subpackageAtomic,
		payloadType: 42, timestamp: now, body: []byte("video"),
	})
	for _, packet := range [][]byte{audio, unknown} {
		if _, err := decoder.Decode(packet); err != nil {
			t.Fatalf("Decode: %v", err)
		}
	}
	if len(sink.frames) != 0 {
		t.Fatalf("frames = %d, want 0", len(sink.frames))
	}
}

func TestDecoderDropsUnknownDevice(t *testing.T) {
	sink := &captureSink{}
	decoder := NewDecoder(codec.Base{
		Resolver: stubResolver{deviceID: 7, uniqueID: "somebody-else"},
		Media:    sink,
	})

	result, err := decoder.Decode(keyFrame([]byte("idr"), time.UnixMilli(1700000000000)))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if result.UniqueID != "" {
		t.Fatalf("unique id = %q, want it unset so no session is bound", result.UniqueID)
	}
	if len(sink.frames) != 0 {
		t.Fatalf("frames = %d, want 0", len(sink.frames))
	}
}

func TestDecoderUsesTheBoundSession(t *testing.T) {
	sink := &captureSink{}
	decoder := NewDecoder(codec.Base{Media: sink})

	result, err := decoder.DecodeWithSession(
		keyFrame([]byte("idr"), time.UnixMilli(1700000000000)),
		&codec.DeviceSession{DeviceID: 11, UniqueID: "013512345006"},
	)
	if err != nil {
		t.Fatalf("DecodeWithSession: %v", err)
	}
	if !result.Consumed || len(sink.frames) != 1 || sink.frames[0].DeviceID != 11 {
		t.Fatalf("result = %+v frames = %d", result, len(sink.frames))
	}
}

func TestDecoderReadsTheTenByteIdentifier(t *testing.T) {
	sink := &captureSink{}
	decoder := NewDecoder(codec.Base{
		Resolver: stubResolver{deviceID: 9, uniqueID: "00000012345123456789"},
		Media:    sink,
	})

	packet := buildPacket(packetSpec{
		idBytes:     []byte{0x00, 0x00, 0x00, 0x12, 0x34, 0x51, 0x23, 0x45, 0x67, 0x89},
		channel:     1,
		dataType:    typeIFrame,
		subpackage:  subpackageAtomic,
		payloadType: payloadH264,
		timestamp:   time.UnixMilli(1700000000000),
		body:        []byte("idr"),
	})
	if _, err := decoder.Decode(packet); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(sink.frames) != 1 || sink.frames[0].DeviceID != 9 {
		t.Fatalf("frames = %d", len(sink.frames))
	}
}
