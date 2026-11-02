// Package suntech implements the Suntech family of protocols. Frames are text
// messages separated by carriage returns (the STT/ALT/EMG/EVT families) or
// compact binary records. A port of SuntechProtocolDecoder.
package suntech

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and on positions.
const ProtocolName = "suntech"

// dateFormat parses the yyyyMMddHH:mm:ss (UTC) timestamps the text protocols
// carry split across a date and a time field.
const dateFormat = "2006010215:04:05"

// defaultPrefix is the legacy protocol marker used to build commands. The Java
// server reads the live prefix from the connection; the Go gateway creates the
// encoder per command so it falls back to the documented default.
const defaultPrefix = "SA200"

// Decoder decodes Suntech frames into positions.
type Decoder struct {
	codec.Base

	// universal is set when the last frame used the universal text layout.
	// The encoder reads it to pick the command format, mirroring the state the
	// Java decoder keeps and the encoder inspects.
	universal bool
	// prefix is the protocol marker of the last text frame.
	prefix string

	// Optional configuration mirrors the Java decoder cache lookups. Zero
	// values match a device with no extra configuration enabled.
	ProtocolType int
	HBM          int
	IncludeADC   bool
	IncludeRPM   bool
	IncludeTemp  bool
}

// NewDecoder builds a Suntech decoder.
func NewDecoder(base codec.Base) *Decoder { return &Decoder{Base: base} }

// Name implements codec.Decoder.
func (d *Decoder) Name() string { return ProtocolName }

// GetUniversal reports whether the last frame used the universal layout.
func (d *Decoder) GetUniversal() bool { return d.universal }

// GetPrefix returns the protocol marker of the last text frame.
func (d *Decoder) GetPrefix() string { return d.prefix }

// Decode implements codec.Decoder.
func (d *Decoder) Decode(frame []byte) (*codec.Result, error) {
	result := codec.NewResult()
	if len(frame) == 0 {
		return result, nil
	}
	buf := helper.NewBuffer(frame)

	if frame[0] == 0x02 {
		return d.decodeZip(result, buf)
	}

	if frame[1] == 0 {
		d.universal = true
		return d.decodeBinary(result, buf)
	}

	values := strings.Split(string(frame), ";")
	prefix := values[0]
	d.prefix = prefix

	switch {
	case prefix == "CRR":
		// Crash reports are a multi-frame dump the Go decoder does not
		// accumulate; a single frame produces no position.
		return result, nil
	case len(prefix) < 5:
		d.universal = true
		return d.decodeUniversal(result, values)
	case strings.HasSuffix(prefix, "HTE"):
		return d.decodeTravelReport(result, values)
	case strings.HasPrefix(prefix, "ST9"):
		return d.decode9(result, values)
	case strings.HasPrefix(prefix, "ST4"):
		return d.decode4(result, values)
	case strings.HasPrefix(prefix, "S") && len(prefix) >= 5:
		return d.decode2356(result, prefix[:5], values)
	}
	return result, nil
}

// resolve resolves an IMEI and records the unique id on the result.
func (d *Decoder) resolve(result *codec.Result, imei string) (int64, error) {
	result.UniqueID = imei
	session, err := d.Resolver.Resolve(imei)
	if err != nil {
		return 0, err
	}
	return session.DeviceID, nil
}

func newPosition(deviceID int64) *model.Position {
	return &model.Position{Protocol: ProtocolName, DeviceID: deviceID}
}

// decodeEmergency maps an emergency alert code to an alarm.
func decodeEmergency(value int) string {
	switch value {
	case 1:
		return model.AlarmSOS
	case 2:
		return model.AlarmParking
	case 3:
		return model.AlarmPowerCut
	case 5, 6:
		return model.AlarmDoor
	case 7:
		return model.AlarmMovement
	case 8:
		return model.AlarmVibration
	}
	return ""
}

// decodeAlert maps an alert code to an alarm.
func decodeAlert(value int) string {
	switch value {
	case 1:
		return model.AlarmOverspeed
	case 5:
		return model.AlarmGeofenceExit
	case 6:
		return model.AlarmGeofenceEnter
	case 14:
		return model.AlarmLowBattery
	case 15:
		return model.AlarmVibration
	case 16:
		return model.AlarmAccident
	case 40:
		return model.AlarmPowerRestored
	case 41:
		return model.AlarmPowerCut
	case 42:
		return model.AlarmSOS
	case 46:
		return model.AlarmAcceleration
	case 47:
		return model.AlarmBraking
	case 50:
		return model.AlarmJamming
	case 132:
		return model.AlarmDoor
	}
	return ""
}

