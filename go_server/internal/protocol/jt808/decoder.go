package jt808

import (
	"encoding/hex"
	"strings"
	"time"

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
		case 0x94:
			position.Set(model.KeyVIN, strings.TrimSpace(string(buf.ReadSlice(length))))
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
