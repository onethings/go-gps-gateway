package startek_test

import (
	"fmt"
	"math"
	"testing"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/protocol/protocaltest"
	"traccar-server_go/go_server/internal/protocol/startek"
)

// resolver gives every IMEI its own device id, so a fixture that resolves to the
// wrong session cannot pass unnoticed.
type resolver map[string]int64

func (r resolver) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	deviceID, ok := r[uniqueID]
	if !ok {
		return nil, nil
	}
	return &codec.DeviceSession{DeviceID: deviceID, UniqueID: uniqueID}, nil
}

var devices = resolver{
	"868825064282040": 1,
	"865491061145970": 2,
	"863911061945394": 3,
	"868703050178631": 4,
	"869926040743375": 5,
	"860294046453690": 6,
	"860262050066062": 7,
	"865429043319537": 8,
	"860262050015424": 9,
	"861157040151686": 10,
	"861157040554384": 11,
	"860262050010565": 12,
	"021104023195429": 13,
	"860262050009146": 14,
}

func newDecoder() *startek.Decoder {
	return startek.NewDecoder(codec.Base{Resolver: devices})
}

func TestDecoderFixtures(t *testing.T) {
	cases := []struct {
		name     string
		frame    string
		deviceID int64
		expected *protocaltest.Expectation
		check    func(t *testing.T, position *model.Position)
	}{
		{
			name:     "driver id in the event data",
			frame:    "&&L171,868825064282040,000,0,,241209063302,A,13.809656,100.558255,14,0.9,0,0,67,1560,520|4|A418|008AAC3F,31,000000BD,02,00,04E3|0171|0000|0000,131,,,,3100  1  61000541  10800  ?FE\r\n",
			deviceID: 1,
			expected: protocaltest.PositionWith(protocaltest.ChecksAttributes).
				Attribute(model.KeyDriverID, "3100  1  61000541  10800  ?"),
		},
		{
			name: "serial reports",
			frame: "&&\\546,865491061145970,710,T1,0.0,0.0,0.0,0.0,0,0,0,0,0.0,0,0.0,0,F0,0.0,0.0,0.0,0.0,0.0,0,0.0,0,0,0,0,0,1,0,0.0,00,0.0,0.0,0.0,0\r\n" +
				"T2,0.000,0.0,9223372036854775808.8,9223372036854775808.8,4294967295,4294967295,0,429496729,0,0,0,0,0,0,0,0,0,0,0.0,21474836.0,9223372036854775808.8,0,00,0,0,0.00\r\n" +
				"T5,0,0,0,0,0,0,0,0,0,0,0,0,0.0,0.0,*,*,0\r\n" +
				"T6,00,03,00,1F,1F,1F,0E,03,00,00,00,00,1F,1F\r\n" +
				"T7,0,0,0,429496729,21474836.000,429496729,0.000,0,0.000,0,0.000,0,0.000,0,0.000,0,0.000,0,0.000,0,0.000,0,0.000,0,0,0,0.000\r\n" +
				"Tx,*,*,*,*,*,0.0,0,0,0,0,-125,0,0,0,0,0,0,0\r\n" +
				"F3\r\n",
			deviceID: 2,
			expected: protocaltest.PositionWith(protocaltest.ChecksAttributes).
				Attribute(model.KeyOdometer, 0).
				Attribute(model.KeyHours, 0).
				Attribute(model.KeyRPM, 0).
				Attribute(model.KeyFuel, 0).
				Attribute(model.KeyFuelConsumption, 0).
				Attribute(model.KeyFuelUsed, 9.223372036854775808e18).
				Attribute(model.KeyCoolant, 0).
				Attribute(model.KeyThrottle, 0).
				Attribute(model.KeyPower, 0).
				Attribute(model.KeyIgnition, true),
			check: func(t *testing.T, position *model.Position) {
				if !position.Outdated {
					t.Error("a serial frame carries no fix of its own")
				}
			},
		},
		{
			name:     "odometer beyond the 32 bit range",
			frame:    "&&l141,863911061945394,000,0,,230918072531,A,22.678598,114.045970,26,0.6,0,0,74,2286304571,460|0|249F|00001093,20,001C,00,00,04A7|019C|0000|0000,1,C0\r\n",
			deviceID: 3,
			expected: protocaltest.PositionWith(protocaltest.ChecksAttributes).
				Attribute(model.KeyOdometer, int64(2286304571)),
		},
		{
			name:     "engine hours",
			frame:    "&&s148,868703050178631,000,37,,230704040211,A,22.678565,114.046011,31,0.5,0,339,77,8,460|0|249F|0AC2620D,27,0000001D,02,00,04F2|01A1|0000|0000,129,,,,949037\r\n",
			deviceID: 4,
			expected: protocaltest.PositionWith(protocaltest.ChecksAttributes).
				Attribute(model.KeyHours, int64(9490000)),
		},
		{
			name:     "instant fuel consumption",
			frame:    "&&x164,869926040743375,000,0,,220705205955,A,33.326001,44.445318,10,1.2,0,57,8,925,418|40|038C|000083CD,31,00000015,00,00,0016|016A|0000|0000,1,,,686|33||44|99|14|124|11|8D\r\n",
			deviceID: 5,
			expected: protocaltest.Position().Attribute(model.KeyFuelConsumption, 1.1),
		},
		{
			name:     "full position",
			frame:    "&&R187,860294046453690,000,0,,220105160656,A,22.994986,72.499711,15,0.9,2,222,55,121135784,404|98|147B|0000376A,24,0000001F,02,00,052E|01A3|0000|0000,1,010000|020000,,853|6|10|105|73|41|125|34|52\r\n",
			deviceID: 6,
			expected: protocaltest.Position().
				Location("2022-01-05T16:06:56Z", true, 72.499711, 22.994986).
				Attribute(model.KeySatellites, 15).
				AttributeAbsent(model.KeyFuel),
			check: func(t *testing.T, position *model.Position) {
				if want := 2 / 1.852; math.Abs(position.Speed-want) > 1e-9 {
					t.Errorf("speed = %v, want %v", position.Speed, want)
				}
				if position.Course != 222 || position.Altitude != 55 {
					t.Errorf("course = %v, altitude = %v", position.Course, position.Altitude)
				}
				if position.Network == nil || len(position.Network.CellTowers) != 1 {
					t.Fatalf("network = %+v", position.Network)
				}
				tower := position.Network.CellTowers[0]
				if tower.MobileCountryCode != 404 || tower.MobileNetworkCode != 98 ||
					tower.LocationAreaCode != 0x147b || tower.CellID != 0x376a ||
					tower.SignalStrength != 24 {
					t.Errorf("cell tower = %+v", tower)
				}
			},
		},
		{
			name:     "no fix",
			frame:    "&&o142,860262050066062,000,27,,211111070826,V,28.653435,-106.077455,0,0.0,0,151,1412,918,0|0|4708|01402D19,6,0000001A,02,00,04C0|016C|0000|0000,1,,,BB\r\n",
			deviceID: 7,
			expected: protocaltest.PositionWith(protocaltest.ChecksAttributes).
				Attribute(model.KeyEvent, 27),
			check: func(t *testing.T, position *model.Position) {
				if position.Valid {
					t.Error("a V frame is not valid")
				}
			},
		},
		{
			name:     "status power and battery",
			frame:    "&&W149,865429043319537,000,0,,211103013512,A,22.679003,114.045085,16,1.1,0,271,76,109075,460|0|249F|000010C5,19,0000003E,00,00,0A57|0168|0000|0000,1,0100000C\r\n",
			deviceID: 8,
			expected: protocaltest.PositionWith(protocaltest.ChecksAttributes).
				Attribute(model.KeyStatus, 62).
				Attribute(model.KeyInput, 0).
				Attribute(model.KeyIgnition, false).
				Attribute(model.KeyDoor, false).
				Attribute(model.KeyPower, 26.47).
				Attribute(model.KeyBattery, 3.6),
		},
		{
			name:     "command result",
			frame:    "&&:23,860262050015424,129,OKA2\r\n",
			deviceID: 9,
			expected: protocaltest.PositionWith(protocaltest.ChecksAttributes).
				Attribute(model.KeyType, "129").
				Attribute(model.KeyResult, "OK"),
			check: func(t *testing.T, position *model.Position) {
				if !position.Outdated {
					t.Error("a command result carries no fix of its own")
				}
			},
		},
		{
			name:     "power cut alarm",
			frame:    "&&X152,861157040151686,000,18,,210907163833,A,10.232715,-67.880423,11,1.4,0,275,437,34804,734|2|3EE4|00579406,28,00000015,00,00,0000|017D|0000|0000,1,010000,,9A\r\n",
			deviceID: 10,
			expected: protocaltest.Position().Attribute(model.KeyAlarm, model.AlarmPowerCut),
		},
		{
			name:     "short frame without adc",
			frame:    "&&o125,861157040554384,000,0,,210702235150,A,27.263505,153.037061,11,1.2,0,0,31,5125,505|1|7032|8C89802,20,0000002D,00,00,01E2|019DF0\r\n",
			deviceID: 11,
			expected: protocaltest.PositionWith(protocaltest.ChecksAttributes).
				Attribute(model.KeyPower, 4.82).
				Attribute(model.KeyBattery, 4.13),
		},
		{
			name:     "driver id on an event",
			frame:    "&&a152,860262050010565,000,53,8F5300,210528015706,A,-38.229746,145.043446,6,1.5,0,285,84,2102994,505|1|306E|082D6101,31,0000003D,02,02,04C0|01A0|0000|0000,1,,DC\r\n",
			deviceID: 12,
			expected: protocaltest.PositionWith(protocaltest.ChecksAttributes).
				Attribute(model.KeyDriverID, "8F5300"),
			check: func(t *testing.T, position *model.Position) {
				if position.HasAttribute(model.KeyAlarm) {
					t.Error("event 53 carries a driver, not an alarm")
				}
			},
		},
		{
			name:     "no alarm for an unmapped event",
			frame:    "&&>141,860262050010565,000,36,,210407094323,V,-38.229711,145.043161,0,0.0,0,0,0,14222,505|1|306E|082D6115,24,00000039,00,00,04C0|0164|0000|0000,1,,41\r\n",
			deviceID: 12,
			expected: protocaltest.PositionWith(protocaltest.ChecksAttributes).
				Attribute(model.KeyEvent, 36),
		},
		{
			name:     "fuel temperature and adc blocks",
			frame:    "&&A147,021104023195429,000,0,,180106093046,A,22.646430,114.065730,8,0.9,54,86,76,326781,460|0|27B3|0EA7,27,0000000F,02,01,04E2|018C|01C8|0000,1,0104B0,01013D|02813546\r\n",
			deviceID: 13,
			expected: protocaltest.PositionWith(protocaltest.ChecksAttributes).
				Attribute("adc2", 4.56).
				Attribute("adc3", 0).
				Attribute("fuel1", 120).
				Attribute("temp1", 31.7).
				Attribute("temp2", -30.9),
		},
		{
			name:     "plain position",
			frame:    "&&y139,860262050009146,000,0,,210323131512,A,22.678655,114.046223,14,1.1,0,231,71,5,460|0|249F|0099C257,28,0000003D,00,00,0493|0199|0000|0000,1,,33\r\n",
			deviceID: 14,
			expected: protocaltest.PositionWith(protocaltest.ChecksAttributes).
				Attribute(model.KeySatellites, 14),
		},
	}

	decoder := newDecoder()
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := protocaltest.VerifyDecode(t, decoder, []byte(testCase.frame), testCase.expected)
			if got := result.Positions[0].DeviceID; got != testCase.deviceID {
				t.Fatalf("device id = %d, want %d", got, testCase.deviceID)
			}
			if testCase.check != nil {
				testCase.check(t, result.Positions[0])
			}
		})
	}
}

