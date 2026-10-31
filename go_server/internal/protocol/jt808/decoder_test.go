package jt808

import (
	"encoding/binary"
	"encoding/hex"
	"math"
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

func TestFrameDecoder(t *testing.T) {
	decoder := NewFrameDecoder()

	got, consumed := decoder.DecodeFrame(mustHex(t, "7e307d02087d01557e"))
	if hex.EncodeToString(got) != "7e307e087d557e" || consumed != 9 {
		t.Fatalf("unescaped frame = %x, consumed = %d", got, consumed)
	}

	// Garbage before the delimiter is skipped.
	stream := mustHex(t, "24247e307d02087d01557e")
	got, consumed = decoder.DecodeFrame(stream)
	if hex.EncodeToString(got) != "7e307e087d557e" || consumed != 11 {
		t.Fatalf("resynced frame = %x, consumed = %d", got, consumed)
	}

	// A frame without its closing delimiter must wait for more data.
	if got, consumed = decoder.DecodeFrame(mustHex(t, "7e307d02087d01")); got != nil || consumed != 0 {
		t.Fatalf("partial frame = %x, consumed = %d", got, consumed)
	}
}

func TestFrameDecoderAsciiSentence(t *testing.T) {
	sentence := "(740190311185,1,001,ELOCK,3,529439)"
	got, consumed := NewFrameDecoder().DecodeFrame([]byte(sentence))
	if string(got) != sentence || consumed != len(sentence) {
		t.Fatalf("ascii frame = %q, consumed = %d", got, consumed)
	}
}

func TestChecksum(t *testing.T) {
	frame := mustHex(t, "7e0200004b071604684030004d00000000000c0006005e56a5065c573400330000005226082717271030011b31010de11c01fe0059007e96033ce517c5007e96033ce55dc7007e96033ce549c8e4020064f002034af50101a47e")

	want := frame[len(frame)-2]
	if got := helper.XOR(frame[1 : len(frame)-2]); got != want {
		t.Errorf("checksum = %02x, want %02x", got, want)
	}
}

func TestDecodeLocationReport(t *testing.T) {
	frame := mustHex(t, "7e0200004b071604684030004d00000000000c0006005e56a5065c573400330000005226082717271030011b31010de11c01fe0059007e96033ce517c5007e96033ce55dc7007e96033ce549c8e4020064f002034af50101a47e")

	decoder := NewDecoder(Base())
	// The resolver accepts any identifier, so the session comes from the frame.
	result, err := decoder.Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}

	position := result.Positions[0]
	if position.DeviceID != 1 {
		t.Errorf("device id = %d, want 1", position.DeviceID)
	}
	if !position.Valid {
		t.Error("valid = false, want true")
	}
	if math.Abs(position.Latitude-(-6.182565)) > 1e-6 {
		t.Errorf("latitude = %.6f, want -6.182565", position.Latitude)
	}
	if math.Abs(position.Longitude-106.714932) > 1e-6 {
		t.Errorf("longitude = %.6f, want 106.714932", position.Longitude)
	}
	if position.Altitude != 51 || position.Course != 82 {
		t.Errorf("altitude = %.0f, course = %.0f, want 51/82", position.Altitude, position.Course)
	}

	// 2026-08-27T17:27:10 in the default GMT+8 device time zone.
	want := time.Date(2026, 8, 27, 9, 27, 10, 0, time.UTC)
	if !position.Time.Equal(want) {
		t.Errorf("time = %s, want %s", position.Time.UTC(), want)
	}

	for key, wantValue := range map[string]any{
		model.KeySatellites: 13,
		model.KeyRSSI:       27,
	} {
		if got, _ := position.Get(key); got != wantValue {
			t.Errorf("attribute %q = %#v, want %#v", key, got, wantValue)
		}
	}

	if len(result.Responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(result.Responses))
	}
	response := result.Responses[0]
	if response[0] != 0x7e || response[len(response)-1] != 0x7e {
		t.Fatalf("response not framed: %x", response)
	}
	body, _ := NewFrameDecoder().DecodeFrame(response)
	if body[1] != 0x80 || body[2] != 0x01 {
		t.Errorf("response type = %x, want 8001", body[1:3])
	}
	payload := body[1+2+2+6+2 : len(body)-2]
	if binary.BigEndian.Uint16(payload) != 0x004d ||
		binary.BigEndian.Uint16(payload[2:]) != msgLocationReport ||
		payload[4] != msgResultSuccess {
		t.Errorf("response payload = %x", payload)
	}
}

func TestDecodeRegister2019(t *testing.T) {
	frame := mustHex(t, "7e0100405c010000086649607758216452180000000000000000000000000000004d443330300000000000000000000000000000000000000000000000000038363634393630373735383231363400000000000000000000000000000004000000000000000000000000000000007a7e")

	result, err := NewDecoder(Base()).Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.UniqueID != "00000866496077582164" {
		t.Errorf("unique id = %q", result.UniqueID)
	}
	if len(result.Positions) != 0 {
		t.Errorf("positions = %d, want 0", len(result.Positions))
	}
	if len(result.Responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(result.Responses))
	}

	body, _ := NewFrameDecoder().DecodeFrame(result.Responses[0])
	if body[1] != 0x81 || body[2] != 0x00 {
		t.Fatalf("response type = %x, want 8100", body[1:3])
	}
	payload := body[1+2+2+1+10+2 : len(body)-2]
	if payload[2] != msgResultSuccess {
		t.Errorf("register result = %02x, want 00", payload[2])
	}
	if string(payload[3:]) != "00000866496077582164" {
		t.Errorf("register id = %q", payload[3:])
	}
}

