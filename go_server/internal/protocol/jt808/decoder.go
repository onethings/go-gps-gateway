package jt808

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and on positions.
const ProtocolName = "jt808"

// Decoder decodes JT808 messages into positions.
type Decoder struct {
	codec.Base

	// Location is the time zone used for the BCD timestamps. Traccar defaults
	// the device time zone for this protocol to GMT+8.
	Location *time.Location
}

// NewDecoder builds a JT808 decoder.
func NewDecoder(base codec.Base) *Decoder {
	return &Decoder{Base: base, Location: time.FixedZone("GMT+8", 8*60*60)}
}

// Name implements codec.Decoder.
func (d *Decoder) Name() string { return ProtocolName }

// Decode implements codec.Decoder. JT808 terminals identify themselves in the
// header, so a session can be resolved from the frame itself; DecodeWithSession
// is still supported for gateways that bind first.
func (d *Decoder) Decode(frame []byte) (*codec.Result, error) {
	return d.decode(frame, nil)
}

// DecodeWithSession implements codec.SessionDecoder.
func (d *Decoder) DecodeWithSession(frame []byte, session *codec.DeviceSession) (*codec.Result, error) {
	return d.decode(frame, session)
}

func (d *Decoder) decode(frame []byte, session *codec.DeviceSession) (*codec.Result, error) {
	result := codec.NewResult()

	if len(frame) > 0 && frame[0] == '(' {
		return d.decodeText(frame, session)
	}

	h, buf, ok := parseHeader(frame)
	if !ok {
		return &codec.Result{}, nil
	}

	// A sub-packet (分包) is one fragment of a larger transmission; its body
	// begins with the sub-packet header (total packet count and packet sequence
	// number) rather than a full message body. Treating it as a whole message
	// would misread that header as location data and record a bogus position, so
	// it is deliberately not consumed or acknowledged.
	if h.subPacket {
		result.Consumed = false
		return result, nil
	}

	uniqueID := DecodeID(h.id)
	result.UniqueID = uniqueID

	if session == nil {
		resolved := d.resolve(uniqueID)
		if resolved == nil {
			return &codec.Result{}, nil
		}
		session = resolved
	}

	switch h.messageType {
	case msgTerminalRegister:
		payload := []byte{byte(h.index >> 8), byte(h.index), msgResultSuccess}
		payload = append(payload, []byte(uniqueID)...)
		result.AddResponse(buildMessage(h, msgTerminalRegisterResp, payload))

	case msgTerminalAuth, msgHeartbeat, msgHeartbeat2, msgTerminalLogout, msgTimezoneSync:
		result.AddResponse(d.generalResponse(h))
		if h.messageType == msgHeartbeat && buf.ReadableBytes() >= 2+3 {
			position := d.newPosition(session)
			d.copyLastLocation(position)
			position.Set(model.KeyBatteryLevel, buf.ReadUnsignedByte())
			position.Set(model.KeyRSSI, buf.ReadUnsignedByte())
			position.Set(model.KeyStatus, buf.ReadUnsignedByte())
			result.AddPosition(position)
		}

	case msgTerminalAttributes:
		result.AddResponse(d.generalResponse(h))
		position := d.newPosition(session)
		d.copyLastLocation(position)
		buf.ReadUnsignedShort()   // terminal type
		buf.SkipBytes(5 + 20 + 7) // manufacturer, model, terminal id
		position.Set(model.KeyICCID, hex.EncodeToString(buf.ReadSlice(10)))
		result.AddPosition(position)

	case msgLocationReport:
		result.AddResponse(d.generalResponse(h))
		result.AddPosition(d.decodeLocation(session, buf))

	case msgDriverIdentity:
		result.AddPosition(d.decodeDriverIdentity(session, buf))

	case msgParameterQueryResp:
		result.AddPosition(d.decodeParameterQueryResponse(session, buf))

	case msgTransparent:
		result.AddResponse(d.generalResponse(h))
		if position := d.decodeTransparent(session, buf); position != nil {
			result.AddPosition(position)
		}

	case msgLocationQueryResp:
		buf.ReadUnsignedShort() // response serial number
		result.AddPosition(d.decodeLocation(session, buf))

	case msgLocationBatch, msgLocationBatch2:
		result.AddResponse(d.generalResponse(h))
		if h.messageType == msgLocationBatch2 && h.bodyLength == 7 {
			// A seven byte 0x0210 body is a battery level plus a packed BCD
			// timestamp for the last known fix, not a batch of locations.
			position := d.newPosition(session)
			d.copyLastLocation(position)
			position.Set(model.KeyBatteryLevel, buf.ReadUnsignedByte())
			position.Time = readDate(buf, d.location())
			result.AddPosition(position)
			break
		}
		for _, position := range d.decodeLocationBatch(session, buf, h.messageType) {
			result.AddPosition(position)
		}

	case msgTimeSyncRequest:
		now := d.Time().UTC()
		payload := []byte{
			byte(now.Year() >> 8), byte(now.Year()),
			byte(now.Month()), byte(now.Day()),
			byte(now.Hour()), byte(now.Minute()), byte(now.Second()),
			msgResultSuccess,
		}
		result.AddResponse(buildMessage(h, msgTimeSyncResponse, payload))

	case msgTerminalGeneralResponse:
		// The terminal answers a downlink message here. There is nothing to
		// reply with, so the frame only reports how the command ended.
		if buf.ReadableBytes() >= 5 {
			serial := buf.ReadUnsignedShort() // answered serial number
			buf.ReadUnsignedShort()           // answered message type
			result.CommandAck = &codec.CommandAck{
				Code:   int(buf.ReadUnsignedByte()),
				Serial: &serial,
			}
		} else {
			result.Consumed = false
		}

	default:
		// Unhandled or unsupported message: keep the connection alive.
		result.Consumed = false
	}

	return result, nil
}

