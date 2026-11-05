// Package gps056 implements the GPS056 tracker protocol. A frame is "$$", a
// two digit ASCII payload length, the payload and a '#' terminator. A port of
// Gps056FrameDecoder and Gps056ProtocolDecoder.
package gps056

import "bytes"

var header = []byte("$$")

// FrameDecoder splits the GPS056 stream into frames. Bytes before a "$$"
// header are dropped, as is everything between a frame and the next header,
// which is what the Java decoder does after slicing.
type FrameDecoder struct{}

// NewFrameDecoder creates a GPS056 frame decoder.
func NewFrameDecoder() *FrameDecoder { return &FrameDecoder{} }

// DecodeFrame implements codec.FrameDecoder.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	start := bytes.Index(data, header)
	if start < 0 {
		// Keep a trailing '$' that may be the first half of a header.
		if len(data) > 0 && data[len(data)-1] == '$' {
			return nil, len(data) - 1
		}
		return nil, len(data)
	}

	if start+4 > len(data) {
		return nil, start
	}

	length, ok := decimal(data[start+2], data[start+3])
	if !ok {
		return nil, start + 1
	}

	// The digits count everything between themselves and the terminator.
	total := length + 5
	if start+total > len(data) {
		return nil, start
	}

	end := start + total
	for end < len(data) && data[end] != '$' {
		end++
	}
	return data[start : start+total], end
}

func decimal(high, low byte) (int, bool) {
	if high < '0' || high > '9' || low < '0' || low > '9' {
		return 0, false
	}
	return int(high-'0')*10 + int(low-'0'), true
}