// The reference decoder never checks the checksum, so swapping the event field
// of a real fixture is enough to walk the whole alarm table.
func TestDecoderAlarmMapping(t *testing.T) {
	const frame = "&&R187,860294046453690,000,%d,,220105160656,A,22.994986,72.499711,15,0.9,2,222,55,121135784,404|98|147B|0000376A,24,0000001F,02,00,052E|01A3|0000|0000,1,010000|020000,,853|6|10|105|73|41|125|34|52\r\n"

	cases := []struct {
		event int
		alarm string
	}{
		{1, model.AlarmSOS},
		{5, model.AlarmDoor},
		{6, model.AlarmDoor},
		{17, model.AlarmLowPower},
		{18, model.AlarmPowerCut},
		{19, model.AlarmPowerRestored},
		{39, model.AlarmAcceleration},
		{40, model.AlarmBraking},
		{41, "cornering"},
		{36, ""},
	}

	decoder := newDecoder()
	for _, testCase := range cases {
		result, err := decoder.Decode([]byte(fmt.Sprintf(frame, testCase.event)))
		if err != nil {
			t.Fatalf("event %d: %v", testCase.event, err)
		}
		if len(result.Positions) != 1 {
			t.Fatalf("event %d: positions = %d", testCase.event, len(result.Positions))
		}
		alarm, ok := result.Positions[0].Get(model.KeyAlarm)
		if testCase.alarm == "" {
			if ok {
				t.Errorf("event %d: alarm = %v, want none", testCase.event, alarm)
			}
			continue
		}
		if alarm != testCase.alarm {
			t.Errorf("event %d: alarm = %v, want %v", testCase.event, alarm, testCase.alarm)
		}
	}
}

