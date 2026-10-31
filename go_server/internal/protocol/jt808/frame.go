// Package jt808 implements JT/T 808 (Ministry of Transport, China), the
// protocol used by the majority of Chinese vehicle terminals.
//
// A frame is delimited by 0x7E, the body never contains a raw 0x7E (escaped as
// 0x7D 0x02, 0x7D as 0x7D 0x01) and ends with an XOR checksum. Some terminals
// use 0xE7 as the delimiter with a different escape table.
package jt808

import "bytes"

// FrameDecoder splits a JT808 stream into frames. The returned frame keeps the
// delimiter at both ends and has its escape sequences already expanded, so the
// decoder can index it directly (a port of Jt808FrameDecoder).
type FrameDecoder struct{}

// NewFrameDecoder creates a JT808 frame decoder.
func NewFrameDecoder() *FrameDecoder { return &FrameDecoder{} }

// DecodeFrame returns one frame and the number of bytes consumed.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	start := 0
	for start < len(data) {
		if b := data[start]; b == '(' || b == 0x7e || b == 0xe7 {
			break
		}
		start++
	}
	if start >= len(data) {
		return nil, len(data)
	}
	if len(data)-start < 2 {
		return nil, start
	}

	delimiter := data[start]

	// ASCII sentence used by a few terminals for text results.
	if delimiter == '(' {
		if index := bytes.IndexByte(data[start+1:], ')'); index >= 0 {
			end := start + 1 + index + 1
			return data[start:end], end
		}
		return nil, start
	}

	alternative := delimiter == 0xe7
	index := bytes.IndexByte(data[start+1:], delimiter)
	if index < 0 {
		return nil, start
	}
	end := start + 1 + index

	frame := make([]byte, 0, end-start+1)
	for i := start; i <= end; i++ {
		b := data[i]
		switch {
		case alternative && (b == 0xe6 || b == 0x3e) && i < end:
			ext := data[i+1]
			i++
			switch {
			case b == 0xe6 && ext == 0x01:
				frame = append(frame, 0xe6)
			case b == 0xe6 && ext == 0x02:
				frame = append(frame, 0xe7)
			case b == 0x3e && ext == 0x01:
				frame = append(frame, 0x3e)
			case b == 0x3e && ext == 0x02:
				frame = append(frame, 0x3d)
			}
		case !alternative && b == 0x7d && i < end:
			ext := data[i+1]
			i++
			switch ext {
			case 0x01:
				frame = append(frame, 0x7d)
			case 0x02:
				frame = append(frame, 0x7e)
			}
		default:
			frame = append(frame, b)
		}
	}

	return frame, end + 1
}
