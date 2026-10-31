// Package teltonika implements the Teltonika (FMxxx, FMB, TAT, MTB) protocol.
// The device first sends its IMEI in a 2 byte length prefixed packet, then
// sends Codec 8 / 8E / 12 / 13 data packets, each carrying one or more AVL
// records. Both rely on the CRC16/IBM checksum.
package teltonika

import "encoding/binary"

// minFrameLength is the smallest possible data packet (4 byte length field,
// codec, count, one byte of record payload and the 8 byte trailer).
const minFrameLength = 12

// FrameDecoder splits a Teltonika stream into frames. A port of
// TeltonikaFrameDecoder.
type FrameDecoder struct{}

// NewFrameDecoder creates a stateless Teltonika frame decoder.
func NewFrameDecoder() *FrameDecoder { return &FrameDecoder{} }

// DecodeFrame returns one complete frame and the number of bytes consumed.
// A lone 0xFF byte is the device heartbeat and is returned as its own frame.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	if len(data) > 0 && data[0] == 0xff {
		return data[:1], 1
	}
	if len(data) < minFrameLength {
		return nil, 0
	}

	// IMEI packet: 2 byte length followed by the ASCII identifier.
	if length := int(binary.BigEndian.Uint16(data)); length > 0 {
		if len(data) >= length+2 {
			return data[:length+2], length + 2
		}
		return nil, 0
	}

	// Data packet: four zero bytes, then the 4 byte data length.
	dataLength := int(binary.BigEndian.Uint32(data[4:]))
	if len(data) >= dataLength+12 {
		return data[:dataLength+12], dataLength + 12
	}
	return nil, 0
}