// parseTime parses the yyyyMMddHH:mm:ss field pair in UTC.
func parseTime(date, clock string) time.Time {
	t, err := time.Parse(dateFormat, date+clock)
	if err != nil {
		return time.Time{}
	}
	return t
}

// decode9 handles the ST9 text family. Only location, emergency and alert
// reports carry a position.
func (d *Decoder) decode9(result *codec.Result, values []string) (*codec.Result, error) {
	index := 1

	typeValue := values[index]
	index++

	if typeValue != "Location" && typeValue != "Emergency" && typeValue != "Alert" {
		return result, nil
	}

	deviceID, err := d.resolve(result, values[index])
	if err != nil {
		return result, err
	}
	index++

	position := newPosition(deviceID)

	if typeValue == "Emergency" || typeValue == "Alert" {
		position.AddAlarm(model.AlarmGeneral)
	}

	if typeValue != "Alert" || d.ProtocolType == 0 {
		position.Set(model.KeyVersionFW, values[index])
		index++
	}

	position.Time = parseTime(values[index], values[index+1])
	index += 2

	if d.ProtocolType == 1 {
		index++ // cell
	}

	position.Latitude, _ = strconv.ParseFloat(values[index], 64)
	index++
	position.Longitude, _ = strconv.ParseFloat(values[index], 64)
	index++
	position.Speed = helper.KnotsFromKph(mustFloat(values[index]))
	index++
	position.Course = mustFloat(values[index])
	index++

	position.Valid = values[index] == "1"
	index++

	if d.ProtocolType == 1 {
		position.Set(model.KeyOdometer, mustInt(values[index]))
		index++
	}

	result.AddPosition(position)
	return result, nil
}

// decode4 handles the ST4 text family.
func (d *Decoder) decode4(result *codec.Result, values []string) (*codec.Result, error) {
	index := 0

	typeValue := values[index][5:]
	index++

	if typeValue != "STT" && typeValue != "ALT" {
		return result, nil
	}

	deviceID, err := d.resolve(result, values[index])
	if err != nil {
		return result, err
	}
	index++

	position := newPosition(deviceID)
	position.Set(model.KeyType, typeValue)

	position.Set(model.KeyVersionFW, values[index])
	index++
	modelValue := mustInt(values[index])
	index++
	if modelValue == 41 {
		index++ // variant
	}

	network := model.NewNetwork()
	for i := 0; i < 7; i++ {
		cid := mustInt(values[index])
		index++
		mcc := mustInt(values[index])
		index++
		mnc := mustInt(values[index])
		index++
		var lac, rssi int
		if i == 0 {
			rssi = mustInt(values[index])
			index++
			lac = mustInt(values[index])
			index++
		} else {
			lac = mustInt(values[index])
			index++
			rssi = mustInt(values[index])
			index++
		}
		index++ // timing advance
		if cid > 0 {
			network.AddCellTower(model.NewCellTowerWithRSSI(mcc, mnc, lac, int64(cid), rssi))
		}
	}
	position.Network = network

	position.Set(model.KeyBattery, mustFloat(values[index]))
	index++
	if values[index] == "0" {
		position.Set(model.KeyArchive, true)
	}
	index++
	position.Set(model.KeyIndex, mustInt(values[index]))
	index++
	position.Set(model.KeyStatus, mustInt(values[index]))
	index++

	if len(values[index]) == 3 {
		index++ // collaborative network
	}

	if modelValue == 41 {
		index++ // collaborative network
		index++ // temperature
		position.Set(model.KeyMotion, mustInt(values[index]) == 2)
		index++
	}

	if values[index] == "" {
		position.Outdated = true
	} else {
		position.Time = parseTime(values[index], values[index+1])
		index += 2

		position.Latitude = mustFloat(values[index])
		index++
		position.Longitude = mustFloat(values[index])
		index++
		position.Speed = helper.KnotsFromKph(mustFloat(values[index]))
		index++
		position.Course = mustFloat(values[index])
		index++

		position.Set(model.KeySatellites, mustInt(values[index]))
		index++

		position.Valid = values[index] == "1"
		index++
	}

	result.AddPosition(position)
	return result, nil
}

