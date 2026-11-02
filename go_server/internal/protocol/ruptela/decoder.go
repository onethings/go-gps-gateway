package ruptela

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and on positions.
const ProtocolName = "ruptela"

// Message types, matching the MSG_* constants in RuptelaProtocolDecoder.
const (
	msgRecords             = 1
	msgDeviceConfiguration = 2
	msgDeviceVersion       = 3
	msgFirmwareUpdate      = 4
	msgSetConnection       = 5
	msgSetOdometer         = 6
	msgSMSViaGPRSResponse  = 7
	msgSMSViaGPRS          = 8
	msgDTCs                = 9
	msgIdentification      = 15
	msgHeartbeat           = 16
	msgSetIO               = 17
	msgFiles               = 37
	msgExtendedRecords     = 68
)

// Decoder decodes Ruptela frames into positions.
type Decoder struct {
	codec.Base
}

// NewDecoder builds a Ruptela decoder.
func NewDecoder(base codec.Base) *Decoder { return &Decoder{Base: base} }

// Name implements codec.Decoder.
func (d *Decoder) Name() string { return ProtocolName }

// Decode implements codec.Decoder.
func (d *Decoder) Decode(frame []byte) (*codec.Result, error) {
	result := codec.NewResult()

	buf := helper.NewBuffer(frame)
	buf.ReadUnsignedShort() // data length

	imei := fmt.Sprintf("%015d", buf.ReadLong())
	result.UniqueID = imei

	session, err := d.Resolver.Resolve(imei)
	if err != nil {
		return result, err
	}
	deviceID := session.DeviceID

	typeID := buf.ReadUnsignedByte()

	switch typeID {
	case msgRecords, msgExtendedRecords:
		buf.ReadUnsignedByte() // records left
		count := buf.ReadUnsignedByte()

		positions := make([]*model.Position, 0, count)
		for i := 0; i < count; i++ {
			position := d.newPosition(deviceID)
			position.Time = time.UnixMilli(int64(buf.ReadUnsignedInt()) * 1000)
			buf.ReadUnsignedByte() // timestamp extension

			if typeID == msgExtendedRecords {
				recordExtension := buf.ReadUnsignedByte()
				mergeRecordCount := helper.BitsFrom(int32(recordExtension), 4)
				currentRecord := helper.BitsTo(int32(recordExtension), 4)

				if currentRecord > 0 && int(currentRecord) <= int(mergeRecordCount) {
					if len(positions) == 0 {
						position = d.lastPosition(position)
					} else {
						position = positions[len(positions)-1]
						positions = positions[:len(positions)-1]
					}
				}
			}

			buf.ReadUnsignedByte() // priority (reserved)

			longitude := buf.ReadInt()
			latitude := buf.ReadInt()
			if longitude > int32(-2147483648) && latitude > int32(-2147483648) {
				position.Valid = true
				position.Longitude = float64(longitude) / 10000000.0
				position.Latitude = float64(latitude) / 10000000.0
				position.Altitude = float64(buf.ReadUnsignedShort()) / 10.0
				position.Course = float64(buf.ReadUnsignedShort()) / 100.0
				position.Set(model.KeySatellites, buf.ReadUnsignedByte())
				position.Speed = helper.KnotsFromKph(float64(buf.ReadUnsignedShort()))
				position.Set(model.KeyHDOP, float64(buf.ReadUnsignedByte())/10.0)
			} else {
				buf.SkipBytes(8)
				position = d.lastPosition(position)
			}

			if typeID == msgExtendedRecords {
				position.Set(model.KeyEvent, buf.ReadUnsignedShort())
			} else {
				position.Set(model.KeyEvent, buf.ReadUnsignedByte())
			}

			d.readParameters(position, buf, typeID, 1)
			d.readParameters(position, buf, typeID, 2)
			d.readParameters(position, buf, typeID, 4)
			d.readParameters(position, buf, typeID, 8)

			decodeDriver(position, model.PrefixIO+"126", model.PrefixIO+"127") // can driver
			decodeDriver(position, model.PrefixIO+"155", model.PrefixIO+"156") // tco driver

			tagIDPart1, _ := position.Get(model.PrefixIO + "760")
			tagIDPart2, _ := position.Get(model.PrefixIO + "761")
			if tagIDPart1 != nil && tagIDPart2 != nil {
				position.Set(model.PrefixIO+"760", nil)
				position.Set(model.PrefixIO+"761", nil)
				position.Set("tagId", strconv.FormatInt(asInt64(tagIDPart1), 16)+strconv.FormatInt(asInt64(tagIDPart2), 16))
			}

			positions = append(positions, position)
		}

		result.Positions = positions
		result.AddResponse([]byte{0x00, 0x02, 0x64, 0x01, 0x13, 0xbc})

	case msgDTCs:
		count := buf.ReadUnsignedByte()
		for i := 0; i < count; i++ {
			position := d.newPosition(deviceID)
			buf.ReadUnsignedByte() // reserved
			position.Time = time.UnixMilli(int64(buf.ReadUnsignedInt()) * 1000)
			position.Valid = true
			position.Longitude = float64(buf.ReadInt()) / 10000000.0
			position.Latitude = float64(buf.ReadInt()) / 10000000.0
			if buf.ReadUnsignedByte() == 2 {
				position.Set(model.KeyArchive, true)
			}
			position.Set(model.KeyDTCs, string(buf.ReadSlice(5)))
			result.AddPosition(position)
		}
		result.AddResponse([]byte{0x00, 0x02, 0x6d, 0x01, 0xc4, 0xa4})

	case msgFiles:
		subtype := buf.ReadUnsignedByte()
		source := buf.ReadUnsignedByte()

		if subtype == 2 {
			filename := buf.ReadSlice(8)
			total := buf.ReadUnsignedShort()
			current := buf.ReadUnsignedShort()
			if current < total-1 {
				content := make([]byte, 0, 12)
				content = append(content, byte(subtype), byte(source))
				content = append(content, filename...)
				content = binary.BigEndian.AppendUint16(content, uint16(current+1))
				result.AddResponse(encodeContent(msgFiles, content))
			}
		}

	case msgIdentification, msgHeartbeat:
		result.AddResponse(encodeContent(typeID, []byte{1}))

	default:
		result.AddPosition(d.decodeCommandResponse(buf, typeID, deviceID))
	}

	return result, nil
}

