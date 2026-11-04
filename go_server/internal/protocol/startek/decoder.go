// Package startek implements the Startek text protocol, a port of
// org.traccar.protocol.StartekProtocolDecoder. A frame reads
// "&&<index><length>,<uniqueId>,<type>,<content><checksum>\r\n": the length
// covers the index character, the unique id, the separating comma and the
// payload that follows it, and the checksum is two ASCII hex digits.
package startek

import (
	"strconv"
	"strings"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and stored on positions.
const ProtocolName = "startek"

var pattern = helper.NewPatternBuilder().
	Text("&&").
	Expression(".").    // index
	Number("d+,").      // length
	Number("(d+),").    // imei
	Number("(xxx),").   // type
	Expression("(.+)"). // content
	Number("xx").       // checksum
	Text("\r\n").
	Compile()

var positionPattern = helper.NewPatternBuilder().
	Number("(d+),").         // event
	Expression("([^,]+)?,"). // event data
	Number("(dd)(dd)(dd)").  // date (yyyymmdd)
	Number("(dd)(dd)(dd),"). // time (hhmmss)
	Expression("([AV]),").   // valid
	Number("(-?d+.d+),").    // longitude
	Number("(-?d+.d+),").    // latitude
	Number("(d+),").         // satellites
	Number("(d+.d+),").      // hdop
	Number("(d+),").         // speed
	Number("(d+),").         // course
	Number("(-?d+),").       // altitude
	Number("(d+),").         // odometer
	Number("(d+)|").         // mcc
	Number("(d+)|").         // mnc
	Number("(x+)|").         // lac
	Number("(x+),").         // cid
	Number("(d+),").         // rssi
	Number("(x+),").         // status
	Number("(x+),").         // inputs
	Number("(x+),").         // outputs
	Number("(x+)|").         // power
	Number("(x+)").          // battery
	Expression("([^,]+)?").  // adc
	GroupBegin().
	Number(",d+").           // extended
	Expression(",([^,]+)?"). // fuel
	GroupBegin().
	Expression(",([^,]+)?"). // temperature
	GroupBegin().
	Text(",").
	GroupBegin().
	Number("(d+)?|"). // rpm
	Number("(d+)?|"). // engine load
	Number("(d+)?|"). // maf flow
	Number("(d+)?|"). // intake pressure
	Number("(d+)?|"). // intake temperature
	Number("(d+)?|"). // throttle
	Number("(d+)?|"). // coolant temperature
	Number("(d+)?|"). // instant fuel
	Number("(d+)[%L]").
	Optional(1). // fuel level
	GroupEndWith("?").
	Expression(",([^,]{20,})").
	Optional(1). // driver id
	Number(",(d+)").
	Optional(1). // hours
	GroupEndWith("?").
	GroupEndWith("?").
	GroupEndWith("?").
	Any().
	Compile()

// Decoder parses one frame.
type Decoder struct {
	codec.Base
}

// NewDecoder creates the decoder.
func NewDecoder(base codec.Base) *Decoder { return &Decoder{Base: base} }

// Name implements codec.Decoder.
func (d *Decoder) Name() string { return ProtocolName }

// Decode implements codec.Decoder.
func (d *Decoder) Decode(frame []byte) (*codec.Result, error) {
	parser := helper.NewParser(pattern, string(frame))
	if !parser.Matches() {
		return &codec.Result{}, nil
	}

	session := d.resolve(parser.Next())
	if session == nil {
		return &codec.Result{}, nil
	}

	messageType := parser.Next()
	content := parser.Next()

	var position *model.Position
	switch messageType {
	case "000":
		position = d.decodePosition(session, content)
	case "710":
		position = d.decodeSerial(session, content)
	default:
		position = model.NewPosition(ProtocolName)
		position.DeviceID = session.DeviceID
		d.copyLastLocation(position)
		position.Outdated = true
		position.Set(model.KeyType, messageType)
		position.Set(model.KeyResult, content)
	}
	if position == nil {
		return &codec.Result{}, nil
	}

	result := codec.NewResult()
	result.AddPosition(position)
	return result, nil
}

func (d *Decoder) decodePosition(session *codec.DeviceSession, content string) *model.Position {
	parser := helper.NewParser(positionPattern, content)
	if !parser.Matches() {
		return nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID

	event := parser.NextInt(0)
	eventData := parser.Next()
	position.Set(model.KeyEvent, event)
	if event == 53 {
		position.Set(model.KeyDriverID, eventData)
	} else if alarm := decodeAlarm(event); alarm != "" {
		position.AddAlarm(alarm)
	}

	position.Time = parser.NextDateTime(helper.YMDHMS)
	position.Valid = parser.Next() == "A"
	position.Longitude = parser.NextDouble(0)
	position.Latitude = parser.NextDouble(0)

	position.Set(model.KeySatellites, parser.NextInt(0))
	position.Set(model.KeyHDOP, parser.NextDouble(0))

	position.Speed = helper.KnotsFromKph(float64(parser.NextInt(0)))
	position.Course = float64(parser.NextInt(0))
	position.Altitude = float64(parser.NextInt(0))

	position.Set(model.KeyOdometer, parser.NextLong(10, 0))

	mcc := parser.NextInt(0)
	mnc := parser.NextInt(0)
	lac := parser.NextHexInt(0)
	cid := parser.NextHexInt(0)
	rssi := parser.NextInt(0)
	position.SetNetwork(model.NewNetworkWith(model.NewCellTowerWithRSSI(mcc, mnc, lac, int64(cid), rssi)))

	position.Set(model.KeyStatus, parser.NextHexInt(0))

	input := parser.NextHexInt(0)
	output := parser.NextHexInt(0)
	position.Set(model.KeyIgnition, helper.CheckBit(int64(input), 1))
	position.Set(model.KeyDoor, helper.CheckBit(int64(input), 2))
	position.Set(model.KeyInput, input)
	position.Set(model.KeyOutput, output)

	position.Set(model.KeyPower, float64(parser.NextHexInt(0))/100)
	position.Set(model.KeyBattery, float64(parser.NextHexInt(0))/100)

	if parser.HasNext() {
		values := strings.Split(parser.Next(), "|")
		for index := 1; index < len(values); index++ {
			value, err := strconv.ParseInt(values[index], 16, 64)
			if err != nil {
				continue
			}
			position.SetIndexed(model.PrefixADC, index+1, float64(value)/100)
		}
	}

	if parser.HasNext() {
		for _, fuel := range strings.Split(parser.Next(), "|") {
			if len(fuel) < 2 {
				continue
			}
			index, err := strconv.Atoi(fuel[:2])
			if err != nil {
				continue
			}
			value, err := strconv.ParseInt(fuel[2:], 16, 64)
			if err != nil {
				continue
			}
			position.Set("fuel"+strconv.Itoa(index), float64(value)/10)
		}
	}

	if parser.HasNext() {
		for _, temperature := range strings.Split(parser.Next(), "|") {
			if len(temperature) < 2 {
				continue
			}
			index, err := strconv.Atoi(temperature[:2])
			if err != nil {
				continue
			}
			value, err := strconv.ParseInt(temperature[2:], 16, 64)
			if err != nil {
				continue
			}
			converted := float64(value & 0x7fff)
			if helper.CheckBit(value, 15) {
				converted = -converted
			}
			position.SetIndexed(model.PrefixTemp, index, converted/10)
		}
	}

	if parser.HasNextAny(9) {
		setOptionalInt(position, model.KeyRPM, parser)
		setOptionalInt(position, model.KeyEngineLd, parser)
		setOptionalInt(position, "airFlow", parser)
		setOptionalInt(position, "airPressure", parser)
		if parser.HasNext() {
			position.Set("airTemp", parser.NextInt(0)-40)
		}
		setOptionalInt(position, model.KeyThrottle, parser)
		if parser.HasNext() {
			position.Set(model.KeyCoolant, parser.NextInt(0)-40)
		}
		if parser.HasNext() {
			position.Set(model.KeyFuelConsumption, float64(parser.NextInt(0))/10)
		}
		setOptionalInt(position, model.KeyFuel, parser)
	}

	if parser.HasNext() {
		position.Set(model.KeyDriverID, parser.Next())
	}

	if parser.HasNext() {
		position.Set(model.KeyHours, int64(parser.NextInt(0))*1000)
	}

	return position
}

func (d *Decoder) decodeSerial(session *codec.DeviceSession, content string) *model.Position {
	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID
	d.copyLastLocation(position)
	position.Outdated = true

	frames := strings.Split(content, "\r\n")

	for _, frame := range frames {
		values := strings.Split(frame, ",")
		index := 1
		next := func() string {
			value := values[index]
			index++
			return value
		}

		switch values[0] {
		case "T1":
			// A short frame would have raised an exception in the reference
			// decoder, which drops the whole message.
			if len(values) < 29 {
				continue
			}
			index += 1 // speed
			setNumber(position, model.KeyRPM, next())
			index += 1 // fuel consumption
			setNumber(position, model.KeyFuel, next())
			index += 4 // axle weights
			index += 1 // turbo pressure
			setInteger(position, model.KeyCoolant, next())
			index += 1 // accelerator pedal
			setInteger(position, "torque", next())
			index += 1 // firmware version
			setNumber(position, model.KeyPower, next())
			index += 1 // coolant level
			setNumber(position, "oilTemp", next())
			index += 1 // oil level
			setNumber(position, model.KeyThrottle, next())
			index += 1 // air inlet pressure
			index += 1 // fuel tank secondary
			index += 1 // current gear
			index += 1 // seatbelt
			setInteger(position, "oilPressure", next())
			index += 1 // wet tank air pressure
			index += 1 // pto state
			if ignition, err := strconv.Atoi(next()); err == nil && ignition < 2 {
				position.Set(model.KeyIgnition, ignition > 0)
			}
			index += 1 // brake pedal
			setNumber(position, "catalystLevel", next())
			index += 1 // fuel type

		case "T2":
			if len(values) < 22 {
				continue
			}
			if odometer, err := strconv.ParseFloat(next(), 64); err == nil {
				position.Set(model.KeyOdometer, odometer*1000)
			}
			index += 1 // total fuel
			index += 1 // fuel used cruise
			index += 1 // fuel used drive
			index += 1
			index += 1
			index += 1 // total idle time
			index += 1 // total time pto
			index += 1 // time cruise
			index += 1
			index += 1
			index += 1
			index += 1
			index += 1
			index += 1 // brake apps
			index += 1 // clutch apps
			setInteger(position, model.KeyHours, next())
			index += 1 // time torque
			setNumber(position, model.KeyFuelConsumption, next())
			index += 1 // total cruise control distance
			setNumber(position, model.KeyFuelUsed, next())
			index += 1 // total drive time
		}
	}

	return position
}

func decodeAlarm(value int) string {
	switch value {
	case 1:
		return model.AlarmSOS
	case 5, 6:
		return model.AlarmDoor
	case 17:
		return model.AlarmLowPower
	case 18:
		return model.AlarmPowerCut
	case 19:
		return model.AlarmPowerRestored
	case 39:
		return model.AlarmAcceleration
	case 40:
		return model.AlarmBraking
	case 41:
		return model.AlarmCornering
	default:
		return ""
	}
}

// setOptionalInt stores an integer that the reference decoder skips when the
// device left the field empty.
func setOptionalInt(position *model.Position, key string, parser *helper.Parser) {
	if value := parser.NextIntPtr(); value != nil {
		position.Set(key, *value)
	}
}

func setNumber(position *model.Position, key string, text string) {
	if value, err := strconv.ParseFloat(text, 64); err == nil {
		position.Set(key, value)
	}
}

func setInteger(position *model.Position, key string, text string) {
	if value, err := strconv.Atoi(text); err == nil {
		position.Set(key, value)
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

// copyLastLocation seeds a position from the last known fix, which the
// non-position frames do not carry.
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

var _ codec.Decoder = (*Decoder)(nil)
