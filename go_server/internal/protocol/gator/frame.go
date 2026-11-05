// Package gator implements the Gator tracker protocol, a port of
// org.traccar.protocol.GatorProtocolDecoder. A frame starts with the two byte
// header 24 24, then a message type and a two byte big endian length at offset
// 3, and is 5 + length bytes long: the length covers every byte after the
// header, including the trailing checksum and the 0D terminator.
package gator

import (
	"encoding/binary"

	"traccar-server_go/go_server/internal/codec"
)

// maxFrameLength bounds one frame. GatorProtocol uses the Traccar default.
const maxFrameLength = 1024

// headerByte is the first byte of the 24 24 frame marker.
const headerByte = 0x24

// FrameDecoder splits the Gator stream into frames, resynchronising on the
// 24 24 header after garbage or a truncated frame.
type FrameDecoder struct{}

// NewFrameDecoder creates a stateless Gator frame decoder.
func NewFrameDecoder() *FrameDecoder { return &FrameDecoder{} }

// DecodeFrame returns one complete frame and the number of bytes consumed, or
// (nil, 0) when the stream does not yet hold a full frame.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	start := indexHeader(data)
	if start < 0 {
		// A lone trailing 0x24 may be the first byte of the next header.
		if len(data) > 0 && data[len(data)-1] == headerByte {
			return nil, len(data) - 1
		}
		return nil, len(data)
	}

	if len(data)-start < 5 {
		return nil, start
	}

	total := 5 + int(binary.BigEndian.Uint16(data[start+3:]))
	if total > maxFrameLength {
		return nil, start + 2
	}
	if len(data)-start < total {
		return nil, start
	}

	return data[start : start+total], start + total
}

func indexHeader(data []byte) int {
	for i := 0; i+1 < len(data); i++ {
		if data[i] == headerByte && data[i+1] == headerByte {
			return i
		}
	}
	return -1
}

var _ codec.FrameDecoder = (*FrameDecoder)(nil)