func (d *Decoder) newPosition(deviceID int64) *model.Position {
	return &model.Position{Protocol: ProtocolName, DeviceID: deviceID}
}

// lastPosition fills a position with the last known location of the device.
// Without a resolved session the position is left empty.
func (d *Decoder) lastPosition(position *model.Position) *model.Position {
	return position
}

func (d *Decoder) readParameters(position *model.Position, buf *helper.Buffer, typeID int, length int) {
	valueCount := buf.ReadUnsignedByte()
	for j := 0; j < valueCount; j++ {
		var id int
		if typeID == msgExtendedRecords {
			id = buf.ReadUnsignedShort()
		} else {
			id = buf.ReadUnsignedByte()
		}
		d.decodeParameter(position, id, buf, length)
	}
}

func (d *Decoder) decodeParameter(position *model.Position, id int, buf *helper.Buffer, length int) {
	switch id {
	case 2, 3, 4, 5:
		position.SetIndexed(model.PrefixIn, id-1, readValue(buf, length, false))
	case 13, 173:
		position.Set(model.KeyMotion, readValue(buf, length, false) > 0)
	case 20:
		position.SetIndexed(model.PrefixADC, 3, readValue(buf, length, false))
	case 21:
		position.SetIndexed(model.PrefixADC, 4, readValue(buf, length, false))
	case 22:
		position.SetIndexed(model.PrefixADC, 1, readValue(buf, length, false))
	case 23:
		position.SetIndexed(model.PrefixADC, 2, readValue(buf, length, false))
	case 29:
		position.Set(model.KeyPower, float64(readValue(buf, length, false))/1000.0)
	case 30:
		position.Set(model.KeyBattery, float64(readValue(buf, length, false))/1000.0)
	case 32:
		position.Set(model.KeyDeviceTmp, int8(readValue(buf, length, true)))
	case 34:
		position.Set(model.KeyDriverID, hex.EncodeToString(buf.ReadSlice(length)))
	case 39:
		position.Set(model.KeyEngineLd, readValue(buf, length, false))
	case 65:
		position.Set(model.KeyOdometer, readValue(buf, length, false))
	case 74:
		position.SetIndexed(model.PrefixTemp, 3, float64(readValue(buf, length, true))/10.0)
	case 78, 79, 80:
		position.SetIndexed(model.PrefixTemp, id-78, float64(readValue(buf, length, true))/10.0)
	case 88:
		if readValue(buf, length, false) > 0 {
			position.AddAlarm(model.AlarmJamming)
		}
	case 94:
		position.Set(model.KeyRPM, float64(readValue(buf, length, false))*0.25)
	case 95:
		position.Set(model.KeyOBDSpeed, readValue(buf, length, false))
	case 98:
		position.Set(model.KeyFuel, float64(readValue(buf, length, false))*100/255.0)
	case 100:
		position.Set(model.KeyFuelConsumption, float64(readValue(buf, length, false))/20.0)
	case 134:
		if readValue(buf, length, false) > 0 {
			position.AddAlarm(model.AlarmBraking)
		}
	case 136:
		if readValue(buf, length, false) > 0 {
			position.AddAlarm(model.AlarmAcceleration)
		}
	case 150:
		position.Set(model.KeyOperator, readValue(buf, length, false))
	case 163:
		position.Set(model.KeyOdometer, float64(readValue(buf, length, false))*5)
	case 164:
		position.Set(model.KeyOdometerTrip, float64(readValue(buf, length, false))*5)
	case 165:
		position.Set(model.KeyOBDSpeed, float64(readValue(buf, length, false))/256.0)
	case 166, 197:
		position.Set(model.KeyRPM, float64(readValue(buf, length, false))*0.125)
	case 170:
		position.Set(model.KeyCharge, readValue(buf, length, false) > 0)
	case 205:
		position.Set(model.KeyFuel, readValue(buf, length, false))
	case 207:
		position.Set(model.KeyFuelLevel, float64(readValue(buf, length, false))*0.4)
	case 208:
		position.Set(model.KeyFuelUsed, float64(readValue(buf, length, false))*0.5)
	case 251, 409:
		position.Set(model.KeyIgnition, readValue(buf, length, false) > 0)
	case 410:
		if readValue(buf, length, false) > 0 {
			position.AddAlarm(model.AlarmTow)
		}
	case 411:
		if readValue(buf, length, false) > 0 {
			position.AddAlarm(model.AlarmAccident)
		}
	case 415:
		if readValue(buf, length, false) == 0 {
			position.AddAlarm(model.AlarmGPSAntennaCut)
		}
	case 645:
		position.Set(model.KeyOBDOdo, float64(readValue(buf, length, false))*1000)
	case 758:
		if readValue(buf, length, false) == 1 {
			position.AddAlarm(model.AlarmTampering)
		}
	default:
		position.SetIndexed(model.PrefixIO, id, readValue(buf, length, false))
	}
}

