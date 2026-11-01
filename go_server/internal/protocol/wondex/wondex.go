// Package wondex implements the Wondex text protocol. A frame starts with
// "#DD#", fields are separated by ";", and the line ends with a line ending.
// The speed is in knots and the coordinates are decimal degrees.
package wondex

import (
	"strings"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and stored on positions.
const ProtocolName = "wondex"

// FrameDecoder splits the stream at a line ending.
type FrameDecoder struct{}

// DecodeFrame implements codec.FrameDecoder.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	end := strings.IndexByte(string(data), '\n')
	if end < 0 {
		return nil, 0
	}
	frame := data[:end]
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
	if !strings.HasPrefix(sentence, "#DD#") {
		return &codec.Result{}, nil
	}

	fields := strings.Split(strings.TrimPrefix(sentence, "#DD#"), ";")
	if len(fields) < 5 {
		return &codec.Result{}, nil
	}

	uniqueID := strings.TrimSpace(fields[0])
	session := d.resolve(uniqueID)
	if session == nil {
		return &codec.Result{}, nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID
	position.Time = d.Time()
	if parsed, ok := helper.ParseStampYMDHMS(fields[1]); ok {
		position.Time = parsed
	}

	latitude, err := helper.ParseCoordinate(fields[2], "")
	if err != nil {
		return &codec.Result{}, nil
	}
	longitude, err := helper.ParseCoordinate(fields[3], "")
	if err != nil {
		return &codec.Result{}, nil
	}
	position.Latitude = latitude
	position.Longitude = longitude
	position.Valid = true

	position.Speed = helper.ParseNumber(fields[4])
	if len(fields) > 5 {
		position.Course = helper.ParseNumber(fields[5])
	}
	if len(fields) > 6 {
		position.Altitude = helper.ParseNumber(fields[6])
	}
	if len(fields) > 7 {
		position.Set(model.KeySatellites, helper.ParseNumber(fields[7]))
	}
	if len(fields) > 8 {
		position.Set(model.KeyHDOP, helper.ParseNumber(fields[8]))
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