// decodeSerialData reads the UEX serial payload and returns the index after
// the trailing checksum field.
func decodeSerialData(position *model.Position, values []string, index int) int {
	remaining := mustInt(values[index])
	index++
	totalFuel := 0.0
	for remaining > 0 {
		attribute := values[index]
		index++
		switch {
		case strings.HasPrefix(attribute, "CabAVL"):
			data := strings.Split(attribute, ",")
			fuel1 := mustFloat(data[2])
			if fuel1 > 0 {
				totalFuel += fuel1
				position.Set("fuel1", fuel1)
			}
			fuel2 := mustFloat(data[3])
			if fuel2 > 0 {
				totalFuel += fuel2
				position.Set("fuel2", fuel2)
			}
		case strings.HasPrefix(attribute, "GTSL"):
			position.Set(model.KeyDriverID, strings.Split(attribute, "|")[4])
		case strings.Contains(attribute, "="):
			pair := strings.SplitN(attribute, "=", 2)
			if len(pair) >= 2 {
				value := strings.TrimSpace(pair[1])
				if strings.Contains(value, ".") {
					value = value[:strings.Index(value, ".")]
				}
				switch pair[0][0] {
				case 't':
					position.SetIndexed(model.PrefixTemp, int(pair[0][2]-'0'), parseHexInt(value))
				case 'N':
					fuel := parseHexInt(value)
					totalFuel += float64(fuel)
					position.Set("fuel"+string(pair[0][2]), fuel)
				case 'Q':
					position.Set("drivingQuality", parseHexInt(value))
				}
			}
		default:
			position.Set("serial", strings.TrimSpace(attribute))
		}
		remaining -= len(attribute) + 1
	}
	if totalFuel > 0 {
		position.Set(model.KeyFuel, totalFuel)
	}
	return index + 1 // checksum
}

func parseHexInt(value string) int {
	n, _ := strconv.ParseInt(value, 16, 64)
	return int(n)
}

