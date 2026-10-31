package h02

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and stored on positions.
const ProtocolName = "h02"

const (
	timeFormat = "150405"
	dateFormat = "20060102150405"
)

var (
	patternText = helper.NewPatternBuilder().
			Text("*").
			Expression("..,"). // manufacturer
			Number("(d+)?,").  // imei
			GroupBegin().
			Text("V4,").
			Expression("(.*),"). // response
			Or().
			Expression("(V[^,]*),").
			GroupEnd().
			Number("(?:(dd)(dd)(dd))?,"). // time (hhmmss)
			GroupBegin().
			Expression("([ABV])?,"). // validity
			Or().
			Number("(d+),"). // coding scheme
			GroupEnd().
			GroupBegin().
			Number("-(d+)-(d+.d+),([NS]),"). // latitude
			Or().
			Number("(d*?)(d?d.d+),([NS]),"). // latitude
			Or().
			Number("(d+)(dd)(d{4}),([NS]),"). // latitude
			GroupEnd().
			GroupBegin().
			Number("-(d+)-(d+.d+),([EW]),"). // longitude
			Or().
			Number("(d*?)(d?d.d+),([EW]),"). // longitude
			Or().
			Number("(d+)(dd)(d{4}),([EW]),"). // longitude
			GroupEnd().
			Number(" *(d+.?d*),").       // speed
			Number("(d+.?d*)?,").        // course
			Number("(?:d+,)?").          // battery
			Number("(?:(dd)(dd)(dd))?"). // date (ddmmyy)
			GroupBegin().
			Text(",,").
			Expression("[^,]*,"). // phone or ip
			Number("(d+)").       // iccid
			Expression("[^,]*").  // padding
			GroupEndWith("?").
			GroupBegin().
			Number(",(x{8})").
			GroupBegin().
			Number(",(d+),").   // odometer
			Number("(-?d+),").  // temperature
			Number("(d+.d+),"). // fuel
			Number("(-?d+),").  // altitude
			Number("(x+),").    // lac
			Number("(x+)").     // cid
			Or().
			Text(",").
			Expression("(.*)"). // data
			Or().
			GroupEnd().
			Or().
			GroupEnd().
			Text("#").
			Compile()

	patternNBR = helper.NewPatternBuilder().
			Text("*").
			Expression("..,").
			Number("(d+),"). // imei
			Text("NBR,").
			Number("(dd)(dd)(dd),").      // time (hhmmss)
			Number("(d+),").              // mcc
			Number("(d+),").              // mnc
			Number("d+,").                // gsm delay time
			Number("d+,").                // count
			Number("((?:d+,d+,-?d+,)+)"). // cells
			Number("(dd)(dd)(dd),").      // date (ddmmyy)
			Number("(x{8})").             // status
			Any().
			Compile()

	patternLink = helper.NewPatternBuilder().
			Text("*").
			Expression("..,").
			Number("(d+),"). // imei
			Text("LINK,").
			Number("(dd)(dd)(dd),"). // time (hhmmss)
			Number("(d+),").         // rssi
			Number("(d+),").         // satellites
			Number("(d+),").         // battery
			Number("(d+),").         // steps
			Number("(d+),").         // turnovers
			Number("(dd)(dd)(dd),"). // date (ddmmyy)
			Number("(x{8})").        // status
			Any().
			Compile()

	patternV3 = helper.NewPatternBuilder().
			Text("*").
			Expression("..,").
			Number("(d+),"). // imei
			Text("V3,").
			Number("(dd)(dd)(dd),"). // time (hhmmss)
			Number("(ddd)").         // mcc
			Number("(d+),").         // mnc
			Number("(d+),").         // count
			Expression("(.*),").     // cell info
			Number("(x{4}),").       // battery
			Number("d+,").           // reboot info
			Text("X,").
			Number("(dd)(dd)(dd),"). // date (ddmmyy)
			Number("(x{8})").        // status
			Text("#").OptionalOne().
			Compile()

	patternVP1 = helper.NewPatternBuilder().
			Text("*hq,").
			Number("(d{15}),"). // imei
			Text("VP1,").
			GroupBegin().
			Text("V,").
			Number("(d+),").       // mcc
			Number("(d+),").       // mnc
			Expression("([^#]+)"). // cells
			Or().
			Expression("[AB],").    // validity
			Number("(d+)(dd.d+),"). // latitude
			Expression("([NS]),").
			Number("(d+)(dd.d+),"). // longitude
			Expression("([EW]),").
			Number("(d+.d+),").     // speed
			Number("(d+.d+),").     // course
			Number("(dd)(dd)(dd)"). // date (ddmmyy)
			GroupEnd().
			Any().
			Compile()

	patternHTBT = helper.NewPatternBuilder().
			Text("*HQ,").
			Number("(d{15}),"). // imei
			Text("HTBT,").
			Number("(d+)"). // battery
			Any().
			Compile()

	patternSMS = helper.NewPatternBuilder().
			Text("*HQ,").
			Number("(d+),"). // id
			Text("SMS,").
			Expression("(.+)").
			Text("#").
			Compile()
)

