package gps056

import (
	"strings"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and stored on positions.
const ProtocolName = "gps056"

// Message type prefixes of the seven character type field.
const (
	typeLogon = "LOGN"
	typeGPS   = "GPSL"
	typeSync  = "SYNC"
)

// Decoder parses one GPS056 frame.
type Decoder struct {
	codec.Base
}

// NewDecoder creates the decoder.
func NewDecoder(base codec.Base) *Decoder { return &Decoder{Base: base} }

// Name implements codec.Decoder.
func (d *Decoder) Name() string { return ProtocolName }

// Decode implements codec.Decoder.
func (d *Decoder) Decode(frame []byte) (*codec.Result, error) {
	// A frame cut short of the type and identifier fields cannot be attributed
	// to a device.
	buf := helper.NewBuffer(frame)
	if buf.ReadableBytes() < 2+2+7+15 {
		return &codec.Result{}, nil
	}

	buf.SkipBytes(2) // header
	buf.SkipBytes(2) // length

	messageType := string(buf.ReadSlice(7))
	uniqueID := string(buf.ReadSlice(15))

	session := d.resolve(uniqueID)
	if session == nil {
		return &codec.Result{}, nil
	}

	switch {
	case strings.HasPrefix(messageType, typeLogon):
		result := codec.NewResult()
		result.AddResponse(response("LGSA"+messageType[4:], uniqueID, []byte("1")))
		return result, nil

	case strings.HasPrefix(messageType, typeGPS):
		position := model.NewPosition(ProtocolName)
		position.DeviceID = session.DeviceID

		date := helper.NewDateBuilder()
		date.SetDateReverse(buf.ReadUnsignedByte(), buf.ReadUnsignedByte(), buf.ReadUnsignedByte())
		date.SetTime(buf.ReadUnsignedByte(), buf.ReadUnsignedByte(), buf.ReadUnsignedByte())

		position.Valid = true
		position.Time = date.Date()
		position.Latitude = decodeCoordinate(buf)
		position.Longitude = decodeCoordinate(buf)
		position.Speed = helper.KnotsFromKph(float64(buf.ReadUnsignedByte()))
		position.Course = float64(buf.ReadUnsignedShort())

		decodeStatus(buf, position)

		result := codec.NewResult()
		result.AddResponse(response("GPSA"+messageType[4:], uniqueID, buf.ReadSlice(2)))
		result.AddPosition(position)
		return result, nil

	case strings.HasPrefix(messageType, typeSync):
		position := model.NewPosition(ProtocolName)
		position.DeviceID = session.DeviceID
		d.copyLastLocation(position)
		position.Outdated = true

		decodeStatus(buf, position)

		result := codec.NewResult()
		result.AddResponse(response("SYSA"+messageType[4:], uniqueID, nil))
		result.AddPosition(position)
		return result, nil
	}

	return &codec.Result{}, nil
}

// decodeCoordinate reads a DDMM.MMMM coordinate and its hemisphere byte.
func decodeCoordinate(buf *helper.Buffer) float64 {
	degrees := float64(buf.GetUnsignedShort(0) / 100)
	minutes := float64(buf.ReadUnsignedShort()%100) + float64(buf.ReadUnsignedShort())/10000.0
	degrees += minutes / 60
	hemisphere := buf.ReadUnsignedByte()
	if hemisphere == 'S' || hemisphere == 'W' {
		degrees = -degrees
	}
	return degrees
}

func decodeStatus(buf *helper.Buffer, position *model.Position) {
	position.Set(model.KeyInput, buf.ReadUnsignedByte())
	position.Set(model.KeyOutput, buf.ReadUnsignedByte())

	low := buf.ReadUnsignedByte()
	high := buf.ReadUnsignedByte()
	position.SetIndexed(model.PrefixADC, 1, float64(int16(uint16(low)|uint16(high)<<8))*5.06)

	position.Set(model.KeySatellites, buf.ReadUnsignedByte())
	position.Set(model.KeyRSSI, buf.ReadUnsignedByte())
}

func response(messageType, uniqueID string, content []byte) []byte {
	data := make([]byte, 0, len(messageType)+len(uniqueID)+len(content)+2)
	data = append(data, '*')
	data = append(data, messageType...)
	data = append(data, uniqueID...)
	data = append(data, content...)
	return append(data, '#')
}

// copyLastLocation seeds a position from the last known fix, which sync frames
// do not carry.
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
