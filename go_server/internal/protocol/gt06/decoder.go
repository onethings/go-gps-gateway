package gt06

import (
	"encoding/binary"
	"encoding/hex"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and on positions.
const ProtocolName = "gt06"

// Message types, matching the MSG_* constants in Gt06ProtocolDecoder.
const (
	msgLogin           = 0x01
	msgGPS             = 0x10
	msgGPSLBS6         = 0x11
	msgGPSLBS1         = 0x12
	msgStatus          = 0x13
	msgSatellite       = 0x14
	msgString          = 0x15
	msgGPSLBSStatus1   = 0x16
	msgWifi            = 0x17
	msgLBSExtend       = 0x18
	msgLBSStatus       = 0x19
	msgGPSPhone        = 0x1A
	msgGPSLBSExtend    = 0x1E
	msgStringInfo      = 0x21
	msgGPSLBS2         = 0x22
	msgHeartbeat       = 0x23
	msgLBSMultiple3    = 0x24
	msgGPSLBSDriver    = 0x25
	msgGPSLBSStatus2   = 0x26
	msgGPSLBSStatus3   = 0x27
	msgLBSMultiple1    = 0x28
	msgAddressRequest  = 0x2A
	msgLBSWifi         = 0x2C
	msgGPSLBS4         = 0x2D
	msgLBSMultiple2    = 0x2E
	msgGPSLBS5         = 0x31
	msgGPSLBSStatus4   = 0x32
	msgGPSLBS3         = 0x37
	msgAlarmModule     = 0x39
	msgCommand0        = 0x80
	msgCommand1        = 0x81
	msgCommand2        = 0x82
	msgTimeRequest     = 0x8A
	msgInfo            = 0x94
	msgAddressResponse = 0x97
	msgGPSLBS7         = 0xA0
	msgGPSLBSStatus5   = 0xA2
	msgFenceMulti      = 0xA4
)

// minGPSBytes is the amount of data read by decodeGPS: date/time, satellites,
// latitude, longitude, speed and flags.
const minGPSBytes = 6 + 1 + 4 + 4 + 1 + 2

// Decoder decodes GT06 frames into positions. Devices expect an
// acknowledgement for every message, so NewDecoder enables Base.Ack.
type Decoder struct {
	codec.Base

	// Location is the time zone used to interpret the device clock. A login
	// frame may carry an offset, which is remembered per unique id and wins
	// over this value for that device.
	Location *time.Location

	timezones sync.Map // unique id -> *time.Location
}

// NewDecoder builds a GT06 decoder.
func NewDecoder(base codec.Base) *Decoder {
	base.Ack = true
	return &Decoder{Base: base, Location: time.UTC}
}

// Name implements codec.Decoder.
func (d *Decoder) Name() string { return ProtocolName }

// Decode implements codec.Decoder. Frames other than the login frame carry no
// device identifier, so they are ignored without a session; use
// DecodeWithSession from the gateway.
func (d *Decoder) Decode(frame []byte) (*codec.Result, error) {
	return d.decode(frame, nil)
}

// DecodeWithSession implements codec.SessionDecoder.
func (d *Decoder) DecodeWithSession(frame []byte, session *codec.DeviceSession) (*codec.Result, error) {
	return d.decode(frame, session)
}

func (d *Decoder) decode(frame []byte, session *codec.DeviceSession) (*codec.Result, error) {
	if len(frame) < 10 || frame[0] != 0x78 || frame[1] != frame[0] {
		return &codec.Result{}, nil
	}

	buf := helper.NewBuffer(frame)
	extended := buf.ReadUnsignedShort() != 0x7878
	var length int
	if extended {
		length = buf.ReadUnsignedShort()
	} else {
		length = buf.ReadUnsignedByte()
	}
	dataLength := length - 5
	messageType := buf.ReadUnsignedByte()

	result := codec.NewResult()

	if messageType == msgLogin {
		// The IMEI is BCD packed with a leading zero nibble.
		result.UniqueID = hex.EncodeToString(buf.ReadSlice(8))[1:]
		buf.ReadUnsignedShort() // device type
		if dataLength > 10 && buf.ReadableBytes() >= 2 {
			d.rememberTimezone(result.UniqueID, buf.ReadUnsignedShort())
		}
		d.acknowledge(result, messageType, frame)
		return result, nil
	}

	if session == nil {
		return &codec.Result{}, nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID

	switch {
	case messageType == msgHeartbeat:
		d.copyLastLocation(position)
		status := buf.ReadUnsignedByte()
		position.Set(model.KeyArmed, helper.CheckBit(int64(status), 0))
		position.Set(model.KeyIgnition, helper.CheckBit(int64(status), 1))
		position.Set(model.KeyCharge, helper.CheckBit(int64(status), 2))
		if buf.ReadableBytes() >= 2+6 {
			position.Set(model.KeyBattery, float64(buf.ReadUnsignedShort())/100)
		}
		if buf.ReadableBytes() >= 1+6 {
			position.Set(model.KeyRSSI, buf.ReadUnsignedByte())
		}
		result.AddPosition(position)

	case messageType == msgAddressRequest:
		result.AddResponse(d.addressResponse())
		return result, nil

	case messageType == msgTimeRequest:
		result.AddResponse(d.timeResponse())
		return result, nil

	case messageType == msgInfo && !extended:
		d.copyLastLocation(position)
		position.Set(model.KeyPower, float64(buf.ReadShort())/100)
		result.AddPosition(position)

	case d.isSupported(messageType):
		position.Set(model.KeyType, messageType)

		if d.hasGPS(messageType) {
			if buf.ReadableBytes() < minGPSBytes {
				d.acknowledge(result, messageType, frame)
				return result, nil
			}
			d.decodeGPS(position, buf, d.location(session.UniqueID))
		} else {
			d.copyLastLocation(position)
		}

		if d.hasLBS(messageType) && buf.ReadableBytes() > 6 {
			d.decodeLBS(position, buf, messageType)
		}

		if d.hasStatus(messageType) {
			d.decodeStatus(position, buf)
			d.decodePower(position, buf)
		}

		result.AddPosition(position)

	default:
		// Unknown message: keep the stream in sync, still acknowledge.
		if dataLength > 0 {
			buf.SkipBytes(dataLength)
		}
	}

	d.acknowledge(result, messageType, frame)
	return result, nil
}

func (d *Decoder) isSupported(messageType int) bool {
	return d.hasGPS(messageType) || d.hasLBS(messageType) || d.hasStatus(messageType)
}

func (d *Decoder) hasGPS(messageType int) bool {
	switch messageType {
	case msgGPS, msgGPSLBS1, msgGPSLBS2, msgGPSLBS3, msgGPSLBS4, msgGPSLBS5,
		msgGPSLBS6, msgGPSLBS7, msgGPSLBSDriver, msgGPSLBSStatus1,
		msgGPSLBSStatus2, msgGPSLBSStatus3, msgGPSLBSStatus4, msgGPSPhone,
		msgGPSLBSExtend, msgFenceMulti, msgAlarmModule:
		return true
	}
	return false
}

func (d *Decoder) hasLBS(messageType int) bool {
	switch messageType {
	case msgLBSStatus, msgGPSLBS1, msgGPSLBS2, msgGPSLBS3, msgGPSLBS4, msgGPSLBS5,
		msgGPSLBS6, msgGPSLBS7, msgGPSLBSDriver, msgGPSLBSStatus1, msgGPSLBSStatus2,
		msgGPSLBSStatus3, msgGPSLBSStatus4, msgFenceMulti, msgAlarmModule:
		return true
	}
	return false
}

func (d *Decoder) hasStatus(messageType int) bool {
	switch messageType {
	case msgStatus, msgGPSLBSStatus1, msgGPSLBSStatus2, msgGPSLBSStatus3,
		msgGPSLBSStatus4, msgGPSLBSStatus5, msgFenceMulti, msgAlarmModule:
		return true
	}
	return false
}

// decodeGPS reads the common GT06 location block: six date/time bytes,
// satellites, latitude, longitude, speed and the flag word that carries the
// fix, hemisphere and ignition bits.
func (d *Decoder) decodeGPS(position *model.Position, buf *helper.Buffer, location *time.Location) {
	year := buf.ReadUnsignedByte()
	month := buf.ReadUnsignedByte()
	day := buf.ReadUnsignedByte()
	hour := buf.ReadUnsignedByte()
	minute := buf.ReadUnsignedByte()
	second := buf.ReadUnsignedByte()
	if year < 100 {
		year += 2000
	}
	position.Time = time.Date(year, time.Month(month), day, hour, minute, second, 0, location)

	position.Set(model.KeySatellites, buf.ReadUnsignedByte()&0x0f)

	latitude := float64(buf.ReadUnsignedInt()) / 60.0 / 30000.0
	longitude := float64(buf.ReadUnsignedInt()) / 60.0 / 30000.0
	position.Speed = helper.KnotsFromKph(float64(buf.ReadUnsignedByte()))

	flags := int(buf.ReadUnsignedShort())
	position.Course = float64(helper.BitsTo(int32(flags), 10))
	position.Valid = helper.CheckBit(int64(flags), 12)
	if !helper.CheckBit(int64(flags), 10) {
		latitude = -latitude
	}
	if helper.CheckBit(int64(flags), 11) {
		longitude = -longitude
	}
	position.Latitude = latitude
	position.Longitude = longitude

	if helper.CheckBit(int64(flags), 14) {
		position.Set(model.KeyIgnition, helper.CheckBit(int64(flags), 15))
	}
}

// decodeLBS reads the cell tower block. Status messages prefix it with a length
// byte; the rest of the layout depends on the message type and the device.
func (d *Decoder) decodeLBS(position *model.Position, buf *helper.Buffer, messageType int) bool {
	length := 0
	if d.hasStatus(messageType) && messageType != msgLBSStatus && messageType != msgGPSLBSStatus5 {
		length = buf.ReadUnsignedByte()
		if length == 0 {
			if zeroed(buf, 9, 45) {
				buf.SkipBytes(min(buf.ReadableBytes(), 45))
			}
			return false
		}
	}

	mcc := buf.ReadUnsignedShort()
	var mnc int
	if helper.CheckBit(int64(mcc), 15) || messageType == msgGPSLBS6 {
		mnc = buf.ReadUnsignedShort()
	} else {
		mnc = buf.ReadUnsignedByte()
	}
	lac := buf.ReadUnsignedShort()
	cid := int64(buf.ReadUnsignedMedium())

	position.SetNetwork(model.NewNetworkWith(
		model.NewCellTower(int(helper.BitsTo(int32(mcc), 15)), mnc, lac, cid)))

	if length > 9 {
		buf.SkipBytes(length - 9)
	}
	return true
}

// decodeStatus reads the one byte status block shared by all status messages.
func (d *Decoder) decodeStatus(position *model.Position, buf *helper.Buffer) {
	status := buf.ReadUnsignedByte()

	position.Set(model.KeyStatus, status)
	position.Set(model.KeyIgnition, helper.CheckBit(int64(status), 1))
	position.Set(model.KeyCharge, helper.CheckBit(int64(status), 2))
	position.Set(model.KeyBlocked, helper.CheckBit(int64(status), 7))

	var alarm string
	switch helper.BitsBetween(int32(status), 3, 6) {
	case 1:
		alarm = model.AlarmVibration
	case 2:
		alarm = model.AlarmPowerCut
	case 3:
		alarm = model.AlarmLowBattery
	case 4:
		alarm = model.AlarmSOS
	case 6:
		alarm = model.AlarmGeofence
	case 7:
		alarm = model.AlarmRemoving
	}
	if alarm != "" {
		position.AddAlarm(alarm)
	}
}

// decodePower reads the battery level, RSSI and alarm nibble that follow the
// status block.
func (d *Decoder) decodePower(position *model.Position, buf *helper.Buffer) {
	battery := buf.ReadUnsignedByte()
	if battery <= 6 {
		position.Set(model.KeyBatteryLevel, battery*100/6)
	} else if battery <= 100 {
		position.Set(model.KeyBatteryLevel, battery)
	}

	position.Set(model.KeyRSSI, buf.ReadUnsignedByte())

	if alarm := d.decodeAlarm(buf.ReadUnsignedByte()); alarm != "" {
		position.AddAlarm(alarm)
	}
}

// decodeAlarm maps the device alarm byte, ported from Gt06ProtocolDecoder.
func (d *Decoder) decodeAlarm(value int) string {
	switch value {
	case 0x01:
		return model.AlarmSOS
	case 0x02:
		return model.AlarmPowerCut
	case 0x03:
		return model.AlarmVibration
	case 0x04:
		return model.AlarmGeofenceEnter
	case 0x05:
		return model.AlarmGeofenceExit
	case 0x06:
		return model.AlarmOverspeed
	case 0x09:
		return model.AlarmVibration
	case 0x0E, 0x0F:
		return model.AlarmLowBattery
	case 0x11:
		return model.AlarmPowerOff
	case 0x0C, 0x13, 0x25, 0x32:
		return model.AlarmTampering
	case 0x14:
		return model.AlarmDoor
	case 0x18:
		return model.AlarmRemoving
	case 0x19:
		return model.AlarmLowBattery
	case 0x1A, 0x27, 0xF1:
		return model.AlarmHardBraking
	case 0x1B, 0x2A, 0x2B, 0x2E:
		return model.AlarmHardCornering
	case 0x23:
		return model.AlarmFallDown
	case 0x26, 0xF0:
		return model.AlarmHardAcceleration
	case 0x28:
		return model.AlarmHardBraking
	case 0x29:
		return model.AlarmHardAcceleration
	case 0x2C, 0xF2:
		return model.AlarmAccident
	case 0x30:
		return model.AlarmJamming
	case 0x33:
		return model.AlarmLock
	case 0x34:
		return model.AlarmUnlock
	case 0x53:
		return model.AlarmFuelLeak
	case 0x5B, 0x5C:
		return model.AlarmTemperature
	case 0xC9:
		return model.AlarmIdle
	}
	return ""
}

// copyLastLocation seeds a position from the last known fix, used by messages
// that only report status or cell information.
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

func (d *Decoder) rememberTimezone(uniqueID string, extension int) {
	hours := (extension >> 4) / 100
	minutes := (extension >> 4) % 100
	offset := (hours*60 + minutes) * 60
	if extension&0x8 != 0 {
		offset = -offset
	}
	d.timezones.Store(uniqueID, time.FixedZone("", offset))
}

func (d *Decoder) location(uniqueID string) *time.Location {
	if value, ok := d.timezones.Load(uniqueID); ok {
		return value.(*time.Location)
	}
	if d.Location != nil {
		return d.Location
	}
	return time.UTC
}

func (d *Decoder) acknowledge(result *codec.Result, messageType int, frame []byte) {
	if !d.Ack {
		return
	}
	result.AddResponse(buildResponse(false, messageType, deviceSerial(frame), nil))
}

func (d *Decoder) addressResponse() []byte {
	const response = "NA&&NA&&0##"
	content := make([]byte, 0, 1+4+len(response))
	content = append(content, byte(len(response)), 0, 0, 0, 0)
	content = append(content, response...)
	return buildResponse(true, msgAddressResponse, 0, content)
}

func (d *Decoder) timeResponse() []byte {
	now := d.Time().UTC()
	content := []byte{
		byte(now.Year() - 2000), byte(now.Month()), byte(now.Day()),
		byte(now.Hour()), byte(now.Minute()), byte(now.Second()),
	}
	return buildResponse(false, msgTimeRequest, 0, content)
}

// deviceSerial returns the serial number stored in the last six bytes of a
// frame (serial, checksum, footer).
func deviceSerial(frame []byte) int {
	if len(frame) < 6 {
		return 0
	}
	return int(binary.BigEndian.Uint16(frame[len(frame)-6:]))
}

// buildResponse assembles a frame and appends the CRC16/X25 over everything
// after the header, mirroring Gt06ProtocolDecoder.sendResponse.
func buildResponse(extended bool, messageType, serial int, content []byte) []byte {
	length := 5 + len(content)
	frame := make([]byte, 0, length+5)
	if extended {
		frame = append(frame, 0x79, 0x79, byte(length>>8), byte(length))
	} else {
		frame = append(frame, 0x78, 0x78, byte(length))
	}
	frame = append(frame, byte(messageType))
	frame = append(frame, content...)
	frame = append(frame, byte(serial>>8), byte(serial))
	crc := helper.CRC16X25(frame[2:])
	frame = append(frame, byte(crc>>8), byte(crc), 0x0d, 0x0a)
	return frame
}

// zeroed reports whether the window [from, to) after the reader index holds
// only zero bytes.
func zeroed(buf *helper.Buffer, from, to int) bool {
	data := buf.Bytes()
	index := buf.ReaderIndex()
	for i := index + from; i < index+to && i < len(data); i++ {
		if data[i] != 0 {
			return false
		}
	}
	return true
}

var (
	_ codec.Decoder        = (*Decoder)(nil)
	_ codec.SessionDecoder = (*Decoder)(nil)
)
