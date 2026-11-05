package gt06

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
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
	msgWifi2           = 0x69
	msgWifi4           = 0xF3
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
	flags := modelFlagsOf(session.Model)
	acknowledge := true

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

	case messageType == msgCommand0 || messageType == msgCommand1 || messageType == msgCommand2:
		// The device answers a downlink command here. GT06 carries no result
		// code, so the frame itself means the command was executed, and it
		// carries no serial that would identify the request either, which is
		// why the gateway closes the newest sent command of the device. Like
		// the Java server, do not answer a command reply.
		result.CommandAck = &codec.CommandAck{Code: 0}
		return result, nil

	case messageType == msgInfo && !extended:
		d.copyLastLocation(position)
		position.Set(model.KeyPower, float64(buf.ReadShort())/100)
		result.AddPosition(position)

	case messageType == msgWifi || messageType == msgWifi2 || messageType == msgWifi4:
		clock := d.decodeWifi(position, buf, messageType, frame, d.location(session.UniqueID))
		result.AddPosition(position)
		// These devices answer a WiFi scan with the echoed clock instead of the
		// usual acknowledgement, and the 0xF3 layout is not answered at all.
		if messageType != msgWifi4 {
			result.AddResponse(echoClockResponse(messageType, clock))
			acknowledge = false
		}

	case messageType == msgLBSWifi:
		d.decodeLBSWifi(position, buf, d.location(session.UniqueID))
		result.AddPosition(position)
		acknowledge = false

	case d.isSupported(messageType, flags):
		position.Set(model.KeyType, messageType)

		// The NT models prefix their location message with the source, the IMEI
		// and the device clock, which the generic layout does not have.
		nt := flags.nt && isNTLocation(messageType)
		if nt {
			d.decodeNTPrefix(position, buf)
		}

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
			d.decodeLBS(position, buf, messageType, flags)
		}

		if d.hasStatus(messageType, flags) {
			d.decodeStatus(position, buf)
			if nt {
				d.decodeNTPower(position, buf, flags)
			} else {
				d.decodePower(position, buf, flags)
			}
		}

		if nt {
			d.decodeNTTrailer(position, buf, messageType)
		}

		result.AddPosition(position)

	default:
		// Unknown message: keep the stream in sync, still acknowledge.
		if dataLength > 0 {
			buf.SkipBytes(dataLength)
		}
	}

	if acknowledge {
		d.acknowledge(result, messageType, frame)
	}
	return result, nil
}

func (d *Decoder) isSupported(messageType int, flags modelFlags) bool {
	return d.hasGPS(messageType) || d.hasLBS(messageType) || d.hasStatus(messageType, flags)
}