func TestDecoderRejectsMalformedFrames(t *testing.T) {
	decoder := newDecoder()

	for _, frame := range []string{
		"",
		"&&",
		"&&:23,860262050015424,129,OK",
		"$$:23,860262050015424,129,OKA2\r\n",
		"&&l141,863911061945394,000,0,,230918072531,A\r\n",
		"&&l141,000011112222333,000,0,,230918072531,A,22.678598,114.045970\r\n",
	} {
		result, err := decoder.Decode([]byte(frame))
		if err != nil {
			t.Fatalf("decode %q: %v", frame, err)
		}
		if len(result.Positions) != 0 {
			t.Fatalf("decode %q: positions = %d", frame, len(result.Positions))
		}
	}
}

func TestDecoderSurvivesShortSerialFrames(t *testing.T) {
	decoder := newDecoder()

	for _, frame := range []string{
		"&&\\546,865491061145970,710,T1,0.0,0.0,0.0,00\r\n",
		"&&\\546,865491061145970,710,T2,0.000,00\r\n",
		"&&\\546,865491061145970,710,T1,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,x,00\r\n",
	} {
		result, err := decoder.Decode([]byte(frame))
		if err != nil {
			t.Fatalf("decode %q: %v", frame, err)
		}
		if len(result.Positions) != 1 {
			t.Fatalf("decode %q: positions = %d", frame, len(result.Positions))
		}
		if len(result.Positions[0].Attributes) != 0 {
			t.Fatalf("decode %q: attributes = %v", frame, result.Positions[0].Attributes)
		}
	}
}

