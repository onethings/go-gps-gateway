// Package tk103 implements the Xexun TK103 family. Frames are ASCII sentences
// that start with "imei:" and end at a line ending. Coordinates are the
// "ddmm.mmmm" form with a separate hemisphere, and the speed is in knots.
package tk103

import (
	"strings"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and stored on positions.
const ProtocolName = "tk103"

// FrameDecoder splits the stream at a line ending. Xexun frames are one sentence
// per line and never contain a bare newline.
type FrameDecoder struct{}

// DecodeFrame implements codec.FrameDecoder.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	for index, b := range data {
		if b == '\n' {
			end := index
			if end > 0 && data[end-1] == '\r' {
				end--
			}
			return data[:end], index + 1
		}
	}
	// Some firmwares omit the newline and send ";imei:" instead.
	if index := strings.Index(string(data), ";imei:"); index > 0 {
		return data[:index], index + 1
	}
	return nil, 0
}

// Decoder parses one sentence.
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
	if !strings.HasPrefix(sentence, "imei:") {
		return &codec.Result{}, nil
	}

	fields := strings.Split(sentence, ",")
	uniqueID := strings.TrimPrefix(fields[0], "imei:")
	if uniqueID == "" || len(fields) < 10 {
		return &codec.Result{}, nil
	}

	session := d.resolve(uniqueID)
	if session == nil {
		return &codec.Result{}, nil
	}

	// Field 1 is ddmmyyhhmmss. A "help me" or alarm frame repeats the same
	// layout, so the position is decoded the same way.
	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID
	position.Time = d.Time()
	if parsed, ok := helper.ParseStampDMYHMS(fields[1]); ok {
		position.Time = parsed
	}

	// 3 is F/L, 4 the sub second clock and 5 the A/V validity flag.
	position.Valid = !strings.EqualFold(strings.TrimSpace(fields[5]), "V") &&
		!strings.EqualFold(strings.TrimSpace(fields[3]), "L")

	latitude, err := helper.ParseDegMinHem(fields[6], fields[7])
	if err != nil {
		return &codec.Result{}, nil
	}
	longitude, err := helper.ParseDegMinHem(fields[8], fields[9])
	if err != nil {
		return &codec.Result{}, nil
	}
	position.Latitude = latitude
	position.Longitude = longitude

	if len(fields) > 10 {
		position.Speed = helper.ParseNumber(fields[10])
	}
	if len(fields) > 11 {
		position.Course = helper.ParseNumber(fields[11])
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