// decode2356 handles the ST100/ST200/ST300/ST500/ST600 text family.
func (d *Decoder) decode2356(result *codec.Result, protocol string, values []string) (*codec.Result, error) {
	index := 0

	typeValue := values[index][5:]
	index++

	resultValue := false
	if values[index] == "Res" {
		resultValue = true
		index++
	}

	deviceID, err := d.resolve(result, values[index])
	if err != nil {
		return result, err
	}
	index++

	position := newPosition(deviceID)
	position.Set(model.KeyType, typeValue)

	if resultValue {
		position.Outdated = true
		position.Set(model.KeyResult, strings.Join(values[index:], ";"))
		result.AddPosition(position)
		return result, nil
	}

	if typeValue != "STT" && typeValue != "EMG" && typeValue != "EVT" &&
		typeValue != "ALT" && typeValue != "UEX" {
		return result, nil
	}

	if strings.HasPrefix(protocol, "ST3") || protocol == "ST500" || protocol == "ST600" {
		index++ // model
	}

	position.Set(model.KeyVersionFW, values[index])
	index++

	position.Time = parseTime(values[index], values[index+1])
	index += 2

	if protocol != "ST500" {
		cid, _ := strconv.ParseInt(values[index], 16, 64)
		index++
		if protocol == "ST600" {
			position.Network = model.NewNetworkWith(model.NewCellTowerWithRSSI(
				mustInt(values[index]),
				mustInt(values[index+1]),
				parseHexInt(values[index+2]),
				cid,
				mustInt(values[index+3]),
			))
			index += 4
		}
	}

	position.Latitude = mustFloat(values[index])
	index++
	position.Longitude = mustFloat(values[index])
	index++
	position.Speed = helper.KnotsFromKph(mustFloat(values[index]))
	index++
	position.Course = mustFloat(values[index])
	index++

	position.Set(model.KeySatellites, mustInt(values[index]))
	index++

	position.Valid = values[index] == "1"
	index++

	position.Set(model.KeyOdometer, mustInt(values[index]))
	index++
	position.Set(model.KeyPower, mustFloat(values[index]))
	index++

	io := values[index]
	index++
	if len(io) >= 6 {
		position.Set(model.KeyIgnition, io[0] == '1')
		position.SetIndexed(model.PrefixIn, 1, io[1] == '1')
		position.SetIndexed(model.PrefixIn, 2, io[2] == '1')
		position.SetIndexed(model.PrefixIn, 3, io[3] == '1')
		position.SetIndexed(model.PrefixOut, 1, io[4] == '1')
		position.SetIndexed(model.PrefixOut, 2, io[5] == '1')
	}

	switch typeValue {
	case "STT":
		position.Set(model.KeyStatus, mustInt(values[index]))
		index++
		position.Set(model.KeyIndex, mustInt(values[index]))
		index++
	case "EMG":
		position.AddAlarm(decodeEmergency(mustInt(values[index])))
		index++
	case "EVT":
		position.Set(model.KeyEvent, mustInt(values[index]))
		index++
	case "ALT":
		position.AddAlarm(decodeAlert(mustInt(values[index])))
		index++
	case "UEX":
		index = decodeSerialData(position, values, index)
	}

	if d.HBM >= 1 {
		if index < len(values) {
			position.Set(model.KeyHours, int64(mustInt(values[index]))*60*1000)
			index++
		}
		if index < len(values) {
			position.Set(model.KeyBattery, mustFloat(values[index]))
			index++
		}
		if index < len(values) && values[index] == "0" {
			position.Set(model.KeyArchive, true)
		}
		index++

		if d.IncludeADC {
			for i := 1; i <= 3; i++ {
				if index < len(values) && values[index] != "" {
					position.SetIndexed(model.PrefixADC, i, mustFloat(values[index]))
				}
				index++
			}
		}

		if d.IncludeRPM && index < len(values) {
			position.Set(model.KeyRPM, mustInt(values[index]))
			index++
		}

		required := 7
		if d.HBM == 1 {
			required = 2
		}
		if len(values)-index >= required {
			driverUniqueID := values[index]
			index++
			if driverUniqueID != "" {
				position.Set(model.KeyDriverID, driverUniqueID)
			}
			index++ // registered
		}

		if d.IncludeTemp {
			for i := 1; i <= 3; i++ {
				temperature := values[index]
				index++
				value := temperature[strings.Index(temperature, ":")+1:]
				if value != "" {
					position.SetIndexed(model.PrefixTemp, i, mustFloat(value))
				}
			}
		}

		if d.HBM >= 2 && len(values)-index >= 5 {
			cid := mustInt(values[index])
			index++
			mcc := mustInt(values[index])
			index++
			mnc := mustInt(values[index])
			index++
			rssi := mustInt(values[index])
			index++
			lac := mustInt(values[index])
			index++
			position.Network = model.NewNetworkWith(model.NewCellTowerWithRSSI(mcc, mnc, lac, int64(cid), rssi))
		}
	}

	result.AddPosition(position)
	return result, nil
}