func (d *Decoder) decodeText(frame []byte, session *codec.DeviceSession) (*codec.Result, error) {
	result := codec.NewResult()
	sentence := strings.TrimSpace(string(frame))
	position := d.newPosition(session)
	d.copyLastLocation(position)
	position.Set(model.KeyResult, sentence)
	result.AddPosition(position)
	return result, nil
}

// bodyLimit is the offset of the trailing checksum. The decoder receives the
// whole frame including checksum and delimiter, so every body read stops here.
func bodyLimit(buf *helper.Buffer) int {
	return len(buf.Bytes()) - 2
}

// readField reads length bytes of the body, clamped to limit. A length that
// runs past the body is a malformed message, not a reason to fail the decode.
func readField(buf *helper.Buffer, length, limit int) []byte {
	if available := limit - buf.ReaderIndex(); length > available {
		length = available
	}
	if length <= 0 {
		return nil
	}
	return buf.ReadSlice(length)
}

// javaTrim strips what Java's String.trim() strips, which is every character
// at or below the space, so fixed width fields padded with NUL are cleaned.
func javaTrim(value string) string {
	return strings.TrimFunc(value, func(r rune) bool { return r <= ' ' })
}

// gbkText decodes the GBK text that Chinese devices use for parameter values.
func gbkText(data []byte) string {
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(data)
	if err != nil {
		return string(data)
	}
	return string(decoded)
}

// decodeDriverIdentity parses the driver card read reported by the terminal.
func (d *Decoder) decodeDriverIdentity(session *codec.DeviceSession, buf *helper.Buffer) *model.Position {
	limit := bodyLimit(buf)
	position := d.newPosition(session)
	d.copyLastLocation(position)

	position.Set("cardStatus", buf.ReadUnsignedByte())
	readDate(buf, d.location()) // card read time, the position keeps its own
	position.Set("cardResult", buf.ReadUnsignedByte())
	position.Set("driver", javaTrim(string(readField(buf, buf.ReadUnsignedByte(), limit))))
	position.Set("cardCode", javaTrim(string(readField(buf, 20, limit))))
	position.Set("cardAgency", javaTrim(string(readField(buf, buf.ReadUnsignedByte(), limit))))
	position.Set("cardValidity", hex.EncodeToString(readField(buf, 4, limit)))

	return position
}

