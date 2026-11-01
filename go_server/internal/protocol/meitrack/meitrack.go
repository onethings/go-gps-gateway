// Package meitrack implements the Meitrack text protocol. A frame starts with
// "$$", carries a three digit length, the fields separated by commas, and ends
// with "*" and an XOR checksum over the fields.
package meitrack

import (
	"fmt"
	"strconv"
	"strings"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and stored on positions.
const ProtocolName = "meitrack"

// FrameDecoder splits the stream at a line ending and drops a frame whose
// checksum does not match: a broken frame must not become a position.
type FrameDecoder struct{}

// DecodeFrame implements codec.FrameDecoder.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	start := indexOf(data, []byte("$$"))
	if start < 0 {
		// Nothing usable in the buffer; a text protocol resynchronises by
		// dropping what it has seen.
		if len(data) > 0 {
			return nil, len(data)
		}
		return nil, 0
	}
	end := indexOf(data[start:], []byte("\n"))
	if end < 0 {
		if start > 0 {
			return nil, start
		}
		return nil, 0
	}
	end += start
	frame := data[start:end]
	if length := len(frame); length > 0 && frame[length-1] == '\r' {
		frame = frame[:length-1]
	}
	return frame, end + 1
}

// Decoder parses one frame.
type Decoder struct {
	codec.Base
}

// NewDecoder creates the decoder.
func NewDecoder(base codec.Base) *Decoder { return &Decoder{Base: base} }

// Name implements codec.Decoder.
func (d *Decoder) Name() string { return ProtocolName }

// Decode implements codec.Decoder.
func (d *Decoder) Decode(frame []byte) (*codec.Result, error) {
	sentence := strings.TrimSpace(string(frame))
	if !strings.HasPrefix(sentence, "$$") {
		return &codec.Result{}, nil
	}

	body, checksum, found := strings.Cut(strings.TrimPrefix(sentence, "$$"), "*")
	if !found {
		return &codec.Result{}, nil
	}
	if expected, err := strconv.ParseUint(strings.TrimSpace(checksum), 16, 8); err == nil {
		if xor(body) != byte(expected) {
			return nil, fmt.Errorf("meitrack: checksum mismatch")
		}
	}

	// The first field is the length of everything after it, then the identifier
	// and the event code, then the position.
	fields := strings.Split(body, ",")
	if len(fields) < 6 {
		return &codec.Result{}, nil
	}

	uniqueID := strings.TrimSpace(fields[1])
	session := d.resolve(uniqueID)
	if session == nil {
		return &codec.Result{}, nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID
	position.Time = d.Time()

	latitude, err := helper.ParseCoordinate(fields[3], "")
	if err != nil {
		return &codec.Result{}, nil
	}
	longitude, err := helper.ParseCoordinate(fields[4], "")
	if err != nil {
		return &codec.Result{}, nil
	}
	position.Latitude = latitude
	position.Longitude = longitude
	if parsed, ok := helper.ParseStampYMDHMS(fields[5]); ok {
		position.Time = parsed
	}
	position.Valid = true

	if len(fields) > 6 {
		position.Speed = helper.ParseNumber(fields[6])
	}
	if len(fields) > 7 {
		position.Course = helper.ParseNumber(fields[7])
	}
	if len(fields) > 8 {
		position.Altitude = helper.ParseNumber(fields[8])
	}
	if len(fields) > 9 {
		position.Set(model.KeySatellites, helper.ParseNumber(fields[9]))
	}

	// The event code is kept so the alarm rules can see what the device reported.
	if event := strings.TrimSpace(fields[2]); event != "" {
		position.Set(model.KeyAlarm, event)
	}

	result := codec.NewResult()
	result.AddPosition(position)
	return result, nil
}

func (d *Decoder) resolve(uniqueID string) *codec.DeviceSession {
	if d.Resolver == nil {
		return nil
	}
	session, err := d.Resolver.Resolve(uniqueID)
	if err != nil {
		return nil
	}
	return session
}

// xor is the checksum Meitrack puts after the '*'.
func xor(body string) byte {
	var value byte
	for index := 0; index < len(body); index++ {
		value ^= body[index]
	}
	return value
}

func indexOf(haystack, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if string(haystack[index:index+len(needle)]) == string(needle) {
			return index
		}
	}
	return -1
}
