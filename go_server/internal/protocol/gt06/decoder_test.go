package gt06

import (
	"encoding/hex"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
	. "traccar-server_go/go_server/internal/protocol/protocaltest"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	data, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("invalid hex %q: %v", s, err)
	}
	return data
}

func testSession() *codec.DeviceSession {
	return &codec.DeviceSession{DeviceID: 1, UniqueID: "123456789012345"}
}

// TestChecksum uses frames recorded from real devices, so it validates the
// CRC16/X25 parameters against the Java implementation.
func TestChecksum(t *testing.T) {
	for _, tc := range []struct {
		frame string
		want  int
	}{
		{"78780d01086471700328358100093f040d0a", 0x3f04},
		{"7878058a000688290d0a", 0x8829},
	} {
		frame := mustHex(t, tc.frame)
		if got := helper.CRC16X25(frame[2 : len(frame)-4]); got != tc.want {
			t.Errorf("crc of %s = %04x, want %04x", tc.frame, got, tc.want)
		}
	}
}

func TestDecodeLogin(t *testing.T) {
	frame := mustHex(t, "78780d01086471700328358100093f040d0a")
	result, err := NewDecoder(Base()).Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.UniqueID != "864717003283581" {
		t.Errorf("unique id = %q, want %q", result.UniqueID, "864717003283581")
	}
	if len(result.Positions) != 0 {
		t.Errorf("login produced %d positions, want 0", len(result.Positions))
	}
	if len(result.Responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(result.Responses))
	}

	ack := result.Responses[0]
	if len(ack) != 10 || ack[0] != 0x78 || ack[1] != 0x78 || ack[2] != 0x05 || ack[3] != msgLogin {
		t.Fatalf("unexpected ack %x", ack)
	}
	if serial := int(ack[4])<<8 | int(ack[5]); serial != 9 {
		t.Errorf("ack serial = %d, want 9", serial)
	}
	if crc := int(ack[6])<<8 | int(ack[7]); crc != helper.CRC16X25(ack[2:6]) {
		t.Errorf("ack crc = %04x, want %04x", crc, helper.CRC16X25(ack[2:6]))
	}
	if ack[8] != 0x0d || ack[9] != 0x0a {
		t.Errorf("ack footer = %x", ack[8:])
	}
}

func TestDecodeGPS(t *testing.T) {
	frame := mustHex(t, "78781f120f0a140e150bc505e51e780293a9e800540000f601006e0055da00035f240d0a")

	result, err := NewDecoder(Base()).DecodeWithSession(frame, testSession())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}

	position := result.Positions[0]
	want := time.Date(2015, 10, 20, 14, 21, 11, 0, time.UTC)
	if !position.Time.Equal(want) {
		t.Errorf("time = %s, want %s", position.Time.UTC(), want)
	}
	if !position.Valid {
		t.Error("valid = false, want true")
	}
	if diff := position.Latitude - 54.94535; diff > 1e-5 || diff < -1e-5 {
		t.Errorf("latitude = %.6f, want 54.945350", position.Latitude)
	}
	if diff := position.Longitude - 24.01762; diff > 1e-5 || diff < -1e-5 {
		t.Errorf("longitude = %.6f, want 24.017620", position.Longitude)
	}
	if position.Speed != 0 || position.Course != 0 {
		t.Errorf("speed = %.2f, course = %.2f, want 0/0", position.Speed, position.Course)
	}
	if satellites, _ := position.Get(model.KeySatellites); satellites != 5 {
		t.Errorf("satellites = %v, want 5", satellites)
	}
	if ignition, ok := position.Get(model.KeyIgnition); !ok || ignition != false {
		t.Errorf("ignition = %v, want false", ignition)
	}
	if messageType, _ := position.Get(model.KeyType); messageType != msgGPSLBS1 {
		t.Errorf("type = %v, want %#x", messageType, msgGPSLBS1)
	}
	if len(result.Responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(result.Responses))
	}
	if serial := deviceSerial(result.Responses[0]); serial != 3 {
		t.Errorf("ack serial = %d, want 3", serial)
	}
}

