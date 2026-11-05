package startek

import (
	"bytes"
	"strconv"

	"traccar-server_go/go_server/internal/codec"
)

var header = []byte("&&")

// FrameDecoder splits the stream into Startek sentences. A sentence carries its
// own length right after the one character index, so the frame is complete as
// soon as that many bytes have arrived.
type FrameDecoder struct{}

// DecodeFrame implements codec.FrameDecoder.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	start := bytes.Index(data, header)
	if start < 0 {
		// Keep a trailing '&' that may be the first half of a header.
		if len(data) > 0 && data[len(data)-1] == '&' {
			return nil, len(data) - 1
		}
		return nil, len(data)
	}

	// Fewer bytes cannot hold the index, the length and the separator, so
	// nothing can be measured yet.
	if len(data)-start < 10 {
		return nil, start
	}

	divider := bytes.IndexByte(data[start+3:], ',')
	if divider < 0 {
		return nil, start
	}
	divider += start + 3

	length, err := strconv.Atoi(string(data[start+3 : divider]))
	if err != nil {
		// The header is not followed by a length, so it was payload noise.
		return nil, start + 1
	}

	// The length counts from the index character, three bytes before the first
	// digit, up to the end of the payload. The other four bytes are the index,
	// the separator and the two checksum digits.
	total := divider + 4 + length
	if len(data) < total {
		return nil, start
	}
	return data[start:total], total
}

var _ codec.FrameDecoder = (*FrameDecoder)(nil)
