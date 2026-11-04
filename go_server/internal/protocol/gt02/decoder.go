package gt02

import (
	"encoding/hex"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and stored on positions.
const ProtocolName = "gt02"

// Message types, matching the MSG_* constants in Gt02ProtocolDecoder.
const (
	msgData      = 0x10
	msgHeartbeat = 0x1A
	msgResponse  = 0x1C
)

// heartbeatResponse is the answer the device expects after a heartbeat.
var heartbeatResponse = []byte{0x54, 0x68, 0x1A, 0x0D, 0x0A}

// Decoder parses one GT02 frame.
type Decoder struct {
	codec.Base
}

// NewDecoder creates the decoder.
func NewDecoder(base codec.Base) *Decoder { return &Decoder{Base: base} }

// Name implements codec.Decoder.
func (d *Decoder) Name() string { return ProtocolName }

// Decode implements codec.Decoder.
func (d *Decoder) Decode(frame []byte) (*codec.Result, error) {
	buf := helper.NewBuffer(frame)
	buf.SkipBytes(3) // header and length

	position := model.NewPosition(ProtocolName)

	// Both are zero on location messages.
	power := buf.ReadUnsignedByte()
	gsm := buf.ReadUnsignedByte()

	// A frame cut short of the identifier cannot be attributed to a device.
	imei := buf.ReadSlice(8)
	if len(imei) < 8 {
		return &codec.Result{}, nil
	}

	// The leading nibble of the packed IMEI is not part of the identifier.
	uniqueID := hex.EncodeToString(imei)[1:]
	session := d.resolve(uniqueID)
	if session == nil {
		return &codec.Result{}, nil
	}
	position.DeviceID = session.DeviceID

	position.Set(model.KeyIndex, buf.ReadUnsignedShort())

	switch messageType := buf.ReadUnsignedByte(); messageType {
	case msgHeartbeat:
		d.copyLastLocation(position)
		position.Outdated = true
		position.Set(model.KeyPower, power)
		position.Set(model.KeyRSSI, gsm)

		result := codec.NewResult()
		result.AddResponse(heartbeatResponse)
		result.AddPosition(position)
		return result, nil

	case msgData:
		date := helper.NewDateBuilder()
		date.SetDate(buf.ReadUnsignedByte(), buf.ReadUnsignedByte(), buf.ReadUnsignedByte())
		date.SetTime(buf.ReadUnsignedByte(), buf.ReadUnsignedByte(), buf.ReadUnsignedByte())
		position.Time = date.Date()

		latitude := float64(buf.ReadUnsignedInt()) / (60 * 30000)
		longitude := float64(buf.ReadUnsignedInt()) / (60 * 30000)

		position.Speed = helper.KnotsFromKph(float64(buf.ReadUnsignedByte()))
		position.Course = float64(buf.ReadUnsignedShort())

		buf.SkipBytes(3) // reserved

		flags := int64(buf.ReadUnsignedInt())
		position.Valid = helper.CheckBit(flags, 0)
		if !helper.CheckBit(flags, 1) {
			latitude = -latitude
		}
		if !helper.CheckBit(flags, 2) {
			longitude = -longitude
		}
		position.Latitude = latitude
		position.Longitude = longitude

		result := codec.NewResult()
		result.AddPosition(position)
		return result, nil

	case msgResponse:
		d.copyLastLocation(position)
		position.Outdated = true
		position.Set(model.KeyResult, string(buf.ReadSlice(buf.ReadUnsignedByte())))

		result := codec.NewResult()
		result.AddPosition(position)
		return result, nil
	}

	return &codec.Result{}, nil
}

// copyLastLocation seeds a position from the last known fix, which heartbeat
// and response frames do not carry.
func (d *Decoder) copyLastLocation(position *model.Position) {
	if d.LastLocation == nil || position.DeviceID == 0 {
		return
	}
	last := d.LastLocation.LastLocation(position.DeviceID)
	if last == nil {
		return
	}
	position.Time = last.Time
	position.Latitude = last.Latitude
	position.Longitude = last.Longitude
	position.Altitude = last.Altitude
	position.Speed = last.Speed
	position.Course = last.Course
	position.Valid = last.Valid
	position.Accuracy = last.Accuracy
	position.Network = last.Network
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