func TestFrameDecoderSplitsConcatenatedFrames(t *testing.T) {
	decoder := &startek.FrameDecoder{}
	first := []byte("&&:23,860262050015424,129,OKA2\r\n")
	second := []byte("&&o125,861157040554384,000,0,,210702235150,A,27.263505,153.037061,11,1.2,0,0,31,5125,505|1|7032|8C89802,20,0000002D,00,00,01E2|019DF0\r\n")

	data := append(append([]byte("garbage"), first...), second...)

	frame, consumed := decoder.DecodeFrame(data)
	if string(frame) != string(first) {
		t.Fatalf("frame = %q, want %q", frame, first)
	}
	if consumed != len("garbage")+len(first) {
		t.Fatalf("consumed = %d, want %d", consumed, len("garbage")+len(first))
	}

	frame, consumed = decoder.DecodeFrame(data[consumed:])
	if string(frame) != string(second) {
		t.Fatalf("frame = %q, want %q", frame, second)
	}
	if consumed != len(second) {
		t.Fatalf("consumed = %d, want %d", consumed, len(second))
	}
}

func TestFrameDecoderWaitsForAFullFrame(t *testing.T) {
	decoder := &startek.FrameDecoder{}
	frame := []byte("&&:23,860262050015424,129,OKA2\r\n")

	for length := 0; length < len(frame); length++ {
		if got, consumed := decoder.DecodeFrame(frame[:length]); got != nil || consumed != 0 {
			t.Fatalf("%d bytes: frame = %q, consumed = %d", length, got, consumed)
		}
	}

	if got, consumed := decoder.DecodeFrame(frame); string(got) != string(frame) || consumed != len(frame) {
		t.Fatalf("frame = %q, consumed = %d", got, consumed)
	}
}
