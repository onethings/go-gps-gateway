package hls

import (
	"encoding/binary"

	"traccar-server_go/go_server/internal/codec"
)

const (
	tsPacketSize   = 188
	pmtPID         = 0x1000
	videoPID       = 0x0100
	streamTypeH264 = 0x1b
	streamTypeH265 = 0x24
)

// tsWriter wraps H.264 or H.265 access units in MPEG-TS packets. It keeps the
// continuity counters of one stream, so a segment continues the numbering of
// the one before it.
type tsWriter struct {
	patCC   byte
	pmtCC   byte
	videoCC byte
}

// write appends one access unit. A key frame starts with a PAT and a PMT so a
// player that joins at the segment can find the elementary stream.
func (w *tsWriter) write(
	out []byte, payload []byte, ptsMillis int64, keyFrame bool, video codec.MediaCodec,
) []byte {
	if keyFrame {
		out = w.writePAT(out)
		out = w.writePMT(out, video == codec.MediaCodecH265)
	}
	pts := ptsMillis * 90
	pes := createPES(payload, pts, video == codec.MediaCodecH265)
	return w.writePES(out, pes, keyFrame, pts)
}

func (w *tsWriter) writePAT(out []byte) []byte {
	start := len(out)
	cc := w.patCC
	w.patCC++

	out = append(out, 0x47, 0x40, 0x00, 0x10|(cc&0x0f), 0x00)

	table := len(out)
	out = append(out,
		0x00,       // table id
		0xb0, 0x0d, // section syntax indicator and section length
		0x00, 0x01, // transport stream id
		0xc1,       // reserved, version 0, current
		0x00, 0x00, // section number, last section number
		0x00, 0x01, // program number
		0xe0|byte(pmtPID>>8), byte(pmtPID&0xff),
	)
	out = appendCRC(out, table)
	return padPacket(out, start)
}

func (w *tsWriter) writePMT(out []byte, h265 bool) []byte {
	start := len(out)
	cc := w.pmtCC
	w.pmtCC++

	out = append(out, 0x47, 0x40|byte(pmtPID>>8), byte(pmtPID&0xff), 0x10|(cc&0x0f), 0x00)

	table := len(out)
	streamType := byte(streamTypeH264)
	if h265 {
		streamType = streamTypeH265
	}
	out = append(out,
		0x02,       // table id
		0xb0, 0x12, // section syntax indicator and section length
		0x00, 0x01, // program number
		0xc1,       // reserved, version 0, current
		0x00, 0x00, // section number, last section number
		0xe0|byte(videoPID>>8), byte(videoPID&0xff), // PCR PID
		0xf0, 0x00, // program info length
		streamType,
		0xe0|byte(videoPID>>8), byte(videoPID&0xff),
		0xf0, 0x00, // ES info length
	)
	out = appendCRC(out, table)
	return padPacket(out, start)
}

// createPES builds the packetized elementary stream of one access unit. It
// prepends an access unit delimiter, which players use to find frame
// boundaries inside the transport stream.
func createPES(payload []byte, pts int64, h265 bool) []byte {
	audLength := 2
	if h265 {
		audLength = 3
	}
	length := len(payload) + 8 + audLength

	pes := make([]byte, 0, 9+audLength+len(payload))
	pes = append(pes, 0x00, 0x00, 0x01, 0xe0)
	if length > 0xffff {
		pes = append(pes, 0x00, 0x00)
	} else {
		pes = append(pes, byte(length>>8), byte(length))
	}
	pes = append(pes, 0x80, 0x80, 0x05)
	pes = append(pes,
		0x21|byte((pts>>29)&0x0e),
		byte(pts>>22),
		0x01|byte((pts>>14)&0xfe),
		byte(pts>>7),
		0x01|byte((pts<<1)&0xfe),
	)

	pes = append(pes, 0x00, 0x00, 0x00, 0x01)
	if h265 {
		pes = append(pes, 0x46, 0x01, 0x50)
	} else {
		pes = append(pes, 0x09, 0xf0)
	}
	return append(pes, payload...)
}

// writePES splits the elementary stream over transport packets. A key frame
// carries the PCR and the random access flag in an adaptation field.
func (w *tsWriter) writePES(out, pes []byte, keyFrame bool, pts int64) []byte {
	offset := 0
	first := true

	for offset < len(pes) {
		start := len(out)
		out = append(out, 0x47)
		pid := videoPID
		if first {
			pid |= 0x4000
		}
		out = append(out, byte(pid>>8), byte(pid))
		headerSize := 4

		cc := w.videoCC
		w.videoCC++

		switch {
		case first && keyFrame:
			out = append(out, 0x30|(cc&0x0f), 0x07, 0x50,
				byte(pts>>25), byte(pts>>17), byte(pts>>9), byte(pts>>1),
				byte((pts&1)<<7)|0x7e, 0x00)
			headerSize += 8
		default:
			remaining := len(pes) - offset
			available := tsPacketSize - 4
			if remaining < available {
				stuffing := available - remaining
				if stuffing == 1 {
					out = append(out, 0x30|(cc&0x0f), 0x00)
					headerSize++
				} else {
					out = append(out, 0x30|(cc&0x0f), byte(stuffing-1), 0x00)
					for i := 0; i < stuffing-2; i++ {
						out = append(out, 0xff)
					}
					headerSize += stuffing
				}
			} else {
				out = append(out, 0x10|(cc&0x0f))
			}
		}

		size := len(pes) - offset
		if size > tsPacketSize-headerSize {
			size = tsPacketSize - headerSize
		}
		out = append(out, pes[offset:offset+size]...)
		offset += size
		out = padPacket(out, start)
		first = false
	}
	return out
}

func appendCRC(out []byte, table int) []byte {
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], crc32MPEG2(out[table:]))
	return append(out, crc[:]...)
}

func padPacket(out []byte, start int) []byte {
	for len(out)-start < tsPacketSize {
		out = append(out, 0xff)
	}
	return out
}
