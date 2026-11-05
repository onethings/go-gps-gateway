// Package gt02 implements the GT02 tracker protocol, the binary relative of
// the ASCII GPS103 family. A frame is 0x68 0x68, one payload length byte, the
// payload and a 0x0D 0x0A terminator. A port of Gt02ProtocolDecoder, with the
// framing of the LengthFieldBasedFrameDecoder(MAX_FRAME_LENGTH, 2, 1, 2, 0)
// the Java protocol installs.
package gt02

import "bytes"

var header = []byte{0x68, 0x68}

// FrameDecoder splits the GT02 stream into frames. Bytes before a 0x68 0x68
// header are dropped, and a frame that has not fully arrived waits in the
// buffer.
type FrameDecoder struct{}

// NewFrameDecoder creates a GT02 frame decoder.
func NewFrameDecoder() *FrameDecoder { return &FrameDecoder{} }

// DecodeFrame implements codec.FrameDecoder.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	start := bytes.Index(data, header)
	if start < 0 {
		// Keep a trailing 0x68 that may be the first half of a header.
		if len(data) > 0 && data[len(data)-1] == header[0] {
			return nil, len(data) - 1
		}
		return nil, len(data)
	}

	if start+3 > len(data) {
		return nil, start
	}

	// The length byte counts everything between itself and the terminator.
	total := 3 + int(data[start+2]) + 2
	if start+total > len(data) {
		return nil, start
	}
	return data[start : start+total], start + total
}
