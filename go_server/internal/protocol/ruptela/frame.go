// Package ruptela implements the Ruptela family of protocols (Ruptela FM,
// truck and tachograph units). Every frame starts with a two byte big endian
// length that covers the rest of the message, and each record carries an
// IMEI followed by a message type.
package ruptela

import "encoding/binary"

// maxFrameLength bounds one Ruptela frame. It matches the MAX_FRAME_LENGTH
// constant the Java LengthFieldBasedFrameDecoder is created with.
const maxFrameLength = 8192

// FrameDecoder splits a Ruptela stream into frames. A port of the
// LengthFieldBasedFrameDecoder(MAX_FRAME_LENGTH, 0, 2, 2, 0) pipeline step the
// Java protocol installs: the two byte length covers everything after it plus
// the message type and checksum, so a complete frame is length + 4 bytes.
type FrameDecoder struct{}

// NewFrameDecoder creates a stateless Ruptela frame decoder.
func NewFrameDecoder() *FrameDecoder { return &FrameDecoder{} }

// DecodeFrame returns one complete frame and the number of bytes consumed.
// The Java frame decoder applies a lengthAdjustment of 2, so the full frame
// is the two length bytes plus length plus the two adjustment bytes.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	if len(data) < 2 {
		return nil, 0
	}
	length := int(binary.BigEndian.Uint16(data[:2]))
	total := length + 4
	if total > maxFrameLength {
		return nil, 0
	}
	if len(data) < total {
		return nil, 0
	}
	return data[:total], total
}