func TestDecodeRequiresSession(t *testing.T) {
	frame := mustHex(t, "78781f120f0a140e150bc505e51e780293a9e800540000f601006e0055da00035f240d0a")
	result, err := NewDecoder(Base()).Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 0 || len(result.Responses) != 0 {
		t.Fatalf("expected empty result without a session, got %d positions", len(result.Positions))
	}
}

func TestDecodeTimeRequest(t *testing.T) {
	decoder := NewDecoder(Base())
	decoder.Now = func() time.Time { return time.Date(2026, 10, 31, 13, 2, 10, 0, time.UTC) }

	result, err := decoder.DecodeWithSession(mustHex(t, "7878058a000688290d0a"), testSession())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(result.Responses))
	}

	want := []byte{0x78, 0x78, 0x0B, msgTimeRequest, 26, 10, 31, 13, 2, 10, 0, 0}
	got := result.Responses[0]
	if len(got) != len(want)+4 {
		t.Fatalf("response length = %d, want %d", len(got), len(want)+4)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("response[%d] = %#x, want %#x", i, got[i], want[i])
		}
	}
}

func TestFrameDecoder(t *testing.T) {
	decoder := NewFrameDecoder()

	first := "78780d01086471700328358100093f040d0a"
	second := "78781f120f0a140e150bc505e51e780293a9e800540000f601006e0055da00035f240d0a"
	stream := mustHex(t, "00ff"+first+second)

	frame, consumed := decoder.DecodeFrame(stream)
	if hex.EncodeToString(frame) != first || consumed != 2+len(first)/2 {
		t.Fatalf("first frame = %x, consumed = %d", frame, consumed)
	}

	offset := consumed
	frame, consumed = decoder.DecodeFrame(stream[offset:])
	if hex.EncodeToString(frame) != second || consumed != len(second)/2 {
		t.Fatalf("second frame = %x, consumed = %d", frame, consumed)
	}

	offset += consumed
	if frame, consumed := decoder.DecodeFrame(stream[offset:]); frame != nil || consumed != 0 {
		t.Fatalf("trailing data yielded %x, consumed = %d", frame, consumed)
	}
}

func TestFrameDecoderExtended(t *testing.T) {
	frame := mustHex(t, "79790011941501282c533a12230ed309f63c8408720d0a")
	got, consumed := NewFrameDecoder().DecodeFrame(frame)
	if hex.EncodeToString(got) != hex.EncodeToString(frame) || consumed != len(frame) {
		t.Fatalf("extended frame = %x, consumed = %d", got, consumed)
	}
}

// TestDecodeAlarmModels pins the branches Gt06ProtocolDecoder switches on. The
// same alarm byte means a different thing on an LW, SEEWORLD or VL device.
func TestDecodeAlarmModels(t *testing.T) {
	decoder := NewDecoder(Base())

	for _, tc := range []struct {
		value int
		model string
		want  string
	}{
		{0x09, "", model.AlarmVibration},
		{0x09, "VL103", model.AlarmTow},
		{0x18, "", model.AlarmRemoving},
		{0x18, "LW12", model.AlarmAccident},
		{0x19, "", model.AlarmLowBattery},
		{0x19, "lw12", model.AlarmHardAcceleration},
		{0x28, "", model.AlarmHardBraking},
		{0x28, "SEEWORLD", model.AlarmHardCornering},
		{0x28, "LL303", model.AlarmPowerOff},
		{0x29, "", model.AlarmHardAcceleration},
		{0x29, "seeworld", model.AlarmAccident},
		{0x30, "", model.AlarmJamming},
		{0x30, "G18", model.AlarmHardBraking},
		{0x0C, "VL103", model.AlarmTampering},
	} {
		if got := decoder.decodeAlarm(tc.value, modelFlagsOf(tc.model)); got != tc.want {
			t.Errorf("alarm %#02x on model %q = %q, want %q", tc.value, tc.model, got, tc.want)
		}
	}
}