// decodeParameterQueryResponse parses the terminal parameter dump. Each entry
// is a four byte identifier, a one byte length and the value.
func (d *Decoder) decodeParameterQueryResponse(session *codec.DeviceSession, buf *helper.Buffer) *model.Position {
	limit := bodyLimit(buf)
	position := d.newPosition(session)
	d.copyLastLocation(position)

	buf.ReadUnsignedShort() // response serial number
	buf.ReadUnsignedByte()  // parameter count, the entry lengths bound the loop

	for limit-buf.ReaderIndex() >= 5 {
		subtype := buf.ReadInt()
		length := buf.ReadUnsignedByte()
		end := buf.ReaderIndex() + length
		if end > limit {
			break
		}
		switch subtype {
		case 0x0001:
			position.Set("heartbeatInterval", buf.ReadUnsignedInt())
		case 0x0010:
			position.Set("apn", gbkText(readField(buf, length, limit)))
		case 0x0013:
			position.Set("server", gbkText(readField(buf, length, limit)))
		case 0x0018:
			position.Set("port", buf.ReadUnsignedInt())
		case 0x0027:
			position.Set("sleepReportInterval", buf.ReadUnsignedInt())
		case 0x0028:
			position.Set("emergencyReportInterval", buf.ReadUnsignedInt())
		case 0x0029:
			position.Set("defaultReportInterval", buf.ReadUnsignedInt())
		case 0x0055:
			position.Set("overspeedThreshold", buf.ReadUnsignedInt())
		case 0x0056:
			position.Set("speedLimitDuration", buf.ReadUnsignedInt())
		case 0x0080:
			position.Set(model.KeyOdometer, buf.ReadUnsignedInt()*100)
		case 0x0083:
			position.Set("plateNumber", javaTrim(gbkText(readField(buf, length, limit))))
		}
		buf.SetReaderIndex(end)
	}

	return position
}

// decodeTransparent parses the pass-through payloads of a separate device
// stack that the terminal relays unchanged.
func (d *Decoder) decodeTransparent(session *codec.DeviceSession, buf *helper.Buffer) *model.Position {
	limit := bodyLimit(buf)

	switch buf.ReadUnsignedByte() {
	case 0x40:
		position := d.newPosition(session)
		d.copyLastLocation(position)
		data := javaTrim(string(readField(buf, limit-buf.ReaderIndex(), limit)))
		if strings.HasPrefix(data, "GTSL") {
			if values := strings.Split(data, "|"); len(values) > 4 {
				position.Set(model.KeyDriverID, values[4])
			}
		} else {
			position.Set("data", data)
		}
		if len(position.Attributes) == 0 {
			return nil
		}
		return position

	case 0x41:
		position := d.newPosition(session)
		d.copyLastLocation(position)
		d.decodeObdRt(position, javaTrim(string(readField(buf, limit-buf.ReaderIndex(), limit))))
		return position

	case 0xF0:
		return d.decodeTransparentArchive(session, buf, limit)

	default:
		position := d.newPosition(session)
		d.copyLastLocation(position)
		return position
	}
}

// decodeTransparentArchive parses the 0xF0 archive records: diagnostic trouble
// codes and alarms, both followed by the coordinates of the event.
func (d *Decoder) decodeTransparentArchive(
	session *codec.DeviceSession, buf *helper.Buffer, limit int) *model.Position {
	position := d.newPosition(session)
	d.copyLastLocation(position)

	// A JC terminal relays its own result string in this record instead.
	if session != nil && strings.HasPrefix(strings.ToUpper(session.Model), "JC") {
		position.Set(model.KeyResult, string(readField(buf, limit-buf.ReaderIndex(), limit)))
		return position
	}

	deviceTime := readDate(buf, d.location())
	if buf.ReadUnsignedByte() > 0 {
		position.Set(model.KeyArchive, true)
	}
	buf.ReadUnsignedByte() // vehicle type

	switch buf.ReadUnsignedByte() {
	case 0x02:
		var codes []string
		count := buf.ReadUnsignedShort()
		for i := 0; i < count && limit-buf.ReaderIndex() >= 6; i++ {
			buf.ReadUnsignedInt() // system id
			codeCount := buf.ReadUnsignedShort()
			for j := 0; j < codeCount && limit-buf.ReaderIndex() >= 10; j++ {
				buf.ReadUnsignedInt() // dtc
				buf.ReadUnsignedInt() // status
				length := buf.ReadUnsignedShort()
				codes = append(codes, javaTrim(string(readField(buf, length, limit))))
			}
		}
		position.Set(model.KeyDTCs, strings.Join(codes, " "))

	case 0x03:
		count := buf.ReadUnsignedByte()
		for i := 0; i < count && limit-buf.ReaderIndex() >= 2; i++ {
			id := buf.ReadUnsignedByte()
			length := buf.ReadUnsignedByte()
			if alarm, ok := transparentAlarms[id]; ok {
				position.AddAlarm(alarm)
			}
			if end := buf.ReaderIndex() + length; end < limit {
				buf.SetReaderIndex(end)
			} else {
				buf.SetReaderIndex(limit)
			}
		}

	default:
		return nil
	}

	if limit-buf.ReaderIndex() >= 12 {
		d.decodeCoordinates(position, buf)
	}
	position.Time = deviceTime

	return position
}

