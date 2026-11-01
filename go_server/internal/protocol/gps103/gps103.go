// Package gps103 implements the Coban GPS103 / TK102 family. Frames are ASCII
// sentences that start with "imei:" and end with ';' or a line ending.
// Coordinates are decimal degrees with a separate hemisphere field, and the
// speed is in knots.
package gps103

import (
	"fmt"
	"strings"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and stored on positions.
const ProtocolName = "gps103"

// FrameDecoder splits the stream at ';' or at a line ending, whichever comes
// first.
type FrameDecoder struct{}

// DecodeFrame implements codec.FrameDecoder.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	for index, b := range data {
		if b == ';' {
			return data[:index], index + 1
		}
		if b == '\n' {
			// Swallow the CR of a CRLF pair so the frame never ends with it.
			end := index
			if end > 0 && data[end-1] == '\r' {
				end--
			}
			return data[:end], index + 1
		}
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
	if sentence == "" {
		return &codec.Result{}, nil
	}
	// A leading '*' belongs to the acknowledgement frames the device sends for
	// our own commands; they carry no position.
	if strings.HasPrefix(sentence, "*") {
		return &codec.Result{}, nil
	}
	if !strings.HasPrefix(sentence, "imei:") {
		return &codec.Result{}, nil
	}

	fields := strings.Split(sentence, ",")
	uniqueID := strings.TrimPrefix(fields[0], "imei:")
	if uniqueID == "" || len(fields) < 2 {
		return &codec.Result{}, nil
	}

	session := d.resolve(uniqueID)
	if session == nil {
		return &codec.Result{}, nil
	}

	// A heartbeat only keeps the connection alive; the answer tells the device
	// that the server is there.
	if strings.EqualFold(fields[1], "heartbeat") {
		result := codec.NewResult()
		result.AddResponse([]byte(fmt.Sprintf("**,imei:%s;B", uniqueID)))
		return result, nil
	}

	if len(fields) < 11 {
		return &codec.Result{}, nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID
	position.Time = d.Time()
	position.Valid = true

	// Field 2 is the device clock as ddmmyyhhmmss; a frame without a usable
	// stamp keeps the server clock so the position is never dated 1970.
	if parsed, ok := helper.ParseStampDMYHMS(fields[2]); ok {
		position.Time = parsed
	}
	if strings.EqualFold(strings.TrimSpace(fields[6]), "V") {
		position.Valid = false
	}

	latitude, err := helper.ParseCoordinate(fields[7], fields[8])
	if err != nil {
		return &codec.Result{}, nil
	}
	longitude, err := helper.ParseCoordinate(fields[9], fields[10])
	if err != nil {
		return &codec.Result{}, nil
	}
	position.Latitude = latitude
	position.Longitude = longitude

	position.Speed = helper.ParseNumber(fields[11])
	if len(fields) > 12 {
		position.Course = helper.ParseNumber(fields[12])
	}
	if len(fields) > 13 {
		position.Altitude = helper.ParseNumber(fields[13])
	}
	if signal := strings.TrimSpace(fields[4]); signal != "" {
		// F is a full fix and L a low signal; both are kept because the validity
		// flag follows the GPS state, not the signal level.
		position.Set("gpsSignal", signal)
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