// Decoder decodes H02 frames into positions.
type Decoder struct {
	codec.Base
}

// NewDecoder builds an H02 decoder. Set Base.Ack to enable the optional R12
// acknowledgements.
func NewDecoder(base codec.Base) *Decoder {
	return &Decoder{Base: base}
}

// Name implements codec.Decoder.
func (d *Decoder) Name() string {
	return ProtocolName
}

// Decode implements codec.Decoder.
func (d *Decoder) Decode(frame []byte) (*codec.Result, error) {
	if len(frame) == 0 {
		return &codec.Result{}, nil
	}

	result := codec.NewResult()

	switch frame[0] {
	case '*':
		sentence := strings.TrimSpace(string(frame))

		first := strings.IndexByte(sentence, ',')
		if first < 0 {
			return &codec.Result{}, nil
		}
		second := strings.IndexByte(sentence[first+1:], ',')
		if second < 0 {
			return &codec.Result{}, nil
		}
		typeStart := first + 1 + second + 1

		typeEnd := strings.IndexByte(sentence[typeStart:], ',')
		if typeEnd < 0 {
			typeEnd = strings.IndexByte(sentence[typeStart:], '#')
		}
		if typeEnd < 0 {
			return &codec.Result{}, nil
		}
		typeEnd += typeStart
		if typeEnd <= 0 {
			return &codec.Result{}, nil
		}

		switch sentence[typeStart:typeEnd] {
		case "V0", "HTBT":
			result.AddResponse([]byte(sentence[:typeEnd] + "#"))
			return d.decodeHeartbeat(sentence, result)
		case "NBR":
			return d.decodeLbs(sentence, result)
		case "LINK":
			return d.decodeLink(sentence, result)
		case "V3":
			return d.decodeV3(sentence, result)
		case "VP1":
			return d.decodeVp1(sentence, result)
		case "SMS":
			return d.decodeSms(sentence, result)
		default:
			return d.decodeText(sentence, result)
		}

	case '$':
		return d.decodeBinary(frame, result)

	default:
		return &codec.Result{}, nil
	}
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

func (d *Decoder) sendResponse(result *codec.Result, id, responseType string) {
	if id == "" {
		return
	}
	var response string
	if responseType == "R12" {
		response = fmt.Sprintf("*HQ,%s,%s,%s#", id, responseType, d.Time().UTC().Format(timeFormat))
	} else {
		response = fmt.Sprintf("*HQ,%s,V4,%s,%s#", id, responseType, d.Time().UTC().Format(dateFormat))
	}
	result.AddResponse([]byte(response))
}

func readCoordinate(buf *helper.Buffer, lon bool) float64 {
	degrees := helper.BcdReadInteger(buf, 2)
	if lon {
		degrees = degrees*10 + buf.GetUnsignedByte(0)>>4
	}

	var result float64
	if lon {
		result = float64(buf.ReadUnsignedByte() & 0x0f)
	}

	length := 6
	if lon {
		length = 5
	}

	result = result*10 + float64(helper.BcdReadInteger(buf, length))/10000.0

	result /= 60
	result += float64(degrees)

	return result
}

func processStatus(position *model.Position, status int64) {
	if !helper.CheckBit(status, 0) {
		position.AddAlarm(model.AlarmVibration)
	} else if !helper.CheckBit(status, 1) || !helper.CheckBit(status, 18) {
		position.AddAlarm(model.AlarmSOS)
	} else if !helper.CheckBit(status, 2) {
		position.AddAlarm(model.AlarmOverspeed)
	} else if !helper.CheckBit(status, 19) {
		position.AddAlarm(model.AlarmPowerCut)
	}

	position.Set(model.KeyIgnition, helper.CheckBit(status, 10))
	position.Set(model.KeyStatus, status)
}

func decodeBattery(value int) *int {
	var result int
	switch {
	case value == 0:
		return nil
	case value <= 3:
		result = (value - 1) * 10
	case value <= 6:
		result = (value - 1) * 20
	case value <= 100:
		result = value
	case value >= 0xF1 && value <= 0xF6:
		result = value - 0xF0
	default:
		return nil
	}
	return &result
}

func (d *Decoder) decodeBinary(frame []byte, result *codec.Result) (*codec.Result, error) {
	buf := helper.NewBuffer(frame)

	longID := buf.ReadableBytes() == 42

	buf.ReadUnsignedByte() // marker

	var id string
	if longID {
		id = hex.EncodeToString(buf.ReadSlice(8))
		if len(id) > 15 {
			id = id[:15]
		}
	} else {
		id = hex.EncodeToString(buf.ReadSlice(5))
	}

	session := d.resolve(id)
	if session == nil {
		return &codec.Result{}, nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID

	dateBuilder := helper.NewDateBuilder().
		SetHour(helper.BcdReadInteger(buf, 2)).
		SetMinute(helper.BcdReadInteger(buf, 2)).
		SetSecond(helper.BcdReadInteger(buf, 2)).
		SetDay(helper.BcdReadInteger(buf, 2)).
		SetMonth(helper.BcdReadInteger(buf, 2)).
		SetYear(helper.BcdReadInteger(buf, 2))
	position.Time = dateBuilder.Date()

	latitude := readCoordinate(buf, false)
	if battery := decodeBattery(buf.ReadUnsignedByte()); battery != nil {
		position.Set(model.KeyBatteryLevel, *battery)
	}
	longitude := readCoordinate(buf, true)

	flags := buf.ReadUnsignedByte() & 0x0f
	position.Valid = flags&0x02 != 0
	if flags&0x04 == 0 {
		latitude = -latitude
	}
	if flags&0x08 == 0 {
		longitude = -longitude
	}

	position.Latitude = latitude
	position.Longitude = longitude

	position.Speed = float64(helper.BcdReadInteger(buf, 3))
	position.Course = float64(buf.ReadUnsignedByte()&0x0f)*100 + float64(helper.BcdReadInteger(buf, 2))

	processStatus(position, int64(buf.ReadUnsignedInt()))

	if d.Ack {
		d.sendResponse(result, id, "R12")
	}

	result.AddPosition(position)
	return result, nil
}

func (d *Decoder) decodeText(sentence string, result *codec.Result) (*codec.Result, error) {
	parser := helper.NewParser(patternText, sentence)
	if !parser.Matches() {
		return &codec.Result{}, nil
	}

	id := parser.Next()
	session := d.resolve(id)
	if session == nil {
		return &codec.Result{}, nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID

	if parser.HasNext() {
		position.Set(model.KeyResult, parser.Next())
	}

	if parser.HasNext() && parser.Next() == "V1" {
		d.sendResponse(result, id, "V1")
	} else if d.Ack {
		d.sendResponse(result, id, "R12")
	}

	dateBuilder := helper.NewDateBuilder()
	if parser.HasNextN(3) {
		dateBuilder.SetTime(parser.NextInt(0), parser.NextInt(0), parser.NextInt(0))
	}

	if parser.HasNext() {
		position.Valid = parser.Next() == "A"
	}
	if parser.HasNext() {
		parser.NextInt(0) // coding scheme
		position.Valid = true
	}

	if parser.HasNextN(3) {
		position.Latitude = parser.NextCoordinate(helper.DegMinHem)
	}
	if parser.HasNextAny(3) {
		position.Latitude = parser.NextCoordinate(helper.DegMinHem)
	}
	if parser.HasNextN(4) {
		position.Latitude = parser.NextCoordinate(helper.DegMinMinHem)
	}

	if parser.HasNextN(3) {
		position.Longitude = parser.NextCoordinate(helper.DegMinHem)
	}
	if parser.HasNextAny(3) {
		position.Longitude = parser.NextCoordinate(helper.DegMinHem)
	}
	if parser.HasNextN(4) {
		position.Longitude = parser.NextCoordinate(helper.DegMinMinHem)
	}

	position.Speed = parser.NextDouble(0)
	position.Course = parser.NextDouble(0)

	if parser.HasNextN(3) {
		dateBuilder.SetDateReverse(parser.NextInt(0), parser.NextInt(0), parser.NextInt(0))
		position.Time = dateBuilder.Date()
	} else {
		position.Time = d.Time()
	}

	if parser.HasNext() {
		position.Set(model.KeyICCID, parser.Next())
	}

	if parser.HasNext() {
		processStatus(position, parser.NextLong(16, 0))
	}

	if parser.HasNextN(6) {
		position.Set(model.KeyOdometer, parser.NextInt(0))
		position.SetIndexed(model.PrefixTemp, 1, parser.NextInt(0))
		position.Set(model.KeyFuel, parser.NextDouble(0))
		position.Altitude = float64(parser.NextInt(0))
		position.SetNetwork(model.NewNetworkWith(
			model.NewCellTower(0, 0, parser.NextHexInt(0), int64(parser.NextHexInt(0)))))
	}

	if parser.HasNext() {
		values := strings.Split(parser.Next(), ",")
		for i, value := range values {
			position.SetIndexed(model.PrefixIO, i+1, strings.TrimSpace(value))
		}
	}

	result.AddPosition(position)
	return result, nil
}

func (d *Decoder) decodeLbs(sentence string, result *codec.Result) (*codec.Result, error) {
	parser := helper.NewParser(patternNBR, sentence)
	if !parser.Matches() {
		return &codec.Result{}, nil
	}

	id := parser.Next()
	session := d.resolve(id)
	if session == nil {
		return &codec.Result{}, nil
	}

	d.sendResponse(result, id, "NBR")

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID

	dateBuilder := helper.NewDateBuilder().
		SetTime(parser.NextInt(0), parser.NextInt(0), parser.NextInt(0))

	network := model.NewNetwork()
	mcc := parser.NextInt(0)
	mnc := parser.NextInt(0)

	cells := strings.Split(parser.Next(), ",")
	for i := 0; i < len(cells)/3; i++ {
		network.AddCellTower(model.NewCellTower(
			mcc, mnc, parseInt(cells[i*3]), int64(parseInt(cells[i*3+1]))))
	}
	position.SetNetwork(network)

	dateBuilder.SetDateReverse(parser.NextInt(0), parser.NextInt(0), parser.NextInt(0))

	position.MarkOutdated(dateBuilder.Date())

	processStatus(position, parser.NextLong(16, 0))

	result.AddPosition(position)
	return result, nil
}

func (d *Decoder) decodeLink(sentence string, result *codec.Result) (*codec.Result, error) {
	parser := helper.NewParser(patternLink, sentence)
	if !parser.Matches() {
		return &codec.Result{}, nil
	}

	session := d.resolve(parser.Next())
	if session == nil {
		return &codec.Result{}, nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID

	dateBuilder := helper.NewDateBuilder().
		SetTime(parser.NextInt(0), parser.NextInt(0), parser.NextInt(0))

	position.Set(model.KeyRSSI, parser.NextInt(0))
	position.Set(model.KeySatellites, parser.NextInt(0))
	position.Set(model.KeyBatteryLevel, parser.NextInt(0))
	position.Set(model.KeySteps, parser.NextInt(0))
	position.Set("turnovers", parser.NextInt(0))

	dateBuilder.SetDateReverse(parser.NextInt(0), parser.NextInt(0), parser.NextInt(0))

	position.MarkOutdated(dateBuilder.Date())

	processStatus(position, parser.NextLong(16, 0))

	result.AddPosition(position)
	return result, nil
}

func (d *Decoder) decodeV3(sentence string, result *codec.Result) (*codec.Result, error) {
	parser := helper.NewParser(patternV3, sentence)
	if !parser.Matches() {
		return &codec.Result{}, nil
	}

	session := d.resolve(parser.Next())
	if session == nil {
		return &codec.Result{}, nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID

	dateBuilder := helper.NewDateBuilder().
		SetTime(parser.NextInt(0), parser.NextInt(0), parser.NextInt(0))

	mcc := parser.NextInt(0)
	mnc := parser.NextInt(0)

	count := parser.NextInt(0)
	network := model.NewNetwork()
	values := strings.Split(parser.Next(), ",")
	for i := 0; i < count && i*4+1 < len(values); i++ {
		network.AddCellTower(model.NewCellTower(
			mcc, mnc, parseInt(values[i*4]), int64(parseInt(values[i*4+1]))))
	}
	position.SetNetwork(network)

	position.Set(model.KeyBattery, parser.NextHexInt(0))

	dateBuilder.SetDateReverse(parser.NextInt(0), parser.NextInt(0), parser.NextInt(0))

	position.MarkOutdated(dateBuilder.Date())

	processStatus(position, parser.NextLong(16, 0))

	result.AddPosition(position)
	return result, nil
}

func (d *Decoder) decodeSms(sentence string, result *codec.Result) (*codec.Result, error) {
	parser := helper.NewParser(patternSMS, sentence)
	if !parser.Matches() {
		return &codec.Result{}, nil
	}

	session := d.resolve(parser.Next())
	if session == nil {
		return &codec.Result{}, nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID

	position.MarkOutdated(time.Time{})

	position.Set(model.KeyResult, parser.Next())

	result.AddPosition(position)
	return result, nil
}

func (d *Decoder) decodeVp1(sentence string, result *codec.Result) (*codec.Result, error) {
	parser := helper.NewParser(patternVP1, sentence)
	if !parser.Matches() {
		return &codec.Result{}, nil
	}

	session := d.resolve(parser.Next())
	if session == nil {
		return &codec.Result{}, nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID

	if parser.HasNextN(3) {
		position.MarkOutdated(time.Time{})

		mcc := parser.NextInt(0)
		mnc := parser.NextInt(0)

		network := model.NewNetwork()
		for _, cell := range strings.Split(parser.Next(), "Y") {
			values := strings.Split(cell, ",")
			if len(values) < 3 {
				continue
			}
			network.AddCellTower(model.NewCellTower(
				mcc, mnc, parseInt(values[0]), int64(parseInt(values[1]))))
		}
		position.SetNetwork(network)
	} else {
		position.Valid = true
		position.Latitude = parser.NextCoordinate(helper.DegMinHem)
		position.Longitude = parser.NextCoordinate(helper.DegMinHem)
		position.Speed = parser.NextDouble(0)
		position.Course = parser.NextDouble(0)

		position.Time = helper.NewDateBuilder().
			SetDateReverse(parser.NextInt(0), parser.NextInt(0), parser.NextInt(0)).
			Date()
	}

	result.AddPosition(position)
	return result, nil
}

func (d *Decoder) decodeHeartbeat(sentence string, result *codec.Result) (*codec.Result, error) {
	parser := helper.NewParser(patternHTBT, sentence)
	if !parser.Matches() {
		return &codec.Result{}, nil
	}

	session := d.resolve(parser.Next())
	if session == nil {
		return &codec.Result{}, nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID

	position.MarkOutdated(time.Time{})

	position.Set(model.KeyBatteryLevel, parser.NextInt(0))

	result.AddPosition(position)
	return result, nil
}

func parseInt(value string) int {
	var result int
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c < '0' || c > '9' {
			break
		}
		result = result*10 + int(c-'0')
	}
	return result
}

var _ codec.Decoder = (*Decoder)(nil)
