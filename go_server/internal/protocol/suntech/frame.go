// Package suntech implements the Suntech family of protocols. Frames come in
// three layouts: binary frames start with an STX byte, an alternate binary
// layout has a zero second byte, and everything else is a text frame
// terminated by a lone carriage return.
package suntech

import "encoding/binary"

// maxFrameLength bounds one Suntech frame. The ZIP/binary payloads are small,
// text frames are the largest so an upper bound of 8192 matches the Java
// decoder's practical limits.
const maxFrameLength = 8192

// FrameDecoder splits a Suntech stream into frames. A port of
// SuntechFrameDecoder.
type FrameDecoder struct{}

// NewFrameDecoder creates a stateless Suntech frame decoder.
func NewFrameDecoder() *FrameDecoder { return &FrameDecoder{} }

// DecodeFrame returns one complete frame and the number of bytes consumed, or
// (nil, 0) when the stream does not yet hold a full frame.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	if len(data) == 0 {
		return nil, 0
	}

	if data[0] == 0x02 {
		// ZIP frame: STX, two byte big endian length, payloadags and a
		// trailing footer byte.
		if len(data) < 4 {
			return nil, 0
		}
		length := 1 + 2 + int(binary.BigEndian.Uint16(data[1:3])) + 1
		if length > maxFrameLength {
			return nil, 0
		}
		if len(data) < length {
			return nil, 0
		}
		return data[:length], length
	}

	if data[1] == 0 {
		// Binary frame: type byte, two byte big endian length and payload.
		if len(data) < 3 {
			return nil, 0
		}
		length := 1 + 2 + int(binary.BigEndian.Uint16(data[1:3]))
		if length > maxFrameLength {
			return nil, 0
		}
		if len(data) < length {
			return nil, 0
		}
		return data[:length], length
	}

	// Text frame: split on the first lone '\r'. A '\r' followed by '\n' is
	// part of a CRLF line break and skipped.
	delimiter := -1
	for {
		idx := indexOf(data, delimiter+1, '\r')
		if idx < 0 {
			return nil, 0
		}
		if idx+1 < len(data) && data[idx+1] == '\n' {
			delimiter = idx
			continue
		}
		return data[:idx], idx + 1
	}
}

// indexOf returns the index of value at or after start, or -1.
func indexOf(data []byte, start int, value byte) int {
	for i := start; i < len(data); i++ {
		if data[i] == value {
			return i
		}
	}
	return -1
}