// decodeCoordinates reads the status word, latitude and longitude block shared
// by the transparent subtypes. The hemisphere bits live in the status word.
func (d *Decoder) decodeCoordinates(position *model.Position, buf *helper.Buffer) {
	status := buf.ReadUnsignedInt()
	position.Set(model.KeyIgnition, bitSet(status, 0))
	position.Set(model.KeyBlocked, bitSet(status, 10))
	position.Set(model.KeyCharge, bitSet(status, 26))
	position.Valid = bitSet(status, 1)

	latitude := float64(buf.ReadUnsignedInt()) / 1000000.0
	longitude := float64(buf.ReadUnsignedInt()) / 1000000.0
	if bitSet(status, 2) {
		latitude = -latitude
	}
	if bitSet(status, 3) {
		longitude = -longitude
	}
	position.Latitude = latitude
	position.Longitude = longitude
}

// decodeObdRt parses the CSV report of an OBD-II adapter. The first field is
// the sentence header and empty fields are values the adapter did not read.
func (d *Decoder) decodeObdRt(position *model.Position, data string) {
	values := strings.Split(data, ",")
	field := func(index int) string {
		if index >= len(values) {
			return ""
		}
		return strings.TrimSpace(values[index])
	}

	setFloat := func(key string, index int) {
		if value := field(index); value != "" {
			if parsed, err := strconv.ParseFloat(value, 64); err == nil {
				position.Set(key, parsed)
			}
		}
	}
	setInt := func(key string, index int) {
		if value := field(index); value != "" {
			if parsed, err := strconv.Atoi(value); err == nil {
				position.Set(key, parsed)
			}
		}
	}

	setFloat(model.KeyPower, 1)
	setFloat(model.KeyRPM, 2)
	setFloat(model.KeyOBDSpeed, 3)
	setFloat(model.KeyThrottle, 4)
	setFloat(model.KeyEngineLd, 5)
	setInt(model.KeyCoolant, 6)
	setFloat(model.KeyFuelConsumption, 7) // instant
	setFloat(model.KeyFuelConsumption, 8) // average
	setFloat(model.KeyOdometerTrip, 9)
	setFloat(model.KeyOBDOdo, 10)
	setFloat("tripFuelUsed", 11)
	setFloat(model.KeyFuelUsed, 12)
}

// transparentAlarms maps the alarm identifiers of the 0xF0 records.
var transparentAlarms = map[int]string{
	0x01: model.AlarmPowerRestored,
	0x02: model.AlarmPowerCut,
	0x0E: model.AlarmIdle,
	0x1A: model.AlarmAcceleration,
	0x1B: model.AlarmBraking,
	0x1C: "cornering", // model has no constant for the plain cornering alarm
	0x1D: model.AlarmLaneChange,
	0x1E: model.AlarmLaneChange,
	0x1F: model.AlarmLaneChange,
	0x23: model.AlarmFatigueDriving,
	0x25: model.AlarmOverspeed,
	0x26: model.AlarmAccident,
	0x27: model.AlarmAccident,
	0x28: model.AlarmAccident,
	0x31: model.AlarmDoor,
	0x32: model.AlarmDoor,
}

