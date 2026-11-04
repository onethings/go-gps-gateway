package gator

import (
	"encoding/hex"
	"fmt"
	"strconv"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and stored on positions.
const ProtocolName = "gator"

const (
	msgHeartbeat        = 0x21
	msgPositionData     = 0x80
	msgRollcallResponse = 0x81
	msgAlarmData        = 0x82
	msgBlindArea        = 0x8E
)

// Decoder parses Gator frames into positions.
type Decoder struct {
	codec.Base
}

// NewDecoder creates a Gator decoder.
func NewDecoder(base codec.Base) *Decoder {
	return &Decoder{Base: base}
}

// Name implements codec.Decoder.
func (d *Decoder) Name() string {
	return ProtocolName
}

// Decode implements codec.Decoder.
func (d *Decoder) Decode(frame []byte) (*codec.Result, error) {
	if len(frame) < 5 {
		return &codec.Result{}, nil
	}

	buf := helper.NewBuffer(frame)
	buf.SkipBytes(2) // header
	messageType := buf.ReadUnsignedByte()
	buf.ReadUnsignedShort() // length

	ids, m588 := deviceIDs(buf)

	result := codec.NewResult()
	result.AddResponse(acknowledgement(frame, messageType))

	switch messageType {
	case msgPositionData, msgRollcallResponse, msgAlarmData, msgBlindArea:
	default:
		return result, nil
	}

	session, err := d.resolve(ids)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return &codec.Result{}, nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID

	position.Time = helper.NewDateBuilder().
		SetYear(helper.BcdReadInteger(buf, 2)).
		SetMonth(helper.BcdReadInteger(buf, 2)).
		SetDay(helper.BcdReadInteger(buf, 2)).
		SetHour(helper.BcdReadInteger(buf, 2)).
		SetMinute(helper.BcdReadInteger(buf, 2)).
		SetSecond(helper.BcdReadInteger(buf, 2)).
		Date()

	position.Latitude = helper.BcdReadCoordinate(buf)
	position.Longitude = helper.BcdReadCoordinate(buf)
	position.Speed = helper.KnotsFromKph(float64(helper.BcdReadInteger(buf, 4)))
	position.Course = float64(helper.BcdReadInteger(buf, 4))

	flags := buf.ReadUnsignedByte()
	position.Valid = flags&0x80 != 0
	position.Set(model.KeySatellites, flags&0x0f)

	position.Set(model.KeyStatus, buf.ReadUnsignedByte())
	position.Set("key", buf.ReadUnsignedByte())

	position.SetIndexed(model.PrefixADC, 1, readADC(buf))
	position.SetIndexed(model.PrefixADC, 2, readADC(buf))

	position.Set(model.KeyOdometer, buf.ReadUnsignedInt())

	if m588 && buf.ReadableBytes() >= 5+2 {
		// Two reserved shorts, then the driving behaviour alarm byte.
		buf.ReadUnsignedShort()
		buf.ReadUnsignedShort()
		alarm := buf.ReadUnsignedByte()
		if helper.CheckBit(int64(alarm), 0) {
			position.AddAlarm(model.AlarmAcceleration)
		}
		if helper.CheckBit(int64(alarm), 1) {
			position.AddAlarm(model.AlarmBraking)
		}
		if helper.CheckBit(int64(alarm), 2) {
			position.AddAlarm(model.AlarmCornering)
		}
	}

	if messageType == msgAlarmData {
		alarm1 := buf.ReadUnsignedByte()
		if helper.CheckBit(int64(alarm1), 0) {
			position.AddAlarm(model.AlarmBraking)
		}
		if helper.CheckBit(int64(alarm1), 5) {
			position.AddAlarm(model.AlarmAcceleration)
		}

		alarm2 := buf.ReadUnsignedByte()
		if helper.CheckBit(int64(alarm2), 1) {
			position.AddAlarm(model.AlarmOverspeed)
		}
		if helper.CheckBit(int64(alarm2), 4) {
			position.AddAlarm(model.AlarmCornering)
		}
	}

	result.AddPosition(position)
	return result, nil
}

// decodeID unpacks the four pseudo-IP bytes into the ten digit identifier.
func decodeID(b1, b2, b3, b4 int) string {
	d1 := 30 + ((b1 >> 7) << 3) + ((b2 >> 7) << 2) + ((b3 >> 7) << 1) + (b4 >> 7)

	return fmt.Sprintf("%02d%02d%02d%02d%02d",
		d1, b1&0x7F, b2&0x7F, b3&0x7F, b4&0x7F)
}

// deviceIDs reads the identifier and reports whether the frame is the M588
// variant, whose eight byte payload starts with a Luhn checked IMEI.
func deviceIDs(buf *helper.Buffer) ([]string, bool) {
	if imei, ok := m588IMEI(buf); ok {
		buf.SkipBytes(8)
		return []string{imei}, true
	}

	id := decodeID(
		buf.ReadUnsignedByte(), buf.ReadUnsignedByte(),
		buf.ReadUnsignedByte(), buf.ReadUnsignedByte())
	return []string{"1" + id, id}, false
}

func m588IMEI(buf *helper.Buffer) (string, bool) {
	if buf.ReadableBytes() <= 8 {
		return "", false
	}

	start := buf.ReaderIndex()
	imei := hex.EncodeToString(buf.Bytes()[start : start+8])[:15]
	for _, digit := range imei {
		if digit < '0' || digit > '9' {
			return "", false
		}
	}

	number, err := strconv.ParseInt(imei[:14], 10, 64)
	if err != nil {
		return "", false
	}
	check, err := strconv.Atoi(imei[14:])
	if err != nil || helper.Luhn(number) != check {
		return "", false
	}

	return imei, true
}

func readADC(buf *helper.Buffer) float64 {
	return float64(buf.ReadUnsignedByte()) + float64(buf.ReadUnsignedByte())/100.0
}

// acknowledgement echoes the heartbeat with the device checksum and the type
// being answered, which is the only reply the protocol defines.
func acknowledgement(frame []byte, messageType int) []byte {
	response := []byte{
		0x24, 0x24, msgHeartbeat, 0x00, 0x05,
		frame[len(frame)-2], byte(messageType), 0x00,
	}
	response = append(response, helper.XOR(response[2:]))
	return append(response, 0x0D)
}

func (d *Decoder) resolve(ids []string) (*codec.DeviceSession, error) {
	if d.Resolver == nil {
		return nil, nil
	}

	for _, id := range ids {
		session, err := d.Resolver.Resolve(id)
		if err != nil {
			return nil, err
		}
		if session != nil {
			return session, nil
		}
	}
	return nil, nil
}

var _ codec.Decoder = (*Decoder)(nil)