// decodeCommandResponse builds the response position for the configuration,
// version, firmware update, SMS and set IO command answer messages.
func (d *Decoder) decodeCommandResponse(buf *helper.Buffer, typeID int, deviceID int64) *model.Position {
	position := d.newPosition(deviceID)
	position.Set(model.KeyType, typeID)

	switch typeID {
	case msgDeviceConfiguration, msgDeviceVersion, msgFirmwareUpdate, msgSMSViaGPRSResponse:
		data := buf.Bytes()[buf.ReaderIndex() : len(buf.Bytes())-2]
		position.Set(model.KeyResult, trimASCII(data))
		return position
	case msgSetIO:
		position.Set(model.KeyResult, buf.ReadUnsignedByte())
		return position
	default:
		return nil
	}
}

// readValue reads a signed or unsigned value of the given width (1, 2, 4 or 8
// bytes), matching readValue in RuptelaProtocolDecoder.
func readValue(buf *helper.Buffer, length int, signed bool) int64 {
	switch length {
	case 1:
		if signed {
			return int64(buf.ReadSignedByte())
		}
		return int64(buf.ReadUnsignedByte())
	case 2:
		if signed {
			return int64(buf.ReadShort())
		}
		return int64(buf.ReadUnsignedShort())
	case 4:
		if signed {
			return int64(buf.ReadInt())
		}
		return int64(buf.ReadUnsignedInt())
	default:
		return buf.ReadLong()
	}
}

// decodeDriver combines two 8 byte I/O values into an ASCII driver identifier.
func decodeDriver(position *model.Position, part1, part2 string) {
	value1, _ := position.Get(part1)
	value2, _ := position.Get(part2)
	if value1 != nil && value2 != nil {
		position.Set(part1, nil)
		position.Set(part2, nil)
		var id [16]byte
		binary.BigEndian.PutUint64(id[:8], asUint64(value1))
		binary.BigEndian.PutUint64(id[8:], asUint64(value2))
		position.Set(model.KeyDriverID, trimASCII(id[:]))
	}
}

func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		return int64(n)
	case int32:
		return int64(n)
	case uint:
		return int64(n)
	case int16:
		return int64(n)
	case uint16:
		return int64(n)
	case uint8:
		return int64(n)
	case int8:
		return int64(n)
	}
	return 0
}

func asUint64(v any) uint64 {
	switch n := v.(type) {
	case int64:
		return uint64(n)
	case int:
		return uint64(n)
	case uint32:
		return uint64(n)
	case uint64:
		return n
	case int32:
		return uint64(n)
	case uint:
		return uint64(n)
	case int16:
		return uint64(n)
	case uint16:
		return uint64(n)
	case uint8:
		return uint64(n)
	case int8:
		return uint64(n)
	}
	return 0
}

func trimASCII(data []byte) string {
	s := string(data)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == 0) {
		s = s[:len(s)-1]
	}
	return s
}

var _ codec.Decoder = (*Decoder)(nil)