// decodeUniversal handles the universal text layout selected by a short prefix
// or a zero second frame byte.
func (d *Decoder) decodeUniversal(result *codec.Result, values []string) (*codec.Result, error) {
	index := 0

	typeValue := values[index]
	index++

	if typeValue != "STT" && typeValue != "ALT" && typeValue != "BLE" &&
		typeValue != "RES" && typeValue != "UEX" {
		return result, nil
	}

	deviceID, err := d.resolve(result, values[index])
	if err != nil {
		return result, err
	}
	index++

	position := newPosition(deviceID)
	position.Set(model.KeyType, typeValue)

	if typeValue == "RES" {
		position.Outdated = true
		position.Set(model.KeyResult, strings.Join(values[index:], ";"))
		result.AddPosition(position)
		return result, nil
	}

	var mask int
	if typeValue == "BLE" {
		mask = 0b1100000110110
	} else {
		mask = parseHexInt(values[index])
		index++
	}

	if helper.CheckBit(int64(mask), 1) {
		index++ // model
	}
	if helper.CheckBit(int64(mask), 2) {
		position.Set(model.KeyVersionFW, values[index])
		index++
	}
	if helper.CheckBit(int64(mask), 3) && values[index] == "0" {
		position.Set(model.KeyArchive, true)
		index++
	} else if helper.CheckBit(int64(mask), 3) {
		index++
	}
	if helper.CheckBit(int64(mask), 4) && helper.CheckBit(int64(mask), 5) {
		position.Time = parseTime(values[index], values[index+1])
		index += 2
	}

	cell := &model.CellTower{}
	hasCell := false
	if helper.CheckBit(int64(mask), 6) {
		cell.CellID, _ = strconv.ParseInt(values[index], 16, 64)
		hasCell = true
		index++
	}
	if helper.CheckBit(int64(mask), 7) {
		cell.MobileCountryCode = mustInt(values[index])
		index++
	}
	if helper.CheckBit(int64(mask), 8) {
		cell.MobileNetworkCode = mustInt(values[index])
		index++
	}
	if helper.CheckBit(int64(mask), 9) {
		cell.LocationAreaCode = parseHexInt(values[index])
		index++
	}
	if hasCell {
		position.Network = model.NewNetworkWith(*cell)
	}

	if helper.CheckBit(int64(mask), 10) {
		position.Set(model.KeyRSSI, mustInt(values[index]))
		index++
	}
	if helper.CheckBit(int64(mask), 11) {
		position.Latitude = mustFloat(values[index])
		index++
	}
	if helper.CheckBit(int64(mask), 12) {
		position.Longitude = mustFloat(values[index])
		index++
	}

	if typeValue == "BLE" {
		position.Valid = true

		count := mustInt(values[index])
		index++
		for i := 1; i <= count; i++ {
			position.Set("tag"+strconv.Itoa(i)+"Rssi", mustInt(values[index]))
			index++
			index += 2 // rssi min, rssi max
			position.Set("tag"+strconv.Itoa(i)+"Id", values[index])
			index++
			position.Set("tag"+strconv.Itoa(i)+"Samples", mustInt(values[index]))
			index++
			position.Set("tag"+strconv.Itoa(i)+"Major", mustInt(values[index]))
			index++
			position.Set("tag"+strconv.Itoa(i)+"Minor", mustInt(values[index]))
			index++
		}
	} else {
		if helper.CheckBit(int64(mask), 13) {
			position.Speed = helper.KnotsFromKph(mustFloat(values[index]))
			index++
		}
		if helper.CheckBit(int64(mask), 14) {
			position.Course = mustFloat(values[index])
			index++
		}
		if helper.CheckBit(int64(mask), 15) {
			position.Set(model.KeySatellites, mustInt(values[index]))
			index++
		}
		if helper.CheckBit(int64(mask), 16) {
			position.Valid = values[index] == "1"
			index++
		}
		if helper.CheckBit(int64(mask), 17) {
			input := mustInt(values[index])
			index++
			position.Set(model.KeyIgnition, helper.CheckBit(int64(input), 0))
			position.Set(model.KeyInput, input)
		}
		if helper.CheckBit(int64(mask), 18) {
			position.Set(model.KeyOutput, mustInt(values[index]))
			index++
		}

		switch typeValue {
		case "ALT":
			if helper.CheckBit(int64(mask), 19) {
				position.AddAlarm(decodeAlert(mustInt(values[index])))
				index++
			}
			if helper.CheckBit(int64(mask), 20) {
				position.Set("alertModifier", values[index])
				index++
			}
			if helper.CheckBit(int64(mask), 21) {
				position.Set("alertData", values[index])
				index++
			}
		case "UEX":
			index = decodeSerialData(position, values, index)
		default:
			if helper.CheckBit(int64(mask), 19) {
				position.Set("mode", mustInt(values[index]))
				index++
			}
			if helper.CheckBit(int64(mask), 20) {
				position.Set("reason", mustInt(values[index]))
				index++
			}
			if helper.CheckBit(int64(mask), 21) {
				position.Set(model.KeyIndex, mustInt(values[index]))
				index++
			}
		}

		if helper.CheckBit(int64(mask), 22) {
			index++ // reserved
		}

		if helper.CheckBit(int64(mask), 23) && typeValue != "UEX" {
			assignMask := parseHexInt(values[index])
			index++
			for i := 0; i <= 30; i++ {
				if helper.CheckBit(int64(assignMask), uint(i)) {
					position.SetIndexed(model.PrefixIO, i+1, values[index])
					index++
				}
			}
		}

		assignIndex := 1
		for index < len(values) {
			value := values[index]
			index++
			if value != "" {
				position.Set("assign"+strconv.Itoa(assignIndex), value)
			}
			assignIndex++
		}
	}

	result.AddPosition(position)
	return result, nil
}

