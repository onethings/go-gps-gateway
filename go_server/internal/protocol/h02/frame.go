// Package h02 implements the H02 (also sold as TK102/GT02 style) protocol used
// by many small Chinese trackers. Messages are either ASCII sentences starting
// with '*' or packed binary frames starting with '$'.
package h02

const (
	// MessageShort is the fixed length of the short binary frame.
	MessageShort = 32
	// MessageLong is the fixed length of the long binary frame.
	MessageLong = 45
)

// FrameDecoder splits the H02 stream into frames. A '*' sentence ends at '#',
// '$' binary frames have a fixed size that is latched from the first frame, and
// 'X' frames always use the short size.
type FrameDecoder struct {
	messageLength int
}

// NewFrameDecoder creates a frame decoder. A messageLength of 0 lets the
// decoder latch the size from the first '$' frame.
func NewFrameDecoder(messageLength int) *FrameDecoder {
	return &FrameDecoder{messageLength: messageLength}
}

// DecodeFrame returns one frame and the number of bytes consumed.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	if len(data) == 0 {
		return nil, 0
	}

	start := 0
	for start < len(data) && data[start] != '*' && data[start] != '$' && data[start] != 'X' {
		start++
	}
	if start == len(data) {
		// No marker in the buffer; drop the scanned bytes.
		return nil, len(data)
	}

	marker := data[start]
	remaining := len(data) - start

	switch marker {
	case '*':
		for i := start; i < len(data); i++ {
			if data[i] == '#' {
				end := i + 1
				frame := data[start:end]
				for end < len(data) && (data[end] == '\r' || data[end] == '\n') {
					end++
				}
				return frame, end
			}
		}
		return nil, start

	case '$':
		if d.messageLength == 0 {
			if remaining == MessageLong {
				d.messageLength = MessageLong
			} else {
				d.messageLength = MessageShort
			}
		}
		if remaining >= d.messageLength {
			return data[start : start+d.messageLength], start + d.messageLength
		}
		return nil, start

	case 'X':
		if remaining >= MessageShort {
			return data[start : start+MessageShort], start + MessageShort
		}
		return nil, start
	}

	return nil, 0
}
