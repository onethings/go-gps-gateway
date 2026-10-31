// Package gt06 implements the GT06 family of protocols (GT06, GT02, TK100,
// Concox, Jimi IoT, Seeworld ...). Frames start with 0x7878 (short header,
// one byte length) or 0x7979 (extended header, two byte length) and end with
// 0x0D 0x0A.
package gt06

import "encoding/binary"

// FrameDecoder splits a GT06 stream into frames. A port of Gt06FrameDecoder.
type FrameDecoder struct{}

// NewFrameDecoder creates a stateless GT06 frame decoder.
func NewFrameDecoder() *FrameDecoder { return &FrameDecoder{} }

// DecodeFrame returns one complete frame and the number of bytes consumed.
// Incomplete data yields (nil, 0). Garbage before a header is skipped, and an
// unterminated frame is recovered at the next 0x0D 0x0A like the Java decoder.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	start := 0
	for start < len(data) && data[start] != 0x78 && data[start] != 0x79 {
		start++
	}
	if start >= len(data) {
		return nil, len(data)
	}

	remaining := data[start:]
	if len(remaining) < 5 {
		return nil, start
	}

	// The length field counts the payload: type + data + serial + checksum.
	var total int
	if remaining[0] == 0x79 {
		total = int(binary.BigEndian.Uint16(remaining[2:])) + 6
	} else {
		total = int(remaining[2]) + 5
	}

	if total >= 5 && len(remaining) >= total &&
		remaining[total-2] == 0x0d && remaining[total-1] == 0x0a {
		return remaining[:total], start + total
	}

	for i := start; i+1 < len(data); i++ {
		if data[i] == 0x0d && data[i+1] == 0x0a {
			return data[start : i+2], i + 2
		}
	}
	return nil, start
}