func TestDecodeLocationExtensions(t *testing.T) {
	id, err := encodeID("123456789012345", 6)
	if err != nil {
		t.Fatalf("encodeID: %v", err)
	}
	h := &header{delimiter: 0x7e, id: id}

	body := make([]byte, 0, 64)
	body = binary.BigEndian.AppendUint32(body, 0)          // alarm
	body = binary.BigEndian.AppendUint32(body, 0x00000002) // valid fix
	body = binary.BigEndian.AppendUint32(body, 54945350)   // latitude
	body = binary.BigEndian.AppendUint32(body, 24017620)   // longitude
	body = binary.BigEndian.AppendUint16(body, 120)        // altitude
	body = binary.BigEndian.AppendUint16(body, 500)        // speed, 50.0 km/h
	body = binary.BigEndian.AppendUint16(body, 90)         // course
	body = append(body, 0x26, 0x10, 0x31, 0x13, 0x02, 0x10)
	body = append(body, 0x01, 0x04)
	body = binary.BigEndian.AppendUint32(body, 12345) // odometer
	body = append(body, 0x06, 0x01, 90)               // battery level
	body = append(body, 0x56, 0x02, 88, 0x00)         // battery level * 10
	body = append(body, 0x31, 0x01, 9)                // satellites
	body = append(body, 0x30, 0x01, 21)               // rssi
	body = append(body, 0xBD, 0x08)
	body = append(body, []byte("12345678")...) // driver id

	frame := buildMessage(h, msgLocationReport, body)

	result, err := NewDecoder(Base()).Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}

	position := result.Positions[0]
	checks := map[string]any{
		model.KeyOdometer:     uint32(12345 * 100),
		model.KeyBatteryLevel: 880,
		model.KeySatellites:   9,
		model.KeyRSSI:         21,
		model.KeyDriverID:     "12345678",
	}
	for key, want := range checks {
		if got, _ := position.Get(key); got != want {
			t.Errorf("attribute %q = %#v, want %#v", key, got, want)
		}
	}
	if math.Abs(position.Speed-helper.KnotsFromKph(50)) > 1e-9 {
		t.Errorf("speed = %.6f", position.Speed)
	}
	if position.Time.UTC() != time.Date(2026, 10, 31, 5, 2, 10, 0, time.UTC) {
		t.Errorf("time = %s", position.Time.UTC())
	}
}

func TestEncodePositionPeriodic(t *testing.T) {
	encoder := NewEncoder(codec.Base{UniqueIDs: NewStubUniqueIDs()})

	command := model.NewCommand(1, model.CommandPositionPeriodic)
	command.SetAttribute(model.CmdKeyFrequency, 60)

	frame, err := encoder.EncodeCommand(command)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if frame[0] != 0x7e || frame[len(frame)-1] != 0x7e {
		t.Fatalf("frame not delimited: %x", frame)
	}

	body, _ := NewFrameDecoder().DecodeFrame(frame)
	if binary.BigEndian.Uint16(body[1:]) != msgParameterSetting {
		t.Errorf("type = %x, want 0310", body[1:3])
	}
	if got := helper.XOR(body[1 : len(body)-2]); got != body[len(body)-2] {
		t.Errorf("checksum = %02x, want %02x", got, body[len(body)-2])
	}
	payload := body[1+2+2+6+2 : len(body)-2]
	want := []byte{1, 0x06, 4, 0, 0, 0, 60}
	if hex.EncodeToString(payload) != hex.EncodeToString(want) {
		t.Errorf("payload = %x, want %x", payload, want)
	}
}

// TestDecodeAlarmModels pins the padlock, AOVX and VL300 alarm branches. The
// generic bits mean something else on those devices.
func TestDecodeAlarmModels(t *testing.T) {
	decoder := NewDecoder(Base())

	for _, tc := range []struct {
		name  string
		value uint32
		model string
		want  []string
	}{
		{"sos", 1 << 0, "", []string{model.AlarmSOS}},
		{"padlock removing", 1 << 4, "G-360P", []string{model.AlarmRemoving}},
		{"padlock tampering", 1 << 1, "G-508P", []string{model.AlarmTampering}},
		{"padlock bit0 is removing", 1 << 0, "G-360P", []string{model.AlarmRemoving}},
		{"aovx movement", 1 << 16, "AL300", []string{model.AlarmMovement}},
		{"aovx ignores overspeed", 1 << 1, "GL100", nil},
		{"generic accident", 1 << 29, "", []string{model.AlarmAccident}},
		{"vl300 drops accident", 1 << 29, "VL300", nil},
		{"lower case model", 1 << 16, "al300", []string{model.AlarmMovement}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			position := model.NewPosition(ProtocolName)
			decoder.decodeAlarm(position, tc.value, tc.model)

			if len(position.Alarms) != len(tc.want) {
				t.Fatalf("alarms = %v, want %v", position.Alarms, tc.want)
			}
			for i, want := range tc.want {
				if position.Alarms[i] != want {
					t.Errorf("alarm %d = %q, want %q", i, position.Alarms[i], want)
				}
			}
		})
	}
}