// TestDecodePowerUsesModelFlags proves the model reaches the alarm byte
// through the decode path, not only the mapping function.
func TestDecodePowerUsesModelFlags(t *testing.T) {
	decoder := NewDecoder(Base())

	for _, tc := range []struct {
		model string
		want  string
	}{
		{"", model.AlarmVibration},
		{"VL512", model.AlarmTow},
	} {
		position := model.NewPosition(ProtocolName)
		buf := helper.NewBuffer([]byte{0x64, 0x1f, 0x09})

		decoder.decodePower(position, buf, modelFlagsOf(tc.model))

		if len(position.Alarms) != 1 || position.Alarms[0] != tc.want {
			t.Errorf("model %q alarms = %v, want [%s]", tc.model, position.Alarms, tc.want)
		}
	}
}

// commandReply builds the 0x80 frame a device sends back after executing a
// downlink command: length byte, ASCII content, two byte language field.
func commandReply(t *testing.T, messageType int, content string) []byte {
	t.Helper()
	data := append([]byte{byte(len(content) + 3)}, content...)
	data = append(data, 0x00, 0x02) // english

	frame := []byte{0x78, 0x78, byte(len(data) + 5), byte(messageType)}
	frame = append(frame, data...)
	frame = append(frame, 0x00, 0x01) // message serial
	crc := helper.CRC16X25(frame[2:])
	return append(frame, byte(crc>>8), byte(crc), 0x0d, 0x0a)
}

func TestDecodeCommandReply(t *testing.T) {
	for _, messageType := range []int{msgCommand0, msgCommand1, msgCommand2} {
		frame := commandReply(t, messageType, "Relay,1#")
		result, err := NewDecoder(Base()).DecodeWithSession(frame, testSession())
		if err != nil {
			t.Fatalf("decode %#x: %v", messageType, err)
		}
		if result.CommandAck == nil {
			t.Fatalf("%#x: no command ack", messageType)
		}
		if result.CommandAck.Code != 0 {
			t.Errorf("%#x: code = %d, want 0", messageType, result.CommandAck.Code)
		}
		if len(result.Responses) != 0 {
			t.Errorf("%#x: responses = %d, want 0", messageType, len(result.Responses))
		}
		if len(result.Positions) != 0 {
			t.Errorf("%#x: positions = %d, want 0", messageType, len(result.Positions))
		}
	}
}

// ntSession is a session whose device row carries a model. The NT layout is
// selected by that model, so a test that wants the branch has to name one.
func ntSession(model string) *codec.DeviceSession {
	session := testSession()
	session.Model = model
	return session
}

// decodeNT runs one frame through a decoder whose session carries the model.
func decodeNT(t *testing.T, model, frame string) *model.Position {
	t.Helper()
	result, err := NewDecoder(Base()).DecodeWithSession(mustHex(t, frame), ntSession(model))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}
	return result.Positions[0]
}

// ntLocation is MSG_GPS_LBS_2 from an NT20; the same bytes appear in the Java
// suite under NT20 and again under XT40, where they differ only in which
// attribute the expectation names.
const ntLocation = "78783c22010357789648774484180c06142038180c06142038cb03313ee4061fe98300185e" +
	"090000000000000000460984284c000200000000017101d6f35d0d0a"

