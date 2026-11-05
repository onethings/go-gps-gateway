package teltonika

import (
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and on positions.
const ProtocolName = "teltonika"

// Codec identifiers, matching the CODEC_* constants in the Java decoder.
const (
	codecGH3000 = 0x07
	codec8      = 0x08
	codec8Ext   = 0x8E
	codec12     = 0x0C
	codec13     = 0x0D
	codec16     = 0x10
)

// Decoder decodes Teltonika frames into positions.
type Decoder struct {
	codec.Base

	// Extended enables the optional 16 byte IO block, matching the
	// "protocol.extended" configuration in Traccar.
	Extended bool
}

// NewDecoder builds a Teltonika decoder.
func NewDecoder(base codec.Base) *Decoder {
	return &Decoder{Base: base}
}

// Name implements codec.Decoder.
func (d *Decoder) Name() string { return ProtocolName }

// Decode implements codec.Decoder. Only the IMEI handshake is meaningful
// without a session; data packets need DecodeWithSession.
func (d *Decoder) Decode(frame []byte) (*codec.Result, error) {
	return d.decode(frame, nil)
}

// DecodeWithSession implements codec.SessionDecoder.
func (d *Decoder) DecodeWithSession(frame []byte, session *codec.DeviceSession) (*codec.Result, error) {
	return d.decode(frame, session)
}

func (d *Decoder) decode(frame []byte, session *codec.DeviceSession) (*codec.Result, error) {
	result := codec.NewResult()

	// Heartbeat: a single 0xFF byte, nothing to answer.
	if len(frame) == 1 && frame[0] == 0xff {
		result.Consumed = false
		return result, nil
	}
	if len(frame) < 2 {
		return &codec.Result{}, nil
	}

	// IMEI handshake. The device expects 0x01 when the device is known and
	// 0x00 otherwise, and closes the connection after 0x00.
	if length := int(binary.BigEndian.Uint16(frame)); length > 0 {
		if len(frame) < length+2 {
			return &codec.Result{}, nil
		}
		result.UniqueID = string(frame[2 : 2+length])
		known := d.Resolver == nil
		if d.Resolver != nil {
			resolved, err := d.Resolver.Resolve(result.UniqueID)
			if err != nil {
				return nil, err
			}
			known = resolved != nil
		}
		if known {
			result.AddResponse([]byte{0x01})
		} else {
			result.AddResponse([]byte{0x00})
		}
		return result, nil
	}

	if session == nil {
		return &codec.Result{}, nil
	}

	buf := helper.NewBuffer(frame)
	buf.SkipBytes(4) // reserved
	buf.SkipBytes(4) // data length
	codecID := buf.ReadUnsignedByte()
	count := buf.ReadUnsignedByte()

	for i := 0; i < count; i++ {
		position := model.NewPosition(ProtocolName)
		position.DeviceID = session.DeviceID
		position.Valid = true

		switch codecID {
		case codec13:
			d.decodeCodec13(position, buf)
		case codec12:
			d.decodeCodec12(position, buf)
		default:
			d.decodeLocation(position, buf, codecID)
		}

		if !position.Outdated || len(position.Attributes) > 0 {
			result.AddPosition(position)
		}
	}

	// Codec 12/13 exchanges are acknowledged by the device, not by us.
	if codecID != codec12 && codecID != codec13 {
		response := make([]byte, 4)
		binary.BigEndian.PutUint32(response, uint32(count))
		result.AddResponse(response)
	}

	return result, nil
}

// decodeLocation parses one AVL record of Codec 8, 8E or 16.
func (d *Decoder) decodeLocation(position *model.Position, buf *helper.Buffer, codecID int) {
	position.Time = time.UnixMilli(buf.ReadLong())
	position.Set(model.KeyPriority, buf.ReadUnsignedByte())
	position.Longitude = float64(buf.ReadInt()) / 10000000.0
	position.Latitude = float64(buf.ReadInt()) / 10000000.0
	position.Altitude = float64(buf.ReadShort())
	position.Course = float64(buf.ReadUnsignedShort())

	satellites := buf.ReadUnsignedByte()
	position.Set(model.KeySatellites, satellites)
	position.Valid = satellites != 0

	position.Speed = helper.KnotsFromKph(float64(buf.ReadUnsignedShort()))

	position.Set(model.KeyEvent, readExtByte(buf, codecID, codec8Ext, codec16))
	if codecID == codec16 {
		buf.ReadUnsignedByte() // generation type
	}
	readExtByte(buf, codecID, codec8Ext) // total IO data records

	d.decodeIOBlock(position, buf, codecID, 1)
	d.decodeIOBlock(position, buf, codecID, 2)
	d.decodeIOBlock(position, buf, codecID, 4)

	if codecID == codec8 || codecID == codec8Ext || codecID == codec16 {
		d.decodeIOBlock(position, buf, codecID, 8)
	}

	if d.Extended {
		count := readExtByte(buf, codecID, codec8Ext)
		for i := 0; i < count; i++ {
			id := readExtByte(buf, codecID, codec8Ext, codec16)
			position.SetIndexed(model.PrefixIO, id, hex.EncodeToString(buf.ReadSlice(16)))
		}
	}

	if codecID == codec8Ext {
		d.decodeExtended(position, buf)
	}

	d.decodeNetwork(position)
}

// decodeIOBlock reads one length group of IO elements.
func (d *Decoder) decodeIOBlock(position *model.Position, buf *helper.Buffer, codecID, length int) {
	count := readExtByte(buf, codecID, codec8Ext)
	for i := 0; i < count; i++ {
		id := readExtByte(buf, codecID, codec8Ext, codec16)
		d.decodeParameter(position, id, buf, length)
	}
}

// decodeExtended reads the Codec 8 Extended variable length elements.
func (d *Decoder) decodeExtended(position *model.Position, buf *helper.Buffer) {
	count := buf.ReadUnsignedShort()
	for i := 0; i < count; i++ {
		id := buf.ReadUnsignedShort()
		length := buf.ReadUnsignedShort()
		switch id {
		case 256, 325: // VIN
			position.Set(model.KeyVIN, strings.TrimSpace(string(buf.ReadSlice(length))))
		case 281: // DTCs
			position.Set(model.KeyDTCs, strings.ReplaceAll(string(buf.ReadSlice(length)), ",", " "))
		case 385: // BLE beacons
			d.decodeBeacons(position, buf.ReadSlice(length))
		case 548, 10828, 10829, 10831, 11317: // BLE tags
			d.decodeTags(position, buf.ReadSlice(length))
		default:
			position.SetIndexed(model.PrefixIO, id, hex.EncodeToString(buf.ReadSlice(length)))
		}
	}
}

// decodeBeacons reads element 385. Each entry starts with a flags byte: a
// non-zero high nibble selects the iBeacon layout (UUID, major, minor), zero
// selects the Eddystone layout (namespace, instance). The two low bits gate the
// battery and temperature fields that follow the RSSI.
func (d *Decoder) decodeBeacons(position *model.Position, data []byte) {
	buf := helper.NewBuffer(data)
	buf.ReadUnsignedByte() // data part

	for index := 1; buf.IsReadable(); index++ {
		flags := buf.ReadUnsignedByte()
		if flags>>4 > 0 {
			setBeacon(position, "beacon", index, "Uuid", hex.EncodeToString(buf.ReadSlice(16)))
			setBeacon(position, "beacon", index, "Major", buf.ReadUnsignedShort())
			setBeacon(position, "beacon", index, "Minor", buf.ReadUnsignedShort())
		} else {
			setBeacon(position, "beacon", index, "Namespace", hex.EncodeToString(buf.ReadSlice(10)))
			setBeacon(position, "beacon", index, "Instance", hex.EncodeToString(buf.ReadSlice(6)))
		}
		setBeacon(position, "beacon", index, "Rssi", int(buf.ReadSignedByte()))
		if flags&0x02 != 0 {
			setBeacon(position, "beacon", index, "Battery", float64(buf.ReadUnsignedShort())/100)
		}
		if flags&0x04 != 0 {
			setBeacon(position, "beacon", index, "Temp", buf.ReadUnsignedShort())
		}
	}
}

// decodeTags reads the length prefixed BLE tag list used by elements 548,
// 10828, 10829, 10831 and 11317. Every tag is its own TLV of parameter ids.
func (d *Decoder) decodeTags(position *model.Position, data []byte) {
	buf := helper.NewBuffer(data)
	buf.ReadUnsignedByte() // header

	for index := 1; buf.IsReadable(); index++ {
		length := buf.ReadUnsignedByte()
		d.decodeTag(position, buf.ReadSlice(length), index)
	}
}

func (d *Decoder) decodeTag(position *model.Position, data []byte, index int) {
	buf := helper.NewBuffer(data)
	for buf.IsReadable() {
		id := buf.ReadUnsignedByte()
		if !buf.IsReadable() {
			return
		}
		length := buf.ReadUnsignedByte()

		switch id {
		case 0:
			setBeacon(position, "tag", index, "Rssi", int(buf.ReadSignedByte()))
		case 1:
			setBeacon(position, "tag", index, "Id", hex.EncodeToString(buf.ReadSlice(length)))
		case 2:
			flags := buf.ReadUnsignedByte()
			if flags&0x40 != 0 {
				setBeacon(position, "tag", index, "LowBattery", true)
			}
			if flags&0x80 != 0 {
				setBeacon(position, "tag", index, "Voltage", buf.ReadUnsignedByte()*10+2000)
			}
		case 5:
			setBeacon(position, "tag", index, "Name", string(buf.ReadSlice(length)))
		case 6:
			setBeacon(position, "tag", index, "Temp", int(buf.ReadShort()))
		case 7:
			setBeacon(position, "tag", index, "Humidity", buf.ReadUnsignedByte())
		case 8:
			setBeacon(position, "tag", index, "Magnet", buf.ReadUnsignedByte() > 0)
		case 9:
			setBeacon(position, "tag", index, "Motion", buf.ReadUnsignedByte() > 0)
		case 10:
			setBeacon(position, "tag", index, "MotionCount", buf.ReadUnsignedShort())
		case 11:
			setBeacon(position, "tag", index, "Pitch", int(buf.ReadSignedByte()))
		case 12:
			setBeacon(position, "tag", index, "AngleRoll", int(buf.ReadShort()))
		case 13:
			setBeacon(position, "tag", index, "LowBattery", buf.ReadUnsignedByte())
		case 14:
			setBeacon(position, "tag", index, "Battery", buf.ReadUnsignedShort())
		case 15:
			setBeacon(position, "tag", index, "Mac", hex.EncodeToString(buf.ReadSlice(6)))
		default:
			buf.SkipBytes(length)
		}
	}
}

// setBeacon names an attribute the way the reference does, with the index
// between the prefix and the field: beacon1Uuid, tag2Battery.
func setBeacon(position *model.Position, prefix string, index int, field string, value any) {
	position.Set(prefix+strconv.Itoa(index)+field, value)
}

// decodeParameter maps one IO element to an attribute. The reader is always
// moved past exactly length bytes, so the cursor stays correct even when a
// handler reads less than the element size.
func (d *Decoder) decodeParameter(position *model.Position, id int, buf *helper.Buffer, length int) {
	start := buf.ReaderIndex()

	switch id {
	case 1:
		position.SetIndexed(model.PrefixIn, 1, buf.ReadUnsignedByte() > 0)
	case 2:
		position.SetIndexed(model.PrefixIn, 2, buf.ReadUnsignedByte() > 0)
	case 3:
		position.SetIndexed(model.PrefixIn, 3, buf.ReadUnsignedByte() > 0)
	case 4:
		position.SetIndexed(model.PrefixIn, 4, buf.ReadUnsignedByte() > 0)
	case 16:
		position.Set(model.KeyOdometer, buf.ReadUnsignedInt())
	case 17:
		position.Set("axisX", buf.ReadShort())
	case 18:
		position.Set("axisY", buf.ReadShort())
	case 19:
		position.Set("axisZ", buf.ReadShort())
	case 21:
		position.Set(model.KeyRSSI, buf.ReadUnsignedByte())
	case 66:
		position.Set(model.KeyPower, float64(buf.ReadUnsignedShort())/1000)
	case 67:
		position.Set(model.KeyBattery, float64(buf.ReadUnsignedShort())/1000)
	case 179:
		position.SetIndexed(model.PrefixOut, 1, buf.ReadUnsignedByte() > 0)
	case 180:
		position.SetIndexed(model.PrefixOut, 2, buf.ReadUnsignedByte() > 0)
	case 181:
		position.Set(model.KeyPDOP, float64(buf.ReadUnsignedShort())/10)
	case 182:
		position.Set(model.KeyHDOP, float64(buf.ReadUnsignedShort())/10)
	case 205:
		position.Set("cid2g", buf.ReadUnsignedShort())
	case 206:
		position.Set("lac", buf.ReadUnsignedShort())
	case 236:
		if buf.ReadUnsignedByte() > 0 {
			position.AddAlarm(model.AlarmGeneral)
		}
	case 239:
		position.Set(model.KeyIgnition, buf.ReadUnsignedByte() > 0)
	case 240:
		position.Set(model.KeyMotion, buf.ReadUnsignedByte() > 0)
	case 241:
		position.Set(model.KeyOperator, buf.ReadUnsignedInt())
	case 253:
		switch buf.ReadUnsignedByte() {
		case 1:
			position.AddAlarm(model.AlarmHardAcceleration)
		case 2:
			position.AddAlarm(model.AlarmHardBraking)
		case 3:
			position.AddAlarm(model.AlarmHardCornering)
		}
	case 636:
		position.Set("cid4g", buf.ReadUnsignedInt())
	default:
		position.SetIndexed(model.PrefixIO, id, readValue(buf, length))
	}

	buf.SetReaderIndex(start + length)
}

// decodeCodec12 reads a serial (command response / text) packet.
func (d *Decoder) decodeCodec12(position *model.Position, buf *helper.Buffer) {
	messageType := buf.ReadUnsignedByte()
	position.Set(model.KeyType, messageType)

	length := int(buf.ReadInt())
	if length < 0 || buf.ReadableBytes() < length {
		return
	}
	data := buf.ReadSlice(length)

	if printable(data) {
		position.Set(model.KeyResult, strings.TrimSpace(string(data)))
	} else {
		position.Set(model.KeyResult, hex.EncodeToString(data))
	}
}

// decodeCodec13 reads an information packet (driver id / text result).
func (d *Decoder) decodeCodec13(position *model.Position, buf *helper.Buffer) {
	position.Set(model.KeyType, buf.ReadUnsignedByte())

	length := int(buf.ReadInt()) - 4
	position.Time = time.UnixMilli(int64(buf.ReadUnsignedInt()) * 1000)
	if length < 0 || buf.ReadableBytes() < length {
		return
	}
	data := buf.ReadSlice(length)

	if !printable(data) {
		position.Set(model.KeyResult, hex.EncodeToString(data))
		return
	}
	text := strings.TrimSpace(string(data))
	if strings.HasPrefix(text, "GTSL") {
		if fields := strings.Split(text, "|"); len(fields) > 4 {
			position.Set(model.KeyDriverID, fields[4])
		}
	} else {
		position.Set(model.KeyResult, text)
	}
}

// decodeNetwork turns the io205/io206/io636 attributes into a cell tower.
func (d *Decoder) decodeNetwork(position *model.Position) {
	lac, hasLac := takeInt(position, "lac")
	cid2g, has2g := takeInt(position, "cid2g")
	cid4g, has4g := takeInt(position, "cid4g")

	if !hasLac || (!has2g && !has4g) {
		return
	}

	cid := cid2g
	if !has2g {
		cid = cid4g
	}
	network := model.NewNetworkWith(model.NewCellTower(0, 0, lac, int64(cid)))
	if !has2g {
		network.RadioType = "lte"
	}
	position.SetNetwork(network)
}

// readExtByte returns a 16 bit value for the codecs whose IO element ids are
// two bytes wide, and a single byte otherwise.
func readExtByte(buf *helper.Buffer, codecID int, wide ...int) int {
	for _, candidate := range wide {
		if codecID == candidate {
			return buf.ReadUnsignedShort()
		}
	}
	return buf.ReadUnsignedByte()
}

func readValue(buf *helper.Buffer, length int) int64 {
	switch length {
	case 1:
		return int64(buf.ReadUnsignedByte())
	case 2:
		return int64(buf.ReadUnsignedShort())
	case 4:
		return int64(buf.ReadUnsignedInt())
	default:
		return buf.ReadLong()
	}
}

func takeInt(position *model.Position, key string) (int, bool) {
	value, ok := position.Get(key)
	if !ok {
		return 0, false
	}
	delete(position.Attributes, key)

	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case uint32:
		return int(v), true
	}
	return 0, false
}

func printable(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	for _, b := range data {
		if b < 0x20 && b != '\r' && b != '\n' && b != '\t' {
			return false
		}
		if b > 0x7e {
			return false
		}
	}
	return true
}

var (
	_ codec.Decoder        = (*Decoder)(nil)
	_ codec.SessionDecoder = (*Decoder)(nil)
)