// isNTLocation reports the two message types the NT models lay out differently.
func isNTLocation(messageType int) bool {
	return messageType == msgGPSLBS2 || messageType == msgGPSLBSDriver
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

// hasStatus reports whether the frame carries a status block. The two location
// types only do on the NT models, which is what makes their layout different:
// the status byte, the voltage and the battery follow the cell tower block.
func (d *Decoder) hasStatus(messageType int, flags modelFlags) bool {
	switch messageType {
	case msgStatus, msgGPSLBSStatus1, msgGPSLBSStatus2, msgGPSLBSStatus3,
		msgGPSLBSStatus4, msgGPSLBSStatus5, msgFenceMulti, msgAlarmModule:
		return true
	case msgGPSLBS2, msgGPSLBSDriver:
		return flags.nt
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
func (d *Decoder) decodeLBS(
	position *model.Position, buf *helper.Buffer, messageType int, flags modelFlags,
) bool {
	length := 0
	if d.hasStatus(messageType, flags) && messageType != msgLBSStatus && messageType != msgGPSLBSStatus5 {
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
	var cid int64
	if messageType == msgGPSLBSDriver {
		// The driver model reports a four byte cell id, the rest three.
		cid = int64(buf.ReadUnsignedInt())
	} else {
		cid = int64(buf.ReadUnsignedMedium())
	}

	position.SetNetwork(model.NewNetworkWith(
		model.NewCellTower(int(helper.BitsTo(int32(mcc), 15)), mnc, lac, cid)))

	if length > 9 {
		buf.SkipBytes(length - 9)
	}
	return true
}

// decodeWifi reads a WiFi scan frame and returns the six clock bytes the device
// echoes back in its answer. The access point count comes from the third byte
// of the frame, exactly as the reference reads it, so a frame whose length field
// is zero carries no access points and the cell block follows the clock. The
// cell entries of these frames have no signal strength byte, unlike the combined
// frame that decodeLBSWifi handles.
func (d *Decoder) decodeWifi(
	position *model.Position, buf *helper.Buffer, messageType int, frame []byte, location *time.Location,
) []byte {
	clock, when := readWifiTime(buf, location)
	position.Time = when

	network := model.NewNetwork()

	count := 0
	if messageType == msgWifi4 {
		count = buf.ReadUnsignedByte()
	} else if len(frame) > 2 {
		count = int(frame[2])
	}
	for i := 0; i < count; i++ {
		if messageType == msgWifi4 {
			buf.SkipBytes(2)
		}
		mac := readMac(buf)
		strength := 0
		if messageType != msgWifi4 {
			strength = buf.ReadUnsignedByte()
		}
		network.AddWifiAccessPoint(model.NewWifiAccessPoint(mac, strength))
	}

	if messageType != msgWifi4 {
		cellCount := buf.ReadUnsignedByte()
		mcc := buf.ReadUnsignedShort()
		mnc := buf.ReadUnsignedByte()
		for i := 0; i < cellCount; i++ {
			lac := buf.ReadUnsignedShort()
			cid := buf.ReadUnsignedMedium()
			network.AddCellTower(model.NewCellTower(mcc, mnc, lac, int64(cid)))
		}
	}

	position.SetNetwork(network)
	return clock
}

// decodeLBSWifi reads the combined cell and WiFi frame (0x2C). Unlike the plain
// WiFi frames it sends a clock in day, month, year order, a fixed block of seven
// cell towers and then the access point list.
func (d *Decoder) decodeLBSWifi(position *model.Position, buf *helper.Buffer, location *time.Location) {
	day := buf.ReadUnsignedByte()
	month := buf.ReadUnsignedByte()
	year := buf.ReadUnsignedByte()
	hour := buf.ReadUnsignedByte()
	minute := buf.ReadUnsignedByte()
	second := buf.ReadUnsignedByte()
	if year < 100 {
		year += 2000
	}
	position.Time = time.Date(year, time.Month(month), day, hour, minute, second, 0, location)

	mcc := buf.ReadUnsignedShort()
	mnc := buf.ReadUnsignedByte()
	if helper.CheckBit(int64(mcc), 15) {
		mnc = buf.ReadUnsignedShort()
	}

	network := model.NewNetwork()
	for i := 0; i < 7; i++ {
		lac := buf.ReadUnsignedShort()
		cid := buf.ReadUnsignedMedium()
		rssi := buf.ReadUnsignedByte()
		if lac > 0 {
			network.AddCellTower(model.NewCellTowerWithRSSI(
				int(helper.BitsTo(int32(mcc), 15)), mnc, lac, int64(cid), -rssi))
		}
	}

	buf.ReadUnsignedByte() // timing advance
	count := buf.ReadUnsignedByte()
	for i := 0; i < count; i++ {
		network.AddWifiAccessPoint(model.NewWifiAccessPoint(readMac(buf), buf.ReadUnsignedByte()))
	}

	position.SetNetwork(network)
}

// readWifiTime reads the packed BCD clock of the WiFi frames.
func readWifiTime(buf *helper.Buffer, location *time.Location) ([]byte, time.Time) {
	start := buf.ReaderIndex()
	year := helper.BcdReadInteger(buf, 2)
	month := helper.BcdReadInteger(buf, 2)
	day := helper.BcdReadInteger(buf, 2)
	hour := helper.BcdReadInteger(buf, 2)
	minute := helper.BcdReadInteger(buf, 2)
	second := helper.BcdReadInteger(buf, 2)
	if year < 100 {
		year += 2000
	}
	clock := buf.Bytes()[start:buf.ReaderIndex()]
	return clock, time.Date(year, time.Month(month), day, hour, minute, second, 0, location)
}

func readMac(buf *helper.Buffer) string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		buf.ReadUnsignedByte(), buf.ReadUnsignedByte(), buf.ReadUnsignedByte(),
		buf.ReadUnsignedByte(), buf.ReadUnsignedByte(), buf.ReadUnsignedByte())
}

// echoClockResponse builds the WiFi scan answer: the short header with a zero
// length, the message type, the clock the device sent and the line ending.
func echoClockResponse(messageType int, clock []byte) []byte {
	frame := []byte{0x78, 0x78, 0x00, byte(messageType)}
	frame = append(frame, clock...)
	return append(frame, '\r', '\n')
}

// decodeNTPrefix reads what an NT model puts in front of the location block of
// MSG_GPS_LBS_2 and MSG_GPS_LBS_DRIVER: the location source, the IMEI and the
// device clock. The Go model has no field for the device clock, so those six
// bytes are consumed and dropped; the GPS block carries the fix time anyway.
func (d *Decoder) decodeNTPrefix(position *model.Position, buf *helper.Buffer) {
	buf.ReadUnsignedByte() // location source type
	buf.SkipBytes(8)       // imei
	buf.SkipBytes(6)       // device clock, packed BCD
}

// decodeNTPower reads the block an NT model reports after the status byte: the
// supply voltage in hundredths of a volt, the battery in tenths, the signal, the
// alarm byte and the language. It is a different layout from decodePower, which
// is why the same frame means something else without an NT model.
func (d *Decoder) decodeNTPower(position *model.Position, buf *helper.Buffer, flags modelFlags) {
	position.Set(model.KeyPower, float64(buf.ReadUnsignedShort())/100)
	position.Set(model.KeyBattery, float64(buf.ReadUnsignedByte())/10)
	position.Set(model.KeyRSSI, buf.ReadUnsignedByte())
	if alarm := d.decodeAlarm(buf.ReadUnsignedByte(), flags); alarm != "" {
		position.AddAlarm(alarm)
	}
	buf.ReadUnsignedByte() // language
}

// decodeNTTrailer reads the odometer, the engine hours and, for the driver
// model, the driver card that an NT model puts after the status block.
func (d *Decoder) decodeNTTrailer(position *model.Position, buf *helper.Buffer, messageType int) {
	if buf.ReadableBytes() < 6 {
		return
	}
	position.Set(model.KeyOdometer, buf.ReadUnsignedMedium())
	position.Set(model.KeyHours, int64(buf.ReadUnsignedMedium())*60*1000)

	if messageType != msgGPSLBSDriver {
		return
	}

	length := buf.ReadUnsignedByte()
	if length == 0 || buf.ReadableBytes() < length {
		return
	}
	buf.ReadUnsignedByte() // driver identifier type
	position.Set(model.KeyDriverID, hex.EncodeToString(buf.ReadSlice(length-1)))
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
func (d *Decoder) decodePower(position *model.Position, buf *helper.Buffer, flags modelFlags) {
	battery := buf.ReadUnsignedByte()
	if battery <= 6 {
		position.Set(model.KeyBatteryLevel, battery*100/6)
	} else if battery <= 100 {
		position.Set(model.KeyBatteryLevel, battery)
	}

	position.Set(model.KeyRSSI, buf.ReadUnsignedByte())

	if alarm := d.decodeAlarm(buf.ReadUnsignedByte(), flags); alarm != "" {
		position.AddAlarm(alarm)
	}
}

// modelFlags are the per model branches Gt06ProtocolDecoder switches on. Most of
// them change what the same alarm byte means; nt changes how two message types
// are laid out at all.
type modelFlags struct {
	lw bool // model starts with LW
	sw bool // model is SEEWORLD
	vl bool // model is in vlModels
	nt bool // model is in ntModels
}

// ntModels lay MSG_GPS_LBS_2 and MSG_GPS_LBS_DRIVER out differently: a location
// prefix, a status block, and odometer and engine hours after it.
var ntModels = map[string]bool{
	"NT11":  true,
	"NT20":  true,
	"NT26":  true,
	"NT40":  true,
	"NT46":  true,
	"VL100": true,
	"XT40":  true,
}

// vlModels are the models that repurpose several alarm bytes.
var vlModels = map[string]bool{
	"VL103": true,
	"LL303": true,
	"VL512": true,
	"G18":   true,
}

// modelFlagsOf derives the branches from the device model. Java upper cases the
// model before comparing, so a lower case row behaves the same here.
func modelFlagsOf(model string) modelFlags {
	upper := strings.ToUpper(model)
	return modelFlags{
		lw: strings.HasPrefix(upper, "LW"),
		sw: upper == "SEEWORLD",
		vl: vlModels[upper],
		nt: ntModels[upper],
	}
}

// decodeAlarm maps the device alarm byte, ported from Gt06ProtocolDecoder.
func (d *Decoder) decodeAlarm(value int, flags modelFlags) string {
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
		if flags.vl {
			return model.AlarmTow
		}
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
		if flags.lw {
			return model.AlarmAccident
		}
		return model.AlarmRemoving
	case 0x19:
		if flags.lw {
			return model.AlarmHardAcceleration
		}
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
		if flags.sw {
			return model.AlarmHardCornering
		}
		if flags.vl {
			return model.AlarmPowerOff
		}
		return model.AlarmHardBraking
	case 0x29:
		if flags.sw {
			return model.AlarmAccident
		}
		return model.AlarmHardAcceleration
	case 0x2C, 0xF2:
		return model.AlarmAccident
	case 0x30:
		if flags.vl {
			return model.AlarmHardBraking
		}
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
