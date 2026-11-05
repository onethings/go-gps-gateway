// Package jt1078 implements JT/T 1078 (Ministry of Transport, China), the
// media protocol a JT808 terminal uses to push live video to the gateway.
//
// A packet starts with the four byte magic 0x30316364, then a fixed header and
// the access unit bytes. Large access units are split into sub-packages that
// the decoder puts back together before it hands one frame to the media sink.
package jt1078

import (
	"bytes"
	"encoding/binary"
)

// ProtocolName is the registry name of the JT1078 server.
const ProtocolName = "jt1078"

// Magic is the four byte packet prefix defined by JT/T 1078.
var Magic = []byte{0x30, 0x31, 0x63, 0x64}

const (
	magicValue = 0x30316364
	// maxFrameSize is the largest packet the header can describe: the longest
	// identifier plus the largest body length.
	maxFrameSize = 18 + 10 + 4 + 2 + 0xffff
)

// FrameDecoder splits a JT1078 stream into packets. A packet is exactly as long
// as its header says, so the decoder never guesses where a packet ends.
type FrameDecoder struct{}

// NewFrameDecoder creates a JT1078 frame decoder.
func NewFrameDecoder() *FrameDecoder { return &FrameDecoder{} }

// DecodeFrame returns one packet and the number of bytes consumed. Bytes before
// the magic are skipped, and a magic split across two reads stays in the
// buffer.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	offset := 0
	for {
		index := bytes.Index(data[offset:], Magic)
		if index < 0 {
			return nil, len(data) - tail(data[offset:])
		}
		offset += index

		length, ok := frameLength(data[offset:])
		if !ok {
			return nil, offset
		}
		if length <= 0 || length > maxFrameSize {
			offset += len(Magic)
			continue
		}
		if len(data)-offset < length {
			return nil, offset
		}
		return data[offset : offset+length], offset + length
	}
}

// tail reports how many trailing bytes may be the start of a magic that the next
// read completes.
func tail(data []byte) int {
	keep := len(data)
	if keep > len(Magic)-1 {
		keep = len(Magic) - 1
	}
	for size := keep; size > 0; size-- {
		if bytes.Equal(data[len(data)-size:], Magic[:size]) {
			return size
		}
	}
	return 0
}

// frameLength computes the packet length from the header. The identifier field
// is either six or ten bytes wide; a leading zero pair marks the ten byte form.
func frameLength(data []byte) (int, bool) {
	if len(data) < 10 {
		return 0, false
	}

	idLength := 6
	if data[8] == 0 && data[9] == 0 {
		idLength = 10
	}

	typeIndex := 8 + idLength + 1
	bodyLengthIndex := typeIndex + 1 + 8
	if len(data) < bodyLengthIndex+2 {
		return 0, false
	}
	if data[typeIndex]>>4 <= 2 {
		bodyLengthIndex += 4
	}
	if len(data) < bodyLengthIndex+2 {
		return 0, false
	}

	bodyLength := int(binary.BigEndian.Uint16(data[bodyLengthIndex:]))
	return bodyLengthIndex + 2 + bodyLength, true
}