// TestDecodeNTLocation pins the layout an NT model uses for MSG_GPS_LBS_2: a
// location prefix, the ordinary location block, a status block with the
// voltage in front, then odometer and engine hours. The Java suite reads the
// same frame with and without the model and only the model branch finds the
// hours.
func TestDecodeNTLocation(t *testing.T) {
	position := decodeNT(t, "NT20", ntLocation)

	// The device clock is 2024-12-06 20:32:56 and the flag word reports the fix
	// as south and west, so the coordinates come out negative.
	want := time.Date(2024, 12, 6, 20, 32, 56, 0, time.UTC)
	if !position.Time.Equal(want) {
		t.Errorf("time = %s, want %s", position.Time.UTC(), want)
	}
	if diff := position.Latitude + 29.75500667; diff > 1e-5 || diff < -1e-5 {
		t.Errorf("latitude = %.8f", position.Latitude)
	}
	if diff := position.Longitude + 57.08593944; diff > 1e-5 || diff < -1e-5 {
		t.Errorf("longitude = %.8f", position.Longitude)
	}
	if satellites, _ := position.Get(model.KeySatellites); satellites != 11 {
		t.Errorf("satellites = %v, want 11", satellites)
	}

	checks := map[string]any{
		model.KeyHours:        int64(22140000),
		model.KeyOdometer:     0,
		model.KeyPower:        24.36,
		model.KeyBattery:      4.0,
		model.KeyRSSI:         76,
		model.KeyBatteryLevel: nil,
	}
	for key, want := range checks {
		got, ok := position.Get(key)
		if want == nil {
			if ok {
				t.Errorf("attribute %q = %v, want the NT branch not to set it", key, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("attribute %q = %#v, want %#v", key, got, want)
		}
	}
}

// TestDecodeNTLocationIsModelDriven decodes the same bytes without an NT model.
// Java keeps hasStatus false there, so no status block, no voltage and no
// hours: the frame is a plain GPS plus cell tower report.
func TestDecodeNTLocationIsModelDriven(t *testing.T) {
	position := decodeNT(t, "GT06N", ntLocation)

	for _, key := range []string{
		model.KeyHours, model.KeyOdometer, model.KeyPower, model.KeyBattery,
	} {
		if got, ok := position.Get(key); ok {
			t.Errorf("attribute %q = %v, want none without an NT model", key, got)
		}
	}
	if position.Time.IsZero() {
		t.Error("time is zero: the frame still has to produce a position")
	}
}

// TestDecodeNTLocationXT40 is the second NT vector in the Java suite: the
// voltage of a device that reports 12.51 V while moving.
func TestDecodeNTLocationXT40(t *testing.T) {
	frame := "78783c220109999999999999991a04171433101a041714310fb00288a5a8050754500008000800" +
		"000000000000000604e32933000200468b000025008d87ce0d0a"
	position := decodeNT(t, "XT40", frame)

	checks := map[string]any{
		model.KeyPower:    12.51,
		model.KeyBattery:  4.1,
		model.KeyRSSI:     51,
		model.KeyOdometer: 18059,
		model.KeyHours:    int64(2220000),
	}
	for key, want := range checks {
		if got, ok := position.Get(key); !ok || got != want {
			t.Errorf("attribute %q = %#v, want %#v", key, got, want)
		}
	}
	if position.Valid {
		t.Error("valid = true, want false: the vector carries no fix")
	}
}

// TestDecodeNTDriver covers MSG_GPS_LBS_DRIVER from an XT40, where the cell id
// is four bytes wide and the frame ends with the driver card.
func TestDecodeNTDriver(t *testing.T) {
	frame := "78784e25010356354871186947190909100e0f190909100e0fbf02846fe805078c900018000a" +
		"02d410c80b0c352c17044605112a4700020000000005840f0102353130303635383036424446" +
		"030113ed780d0a"
	position := decodeNT(t, "XT40", frame)

	checks := map[string]any{
		model.KeyDriverID: "0235313030363538303642444603",
		model.KeyPower:    12.97,
		model.KeyBattery:  4.2,
		model.KeyRSSI:     71,
		model.KeyHours:    int64(84720000),
	}
	for key, want := range checks {
		if got, ok := position.Get(key); !ok || got != want {
			t.Errorf("attribute %q = %#v, want %#v", key, got, want)
		}
	}

	// The cell id of this message type is four bytes wide. A three byte read
	// would shift every field after it, so the tower is the cheap proof.
	if position.Network == nil || len(position.Network.CellTowers) != 1 {
		t.Fatalf("network = %+v, want one cell tower", position.Network)
	}
	tower := position.Network.CellTowers[0]
	if tower.MobileCountryCode != 724 || tower.MobileNetworkCode != 16 ||
		tower.LocationAreaCode != 0xc80b || tower.CellID != 0x0c352c17 {
		t.Errorf("cell tower = %+v, want 724/16/c80b/0c352c17", tower)
	}
}