// decodeZip handles the binary STX framed records.
func (d *Decoder) decodeZip(result *codec.Result, buf *helper.Buffer) (*codec.Result, error) {
	buf.ReadUnsignedByte()  // header
	buf.ReadUnsignedShort() // length

	if buf.ReadUnsignedByte() != 0x10 {
		return result, nil
	}

	imei := strings.TrimLeft(hex.EncodeToString(buf.ReadSlice(5)), "0")
	deviceID, err := d.resolve(result, imei)
	if err != nil {
		return result, err
	}

	buf.ReadUnsignedByte()  // model
	buf.ReadUnsignedShort() // software version

	position := newPosition(deviceID)

	position.Time = helper.NewDateBuilder().
		SetDate(buf.ReadUnsignedByte(), buf.ReadUnsignedByte(), buf.ReadUnsignedByte()).
		SetTime(buf.ReadUnsignedByte(), buf.ReadUnsignedByte(), buf.ReadUnsignedByte()).
		Date()

	buf.ReadUnsignedShort() // lac
	buf.ReadUnsignedByte()  // cid

	position.Latitude = float64(buf.ReadUnsignedByte()) + float64(helper.BcdReadInteger(buf, 6))/1000000.0
	position.Longitude = float64(buf.ReadUnsignedByte()) + float64(helper.BcdReadInteger(buf, 6))/1000000.0
	position.Speed = float64(buf.ReadUnsignedShort()) + float64(helper.BcdReadInteger(buf, 2))/100.0
	position.Course = float64(buf.ReadUnsignedShort()) + float64(helper.BcdReadInteger(buf, 2))/100.0

	flags := buf.ReadUnsignedByte()
	position.Valid = helper.CheckBit(int64(flags), 7)
	if helper.CheckBit(int64(flags), 6) {
		position.Latitude = -position.Latitude
	}
	if helper.CheckBit(int64(flags), 5) {
		position.Longitude = -position.Longitude
	}

	position.Set(model.KeyOdometer, int64(buf.ReadUnsignedInt()))
	position.Set(model.KeyPower, float64(buf.ReadUnsignedByte())+float64(helper.BcdReadInteger(buf, 2))/100.0)

	io := buf.ReadUnsignedByte()
	position.Set(model.KeyIgnition, helper.CheckBit(int64(io), 0))
	for i := 1; i <= 3; i++ {
		position.SetIndexed(model.PrefixIn, i, helper.CheckBit(int64(io), uint(i)))
	}
	for i := 1; i <= 2; i++ {
		position.SetIndexed(model.PrefixOut, i, helper.CheckBit(int64(io), uint(i+3)))
	}

	position.Set(model.KeyEvent, buf.ReadUnsignedByte())

	if d.HBM == 1 {
		position.Set(model.KeyHours, int64(buf.ReadUnsignedInt()))
		position.Set(model.KeyBattery, buf.ReadUnsignedShort())
		if buf.ReadUnsignedByte() == 0 {
			position.Set(model.KeyArchive, true)
		}
	}

	if d.HBM == 2 {
		cid := buf.ReadUnsignedShort()
		mcc := buf.ReadUnsignedShort()
		mnc := buf.ReadUnsignedByte()
		rssi := buf.ReadUnsignedShort()
		lac := buf.ReadUnsignedShort()
		position.Network = model.NewNetworkWith(model.NewCellTowerWithRSSI(mcc, mnc, lac, int64(cid), rssi))
		buf.ReadUnsignedByte() // timing advance
	}

	buf.ReadUnsignedByte() // footer

	result.AddPosition(position)
	return result, nil
}

// readSignedMagnitudeInt reads a signed magnitude integer, matching
// BufferUtil.readSignedMagnitudeInt: the sign lives in the top bit.
func readSignedMagnitudeInt(buf *helper.Buffer) int {
	raw := buf.ReadUnsignedInt()
	value := int(raw & 0x7fffffff)
	if raw&0x80000000 != 0 {
		value = -value
	}
	return value
}