func (d *Decoder) resolve(uniqueID string) *codec.DeviceSession {
	if d.Resolver == nil {
		return nil
	}
	if resolved, err := d.Resolver.Resolve(uniqueID); err == nil && resolved != nil {
		return resolved
	}
	stripped := strings.TrimLeft(uniqueID, "0")
	if stripped != "" && stripped != uniqueID {
		if resolved, err := d.Resolver.Resolve(stripped); err == nil && resolved != nil {
			return resolved
		}
	}
	return nil
}

func (d *Decoder) newPosition(session *codec.DeviceSession) *model.Position {
	position := model.NewPosition(ProtocolName)
	if session != nil {
		position.DeviceID = session.DeviceID
	}
	return position
}

func (d *Decoder) copyLastLocation(position *model.Position) {
	if d.LastLocation == nil {
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

func (d *Decoder) generalResponse(h *header) []byte {
	payload := []byte{
		byte(h.index >> 8), byte(h.index),
		byte(h.messageType >> 8), byte(h.messageType),
		msgResultSuccess,
	}
	return buildMessage(h, msgGeneralResponse, payload)
}

// decodeLocation parses the 0x0200 body of a whole frame. The frame buffer
// still carries the checksum and closing delimiter, so the extension walk stops
// two bytes before the end.
func (d *Decoder) decodeLocation(session *codec.DeviceSession, buf *helper.Buffer) *model.Position {
	return d.decodeLocationData(session, buf, len(buf.Bytes())-2)
}

// decodeLocationFragment parses a location body sliced out of a batch. The
// fragment has no checksum or delimiter, so it is walked to its own end.
func (d *Decoder) decodeLocationFragment(session *codec.DeviceSession, fragment []byte) *model.Position {
	return d.decodeLocationData(session, helper.NewBuffer(fragment), len(fragment))
}

// decodeLocationData parses alarm, status, coordinates, motion and time,
// followed by the variable length extension block. Extensions are bounded by
// extensionLimit, which excludes the frame trailer when there is one.
func (d *Decoder) decodeLocationData(
	session *codec.DeviceSession, buf *helper.Buffer, extensionLimit int) *model.Position {
	position := d.newPosition(session)

	d.decodeAlarm(position, buf.ReadUnsignedInt(), session.Model)

	status := uint32(buf.ReadInt())
	position.Set(model.KeyIgnition, bitSet(status, 0))
	position.Set(model.KeyBlocked, bitSet(status, 10))
	position.Set(model.KeyCharge, bitSet(status, 26))
	position.Valid = bitSet(status, 1)

	latitude := float64(buf.ReadUnsignedInt()) / 1000000.0
	longitude := float64(buf.ReadUnsignedInt()) / 1000000.0
	if bitSet(status, 2) {
		latitude = -latitude
	}
	if bitSet(status, 3) {
		longitude = -longitude
	}
	position.Latitude = latitude
	position.Longitude = longitude

	position.Altitude = float64(buf.ReadShort())
	position.Speed = helper.KnotsFromKph(float64(buf.ReadUnsignedShort()) / 10)
	position.Course = float64(buf.ReadUnsignedShort())
	position.Time = readDate(buf, d.location())

	// A 20 byte tail is the short form used by some trackers.
	if buf.ReadableBytes() == 20 {
		buf.SkipBytes(4)
		position.Set(model.KeyOdometer, buf.ReadUnsignedInt()*1000)
		position.Set(model.KeyBattery, float64(buf.ReadUnsignedShort())/10)
		buf.ReadUnsignedInt() // area id
		position.Set(model.KeyRSSI, buf.ReadUnsignedByte())
		buf.SkipBytes(3)
		return position
	}

	d.decodeExtensions(position, buf, extensionLimit)
	return position
}

// decodeLocationBatch parses the batch location bodies. 0x0704 prefixes the
// items with the item count and the report type, while 0x0210 has no such
// header; both then carry each location as a length prefixed 0x0200 body. A
// non-zero 0x0704 report type marks a blind area upload, which is stored as an
// archived position.
func (d *Decoder) decodeLocationBatch(
	session *codec.DeviceSession, buf *helper.Buffer, messageType int) []*model.Position {
	archive := false
	if messageType == msgLocationBatch {
		buf.ReadUnsignedShort() // item count, the item lengths bound the loop
		archive = buf.ReadUnsignedByte() > 0
	}

	var positions []*model.Position
	for buf.ReadableBytes() > 2 {
		var length int
		if messageType == msgLocationBatch2 {
			length = buf.ReadUnsignedByte()
		} else {
			length = buf.ReadUnsignedShort()
		}
		// Keep the checksum and closing delimiter out of the item: a length
		// that does not fit in what is left is a corrupt batch.
		if length <= 0 || length > buf.ReadableBytes()-2 {
			break
		}
		position := d.decodeLocationFragment(session, buf.ReadSlice(length))
		if archive {
			position.Set(model.KeyArchive, true)
		}
		positions = append(positions, position)
	}
	return positions
}

// decodeExtensions walks the TLVs that follow the location body. Every entry
// is a one byte id, a one byte length and the value.
func (d *Decoder) decodeExtensions(position *model.Position, buf *helper.Buffer, limit int) {
	for buf.ReaderIndex() < limit && buf.ReadableBytes() > 2 {
		subtype := buf.ReadUnsignedByte()
		length := buf.ReadUnsignedByte()
		end := buf.ReaderIndex() + length
		if end > limit {
			break
		}

		switch subtype {
		case 0x01:
			position.Set(model.KeyOdometer, buf.ReadUnsignedInt()*100)
		case 0x02:
			fuel := buf.ReadUnsignedShort()
			if fuel&0x8000 != 0 {
				position.Set(model.KeyFuel, fuel&0x7fff)
			} else {
				position.Set(model.KeyFuel, float64(fuel)/10)
			}
		case 0x06:
			position.Set(model.KeyBatteryLevel, buf.ReadUnsignedByte())
		case 0x25:
			position.Set(model.KeyInput, buf.ReadUnsignedInt())
		case 0x30:
			position.Set(model.KeyRSSI, buf.ReadUnsignedByte())
		case 0x31:
			position.Set(model.KeySatellites, buf.ReadUnsignedByte())
		case 0x56:
			position.Set(model.KeyBatteryLevel, buf.ReadUnsignedByte()*10)
			buf.ReadUnsignedByte() // reserved
		case 0x69:
			position.Set(model.KeyBattery, float64(buf.ReadUnsignedShort())/100)
		case 0x64, 0x65:
			buf.ReadUnsignedInt()  // alarm serial number
			buf.ReadUnsignedByte() // alarm status
			alarmKey := "adasAlarm"
			if subtype == 0x65 {
				alarmKey = "dmsAlarm"
			}
			position.Set(alarmKey, buf.ReadUnsignedByte())
			if length >= 47 {
				buf.SetReaderIndex(end - 16)
				position.Set("alarmLabel", hex.EncodeToString(buf.ReadSlice(16)))
			}
		case 0x77:
			for end-buf.ReaderIndex() >= 8 {
				tire := buf.ReadUnsignedByte()
				position.Set(fmt.Sprintf("tire%dSensorId", tire), hex.EncodeToString(buf.ReadSlice(3)))
				position.Set(fmt.Sprintf("tire%dPressure", tire), float64(buf.ReadUnsignedShort()&0x3ff)/40.0)
				position.Set(fmt.Sprintf("tire%dTemp", tire), buf.ReadUnsignedByte()-50)
				position.Set(fmt.Sprintf("tire%dStatus", tire), buf.ReadUnsignedByte())
			}
		case 0x91:
			if length >= 22 {
				position.Set(model.KeyBattery, float64(buf.ReadUnsignedShort())/10.0)
				position.Set(model.KeyRPM, buf.ReadUnsignedShort())
				position.Set(model.KeyOBDSpeed, buf.ReadUnsignedByte())
				position.Set(model.KeyThrottle, buf.ReadUnsignedByte()*100/255)
				position.Set(model.KeyEngineLd, buf.ReadUnsignedByte()*100/255)
				position.Set(model.KeyCoolant, buf.ReadUnsignedByte()-40)
				buf.SkipBytes(2) // fuel pressure
				position.Set(model.KeyFuelConsumption, float64(buf.ReadUnsignedShort())/100.0)
				buf.SkipBytes(2) // intake pressure
				buf.SkipBytes(4) // engine run time
				buf.SkipBytes(2) // rail pressure
				position.Set(model.KeyFuelUsed, float64(buf.ReadUnsignedShort())/100.0)
			}
		case 0x94:
			position.Set(model.KeyVIN, strings.TrimSpace(string(buf.ReadSlice(length))))
		case 0xE6:
			if length >= 7 && string(buf.Bytes()[buf.ReaderIndex():buf.ReaderIndex()+7]) == "$OBD-RT" {
				d.decodeObdRt(position, string(buf.ReadSlice(length)))
			}
		case 0xAC:
			position.Set(model.KeyOdometer, buf.ReadUnsignedInt())
		case 0xBC:
			position.Set("driver", strings.TrimSpace(string(buf.ReadSlice(length))))
		case 0xBD:
			position.Set(model.KeyDriverID, string(buf.ReadSlice(length)))
		default:
			buf.SkipBytes(length)
		}

		buf.SetReaderIndex(end)
	}
}

// padlockModels and aovxModels branch the alarm bits, which mean something
// else on those devices. Java compares the raw device model, and upper casing
// both sides keeps a lower case row working the same way.
var (
	padlockModels = map[string]bool{"G-360P": true, "G-508P": true}
	aovxModels    = map[string]bool{"AL300": true, "GL100": true}
)

func (d *Decoder) decodeAlarm(position *model.Position, value uint32, deviceModel string) {
	upper := strings.ToUpper(deviceModel)
	switch {
	case padlockModels[upper]:
		if bitSet(value, 0) || bitSet(value, 4) {
			position.AddAlarm(model.AlarmRemoving)
		}
		if bitSet(value, 1) {
			position.AddAlarm(model.AlarmTampering)
		}
		return
	case aovxModels[upper]:
		if bitSet(value, 16) {
			position.AddAlarm(model.AlarmMovement)
		}
		return
	}

	if bitSet(value, 0) {
		position.AddAlarm(model.AlarmSOS)
	}
	if bitSet(value, 1) {
		position.AddAlarm(model.AlarmOverspeed)
	}
	if bitSet(value, 2) {
		position.AddAlarm(model.AlarmFatigueDriving)
	}
	if bitSet(value, 5) || bitSet(value, 6) {
		position.AddAlarm(model.AlarmGPSAntennaCut)
	}
	if bitSet(value, 4) || bitSet(value, 9) || bitSet(value, 10) || bitSet(value, 11) {
		position.AddAlarm(model.AlarmFault)
	}
	if bitSet(value, 7) || bitSet(value, 18) {
		position.AddAlarm(model.AlarmLowBattery)
	}
	if bitSet(value, 8) {
		position.AddAlarm(model.AlarmPowerOff)
	}
	if bitSet(value, 15) {
		position.AddAlarm(model.AlarmVibration)
	}
	if bitSet(value, 16) || bitSet(value, 17) {
		position.AddAlarm(model.AlarmTampering)
	}
	if bitSet(value, 19) {
		position.AddAlarm(model.AlarmParking)
	}
	if bitSet(value, 20) {
		position.AddAlarm(model.AlarmGeofence)
	}
	if bitSet(value, 26) {
		position.AddAlarm(model.AlarmGeneral)
	}
	if bitSet(value, 28) {
		position.AddAlarm(model.AlarmMovement)
	}
	if bitSet(value, 29) || bitSet(value, 30) {
		if upper != "VL300" {
			position.AddAlarm(model.AlarmAccident)
		}
	}
}

func (d *Decoder) location() *time.Location {
	if d.Location != nil {
		return d.Location
	}
	return time.FixedZone("GMT+8", 8*60*60)
}

var (
	_ codec.Decoder        = (*Decoder)(nil)
	_ codec.SessionDecoder = (*Decoder)(nil)
)