// decodeBinary handles the binary records whose second byte is zero.
func (d *Decoder) decodeBinary(result *codec.Result, buf *helper.Buffer) (*codec.Result, error) {
	typeValue := buf.ReadUnsignedByte()
	buf.ReadUnsignedShort() // length

	imei := strings.TrimLeft(hex.EncodeToString(buf.ReadSlice(5)), "0")
	deviceID, err := d.resolve(result, imei)
	if err != nil {
		return result, err
	}

	position := newPosition(deviceID)

	mask := buf.ReadUnsignedMedium()

	if helper.CheckBit(int64(mask), 1) {
		buf.ReadUnsignedByte() // model
	}

	if helper.CheckBit(int64(mask), 2) {
		position.Set(model.KeyVersionFW, fmt.Sprintf("%d.%d.%d",
			buf.ReadUnsignedByte(), buf.ReadUnsignedByte(), buf.ReadUnsignedByte()))
	}

	if helper.CheckBit(int64(mask), 3) && buf.ReadUnsignedByte() == 0 {
		position.Set(model.KeyArchive, true)
	}

	if helper.CheckBit(int64(mask), 4) && helper.CheckBit(int64(mask), 5) {
		position.Time = helper.NewDateBuilder().
			SetDate(buf.ReadUnsignedByte(), buf.ReadUnsignedByte(), buf.ReadUnsignedByte()).
			SetTime(buf.ReadUnsignedByte(), buf.ReadUnsignedByte(), buf.ReadUnsignedByte()).
			Date()
	}

	if helper.CheckBit(int64(mask), 6) {
		buf.ReadUnsignedInt() // cell
	}
	if helper.CheckBit(int64(mask), 7) {
		buf.ReadUnsignedShort() // mcc
	}
	if helper.CheckBit(int64(mask), 8) {
		buf.ReadUnsignedShort() // mnc
	}
	if helper.CheckBit(int64(mask), 9) {
		buf.ReadUnsignedShort() // lac
	}
	if helper.CheckBit(int64(mask), 10) {
		position.Set(model.KeyRSSI, buf.ReadUnsignedByte())
	}
	if helper.CheckBit(int64(mask), 11) {
		position.Latitude = float64(readSignedMagnitudeInt(buf)) / 1000000.0
	}
	if helper.CheckBit(int64(mask), 12) {
		position.Longitude = float64(readSignedMagnitudeInt(buf)) / 1000000.0
	}
	if helper.CheckBit(int64(mask), 13) {
		position.Speed = helper.KnotsFromKph(float64(buf.ReadUnsignedShort()) / 100.0)
	}
	if helper.CheckBit(int64(mask), 14) {
		position.Course = float64(buf.ReadUnsignedShort()) / 100.0
	}
	if helper.CheckBit(int64(mask), 15) {
		position.Set(model.KeySatellites, buf.ReadUnsignedByte())
	}
	if helper.CheckBit(int64(mask), 16) {
		position.Valid = buf.ReadUnsignedByte() > 0
	}
	if helper.CheckBit(int64(mask), 17) {
		input := buf.ReadUnsignedByte()
		position.Set(model.KeyIgnition, helper.CheckBit(int64(input), 0))
		position.Set(model.KeyInput, input)
	}
	if helper.CheckBit(int64(mask), 18) {
		position.Set(model.KeyOutput, buf.ReadUnsignedByte())
	}

	alertID := 0
	if helper.CheckBit(int64(mask), 19) {
		alertID = buf.ReadUnsignedByte()
		if typeValue == 0x82 {
			position.AddAlarm(decodeAlert(alertID))
		}
	}

	if helper.CheckBit(int64(mask), 20) {
		buf.ReadUnsignedShort() // alert modifier
	}

	if helper.CheckBit(int64(mask), 21) && alertID == 59 {
		position.Set(model.KeyDriverID, hex.EncodeToString(buf.ReadSlice(8)))
	}

	result.AddPosition(position)
	return result, nil
}

// decodeTravelReport handles the *HTE travel report frames, which only carry a
// driver unique id.
func (d *Decoder) decodeTravelReport(result *codec.Result, values []string) (*codec.Result, error) {
	index := 1

	deviceID, err := d.resolve(result, values[index])
	if err != nil {
		return result, err
	}
	index++

	position := newPosition(deviceID)
	position.Outdated = true
	position.Set(model.KeyDriverID, values[len(values)-1])

	result.AddPosition(position)
	return result, nil
}

func mustInt(value string) int {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}

func mustFloat(value string) float64 {
	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return n
}

var _ codec.Decoder = (*Decoder)(nil)
