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

// buildSubPacketFrame builds a JT808 frame with the sub-packet (分包) flag set
// in the message attribute field (0x2000). The body is prefixed with the four
// byte sub-packet header: total packet count then the packet sequence number.
func buildSubPacketFrame(t *testing.T, messageType int, total, sequence int, body []byte) []byte {
	t.Helper()
	id, err := encodeID("123456789012345", 6)
	if err != nil {
		t.Fatalf("encodeID: %v", err)
	}
	h := &header{delimiter: 0x7e, id: id}

	packet := make([]byte, 0, len(body)+4)
	packet = binary.BigEndian.AppendUint16(packet, uint16(total))
	packet = binary.BigEndian.AppendUint16(packet, uint16(sequence))
	packet = append(packet, body...)

	attribute := len(packet) | 0x2000
	raw := make([]byte, 0, 2+2+6+2+len(packet)+2)
	raw = append(raw, byte(messageType>>8), byte(messageType),
		byte(attribute>>8), byte(attribute))
	raw = append(raw, h.id...)
	raw = append(raw, 0, 1) // serial
	raw = append(raw, packet...)
	raw = append(raw, helper.XOR(raw))

	frame := []byte{0x7e}
	for _, b := range raw {
		switch b {
		case 0x7d:
			frame = append(frame, 0x7d, 0x01)
		case 0x7e:
			frame = append(frame, 0x7d, 0x02)
		default:
			frame = append(frame, b)
		}
	}
	return append(frame, 0x7e)
}

// TestSubPacketNotMisparsed guards against a real gap found in review: a
// location report (0x0200) that carries the sub-packet flag (0x2000) is a
// fragment of a larger transmission, not a complete message. Before the fix the
// body was parsed from the sub-packet header, producing a garbage position and
// a spurious acknowledgement.
func TestSubPacketNotMisparsed(t *testing.T) {
	decoder := NewDecoder(Base())

	// A plausible 0x0200 body split across two packets. Each fragment is fed
	// alone; neither is a complete location report.
	body := make([]byte, 0, 32)
	body = binary.BigEndian.AppendUint32(body, 0)          // alarm
	body = binary.BigEndian.AppendUint32(body, 0x00000002) // valid fix
	body = binary.BigEndian.AppendUint32(body, 54945350)   // latitude
	body = binary.BigEndian.AppendUint32(body, 24017620)   // longitude
	body = binary.BigEndian.AppendUint16(body, 120)        // altitude
	body = binary.BigEndian.AppendUint16(body, 500)        // speed, 50.0 km/h
	body = binary.BigEndian.AppendUint16(body, 90)         // course
	body = append(body, 0x26, 0x10, 0x31, 0x13, 0x02, 0x10)

	// Packet 1 of 2 carries the first fragment, packet 2 the second.
	for _, frame := range [][]byte{
		buildSubPacketFrame(t, msgLocationReport, 2, 1, body[:16]),
		buildSubPacketFrame(t, msgLocationReport, 2, 2, body[16:]),
	} {
		result, err := decoder.Decode(frame)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(result.Positions) != 0 {
			t.Fatalf("sub-packet parsed as complete message: positions = %d", len(result.Positions))
		}
		if len(result.Responses) != 0 {
			t.Fatalf("sub-packet answered: responses = %d", len(result.Responses))
		}
	}
}

// TestEncodeCommandCarriesItsOwnSerial pins the correlation rule: every
// downlink frame gets the next serial, the command remembers it, and a second
// command cannot reuse it.
func TestEncodeCommandCarriesItsOwnSerial(t *testing.T) {
	encoder := NewEncoder(codec.Base{
		UniqueIDs: NewStubUniqueIDs(),
		Serials:   codec.NewSerialSource(),
	})

	first := model.NewCommand(1, model.CommandEngineStop)
	firstSerial := 1
	frame, err := encoder.EncodeCommand(first)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if first.Serial != firstSerial {
		t.Fatalf("serial = %d, want %d", first.Serial, firstSerial)
	}
	body, _ := NewFrameDecoder().DecodeFrame(frame)
	if serial := int(binary.BigEndian.Uint16(body[1+2+2+6:])); serial != firstSerial {
		t.Errorf("frame serial = %d, want %d", serial, firstSerial)
	}

	second := model.NewCommand(1, model.CommandEngineResume)
	frame, err = encoder.EncodeCommand(second)
	if err != nil {
		t.Fatalf("encode second: %v", err)
	}
	if second.Serial != firstSerial+1 {
		t.Fatalf("second serial = %d, want %d", second.Serial, firstSerial+1)
	}
	body, _ = NewFrameDecoder().DecodeFrame(frame)
	if serial := int(binary.BigEndian.Uint16(body[1+2+2+6:])); serial != second.Serial {
		t.Errorf("frame serial = %d, want %d", serial, second.Serial)
	}

	// A command that already carries a serial keeps it, which is what lets a
	// recovered command match the answer of the frame it was queued for.
	recovered := model.NewCommand(1, model.CommandEngineStop)
	recovered.Serial = 4242
	if _, err := encoder.EncodeCommand(recovered); err != nil {
		t.Fatalf("encode recovered: %v", err)
	}
	if recovered.Serial != 4242 {
		t.Errorf("recovered serial = %d, want 4242", recovered.Serial)
	}

	// A raw custom payload is already a whole frame, so it is not numbered.
	custom := model.NewCommand(1, model.CommandCustom)
	custom.SetAttribute(model.CmdKeyData, "7e0200000001")
	if _, err := encoder.EncodeCommand(custom); err != nil {
		t.Fatalf("encode custom: %v", err)
	}
	if custom.Serial != 0 {
		t.Errorf("custom serial = %d, want 0: the caller owns that frame", custom.Serial)
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

// uplinkFrame builds a frame the way the gateway sees it: escaped by
// buildMessage and expanded again by the frame decoder.
func uplinkFrame(t *testing.T, messageType int, payload []byte) []byte {
	t.Helper()
	raw := buildMessage(
		&header{delimiter: 0x7e, id: mustHex(t, "071604684030")}, messageType, payload)
	frame, consumed := NewFrameDecoder().DecodeFrame(raw)
	if frame == nil || consumed != len(raw) {
		t.Fatalf("frame decoder returned %x, consumed %d of %d", frame, consumed, len(raw))
	}
	return frame
}

// TestDecodeTerminalGeneralResponse covers the answer a terminal sends once it
// has acted on a downlink command.
func TestDecodeTerminalGeneralResponse(t *testing.T) {
	decoder := NewDecoder(Base())

	cases := []struct {
		name   string
		result byte
		code   int
	}{
		{"accepted", 0x00, 0},
		{"refused", 0x01, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// answered serial, answered message id (0x8105 terminal control),
			// result
			frame := uplinkFrame(t, msgTerminalGeneralResponse,
				[]byte{0x00, 0x07, 0x81, 0x05, tc.result})

			result, err := decoder.Decode(frame)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if result.CommandAck == nil {
				t.Fatalf("no command ack (consumed = %v)", result.Consumed)
			}
			if result.CommandAck.Code != tc.code {
				t.Errorf("code = %d, want %d", result.CommandAck.Code, tc.code)
			}
			if result.CommandAck.Serial == nil || *result.CommandAck.Serial != 7 {
				t.Errorf("serial = %v, want the answered serial 7", result.CommandAck.Serial)
			}
			if len(result.Positions) != 0 {
				t.Errorf("positions = %d, want 0", len(result.Positions))
			}
			if len(result.Responses) != 0 {
				t.Errorf("responses = %d, want 0", len(result.Responses))
			}
		})
	}
}

func TestDecodeShortTerminalGeneralResponse(t *testing.T) {
	frame := uplinkFrame(t, msgTerminalGeneralResponse, []byte{0x00, 0x07})

	result, err := NewDecoder(Base()).Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.CommandAck != nil {
		t.Fatalf("ack = %+v, want none", result.CommandAck)
	}
	if result.Consumed {
		t.Error("a truncated general response was consumed")
	}
}

// TestDecodeLocationBatch0704 pins the 0x0704 framing: an item count, a report
// type that marks blind area uploads as archived, then a length prefixed 0x0200
// body per item. The platform acknowledges the batch with a general response.
func TestDecodeLocationBatch0704(t *testing.T) {
	frame := mustHex(t, "7e0704003e0184380000500084000101003900000000000c000f01d792b7034f72e600c90000009b26101319020901040000000030011f31010ad00400000000d40164d7030100beda0105e17e")

	result, err := NewDecoder(Base()).Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}

	position := result.Positions[0]
	if archived, _ := position.Get(model.KeyArchive); archived != true {
		t.Errorf("archive = %#v, want true for a blind area batch", archived)
	}
	if got, _ := position.Get(model.KeySatellites); got != 10 {
		t.Errorf("satellites = %#v, want 10", got)
	}
	if got, _ := position.Get(model.KeyRSSI); got != 31 {
		t.Errorf("rssi = %#v, want 31", got)
	}
	if math.Abs(position.Latitude-(-30.905015)) > 1e-6 || math.Abs(position.Longitude-(-55.538406)) > 1e-6 {
		t.Errorf("coordinates = %.6f/%.6f", position.Latitude, position.Longitude)
	}
	if position.Altitude != 201 || position.Course != 155 {
		t.Errorf("altitude = %.0f, course = %.0f, want 201/155", position.Altitude, position.Course)
	}
	if want := time.Date(2026, 10, 13, 11, 2, 9, 0, time.UTC); !position.Time.Equal(want) {
		t.Errorf("time = %s, want %s", position.Time.UTC(), want)
	}

	if len(result.Responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(result.Responses))
	}
	body, _ := NewFrameDecoder().DecodeFrame(result.Responses[0])
	if body[1] != 0x80 || body[2] != 0x01 {
		t.Errorf("response type = %x, want 8001", body[1:3])
	}
	if got, want := helper.XOR(body[1:len(body)-2]), body[len(body)-2]; got != want {
		t.Errorf("response checksum = %02x, want %02x", got, want)
	}
	payload := body[1+2+2+6+2 : len(body)-2]
	if binary.BigEndian.Uint16(payload) != 0x0084 ||
		binary.BigEndian.Uint16(payload[2:]) != msgLocationBatch ||
		payload[4] != msgResultSuccess {
		t.Errorf("response payload = %x", payload)
	}
}

// TestDecodeLocationBatchMultiple checks that every item is decoded from its
// own length delimited fragment, so extensions and times cannot bleed into the
// next position.
func TestDecodeLocationBatchMultiple(t *testing.T) {
	frame := mustHex(t, "7e070401654eb6fb4afd9e485b000601003900000000000c000301f5a58700ca24c5000d0000004925111215454501040000000025040000000030011731010aeb095554432b30383a3030003900000000000c000301f5a58e00ca24cd000d0000002825111215454701040000000025040000000030011731010aeb095554432b30383a3030003900000000000c000301f5a5ad00ca24de000e0000011025111215455901040000000025040000000030011931010aeb095554432b30383a3030003900000000000c000301f5a5b100ca24ed000e0000004625111215461401040000000025040000000030011931010aeb095554432b30383a3030003900000000000c000301f5a5a900ca24f5000e000000a125111215461701040000000025040000000030011931010aeb095554432b30383a3030003900000000000c000301f5a5af00ca24fb000e000000dd25111215463201040000000025040000000030011731010beb095554432b30383a3030537e")

	result, err := NewDecoder(Base()).Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 6 {
		t.Fatalf("positions = %d, want 6", len(result.Positions))
	}

	for i, position := range result.Positions {
		if archived, _ := position.Get(model.KeyArchive); archived != true {
			t.Errorf("position %d archive = %#v, want true", i, archived)
		}
	}

	first := result.Positions[0]
	if math.Abs(first.Latitude-32.875911) > 1e-6 || math.Abs(first.Longitude-13.247685) > 1e-6 {
		t.Errorf("first coordinates = %.6f/%.6f", first.Latitude, first.Longitude)
	}
	if want := time.Date(2025, 11, 12, 7, 45, 45, 0, time.UTC); !first.Time.Equal(want) {
		t.Errorf("first time = %s, want %s", first.Time.UTC(), want)
	}
	if got, _ := first.Get(model.KeySatellites); got != 10 {
		t.Errorf("first satellites = %#v, want 10", got)
	}

	last := result.Positions[5]
	if math.Abs(last.Latitude-32.875951) > 1e-6 || math.Abs(last.Longitude-13.247739) > 1e-6 {
		t.Errorf("last coordinates = %.6f/%.6f", last.Latitude, last.Longitude)
	}
	if want := time.Date(2025, 11, 12, 7, 46, 32, 0, time.UTC); !last.Time.Equal(want) {
		t.Errorf("last time = %s, want %s", last.Time.UTC(), want)
	}
	if got, _ := last.Get(model.KeySatellites); got != 11 {
		t.Errorf("last satellites = %#v, want 11", got)
	}
}

// TestDecodeLocationBatch2019Header covers the 10 byte identifier and the
// normal (non archived) report type.
func TestDecodeLocationBatch2019Header(t *testing.T) {
	frame := mustHex(t, "7e0704402d0100000866496077582164881d0001000028000000000000000701a00192091f2a4700160000000226050916422701040000258230010031010e507e")

	result, err := NewDecoder(Base()).Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}

	position := result.Positions[0]
	if _, ok := position.Get(model.KeyArchive); ok {
		t.Error("normal batch item marked as archived")
	}
	if math.Abs(position.Latitude-(-27.263378)) > 1e-6 || math.Abs(position.Longitude-153.037383) > 1e-6 {
		t.Errorf("coordinates = %.6f/%.6f", position.Latitude, position.Longitude)
	}
	if want := time.Date(2026, 5, 9, 8, 42, 27, 0, time.UTC); !position.Time.Equal(want) {
		t.Errorf("time = %s, want %s", position.Time.UTC(), want)
	}
}

// TestDecodeLocationBatch2Battery pins the seven byte 0x0210 body: a battery
// level and a timestamp for the last known fix, not a batch.
func TestDecodeLocationBatch2Battery(t *testing.T) {
	frame := mustHex(t, "7e0210000704546000231900884f2606180229579c7e")

	result, err := NewDecoder(Base()).Decode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}

	position := result.Positions[0]
	if got, _ := position.Get(model.KeyBatteryLevel); got != 79 {
		t.Errorf("battery level = %#v, want 79", got)
	}
	if want := time.Date(2026, 6, 17, 18, 29, 57, 0, time.UTC); !position.Time.Equal(want) {
		t.Errorf("time = %s, want %s", position.Time.UTC(), want)
	}
}

// batchLocationBody builds one 0x0200 body for a synthetic batch.
func batchLocationBody(
	latitude, longitude uint32, timestamp, extensions []byte) []byte {
	body := make([]byte, 0, 32+len(extensions))
	body = binary.BigEndian.AppendUint32(body, 0)          // alarm
	body = binary.BigEndian.AppendUint32(body, 0x00000002) // valid fix
	body = binary.BigEndian.AppendUint32(body, latitude)
	body = binary.BigEndian.AppendUint32(body, longitude)
	body = binary.BigEndian.AppendUint16(body, 100) // altitude
	body = binary.BigEndian.AppendUint16(body, 500) // speed, 50.0 km/h
	body = binary.BigEndian.AppendUint16(body, 90)  // course
	body = append(body, timestamp...)
	return append(body, extensions...)
}

// TestDecodeLocationBatch2 covers the 0x0210 framing, where each item is a
// single byte length plus a 0x0200 body and there is no report type.
func TestDecodeLocationBatch2(t *testing.T) {
	first := batchLocationBody(54945350, 24017620,
		[]byte{0x26, 0x08, 0x27, 0x10, 0x30, 0x11},
		[]byte{0x31, 0x01, 0x09, 0x30, 0x01, 0x15})
	second := batchLocationBody(54945351, 24017621,
		[]byte{0x26, 0x08, 0x27, 0x10, 0x31, 0x22},
		[]byte{0x31, 0x01, 0x0b})

	payload := []byte{byte(len(first))}
	payload = append(payload, first...)
	payload = append(payload, byte(len(second)))
	payload = append(payload, second...)

	result, err := NewDecoder(Base()).Decode(uplinkFrame(t, msgLocationBatch2, payload))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 2 {
		t.Fatalf("positions = %d, want 2", len(result.Positions))
	}

	for i, position := range result.Positions {
		if _, ok := position.Get(model.KeyArchive); ok {
			t.Errorf("position %d archived, want no report type on 0x0210", i)
		}
	}
	if got, _ := result.Positions[0].Get(model.KeySatellites); got != 9 {
		t.Errorf("first satellites = %#v, want 9", got)
	}
	if got, _ := result.Positions[0].Get(model.KeyRSSI); got != 21 {
		t.Errorf("first rssi = %#v, want 21", got)
	}
	if got, _ := result.Positions[1].Get(model.KeySatellites); got != 11 {
		t.Errorf("second satellites = %#v, want 11", got)
	}
	if want := time.Date(2026, 8, 27, 2, 30, 11, 0, time.UTC); !result.Positions[0].Time.Equal(want) {
		t.Errorf("first time = %s, want %s", result.Positions[0].Time.UTC(), want)
	}
	if want := time.Date(2026, 8, 27, 2, 31, 22, 0, time.UTC); !result.Positions[1].Time.Equal(want) {
		t.Errorf("second time = %s, want %s", result.Positions[1].Time.UTC(), want)
	}
}

// TestDecodeLocationBatchCorruptLength guards against a batch item whose length
// runs into the checksum: it must stop instead of panicking or inventing a
// position from the next frame.
func TestDecodeLocationBatchCorruptLength(t *testing.T) {
	payload := []byte{0x00, 0x01, 0x00, 0xff, 0xff}

	result, err := NewDecoder(Base()).Decode(uplinkFrame(t, msgLocationBatch, payload))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 0 {
		t.Errorf("positions = %d, want 0", len(result.Positions))
	}
	if len(result.Responses) != 1 {
		t.Errorf("responses = %d, want 1", len(result.Responses))
	}
}

func TestDecodeDriverIdentity(t *testing.T) {
	frame := mustHex(t, "7e0702002201827061151401bc012510091507510000303032396238383500000000000000000000000000000000002b7e")

	VerifyDecode(t, NewDecoder(Base()), frame,
		PositionWith(ChecksAttributes).
			Attribute("cardStatus", 1).
			Attribute("cardResult", 0).
			Attribute("cardCode", "0029b885").
			Attribute("cardValidity", "00000000"))
}

func TestDecodeParameterQueryResponse(t *testing.T) {
	frame := mustHex(t, "7e0104018502453031334908d600002c0000000104000000b400000002040000000000000003040000000000000004040000000000000005040000000000000006040000000000000007040000000000000010066d70746e657400000011000000001200000000130f3136382e3134342e3130342e32323400000014000000001500000000160000000017000000001804000013970000001904000000000000002004000000000000002104000000000000002204000000000000002704000001f400000028040000000000000029040000000a0000002c04000000000000003004000000140000005004000000000000005204000000000000005304000000000000005504000000000000005604000000050000005704000000000000005804000000000000005904000000000000005a04000000000000007004000000000000007104000000000000007204000000000000007304000000000000007404000000000000008004000000000000008102002c0000008202012f000000830cd4c142383838383820202020000000840101787e")

	VerifyDecode(t, NewDecoder(Base()), frame,
		PositionWith(ChecksAttributes).
			Attribute("plateNumber", "粤B88888").
			Attribute("heartbeatInterval", 180).
			Attribute("apn", "mptnet").
			Attribute("server", "168.144.104.224").
			Attribute("port", 5015).
			Attribute("sleepReportInterval", 500).
			Attribute("defaultReportInterval", 10).
			Attribute("speedLimitDuration", 5))
}

// The GTSL payload carries its own length prefix; it drops off with the NUL
// padding because Java's trim also strips control characters.
func TestDecodeTransparentDriverID(t *testing.T) {
	frame := mustHex(t, "7e0900001b41305888975883194000184754534c7c367c317c307c31303231383039347c317c0d0ab47e")

	VerifyDecode(t, NewDecoder(Base()), frame,
		PositionWith(ChecksAttributes).Attribute(model.KeyDriverID, "10218094"))
}

func TestDecodeTransparentObdRt(t *testing.T) {
	frame := mustHex(t, "7e0900004a018178393931007941244f42442d52542c31322e332c302c32302c302e30302c302e30302c302c31302e38392c33312e31352c3138312e36382c3138312c35342e36322c3536352e39362c302c302c300d0ae97e")

	VerifyDecode(t, NewDecoder(Base()), frame,
		PositionWith(ChecksAttributes).
			Attribute(model.KeyPower, 12.3).
			Attribute(model.KeyFuelUsed, 565.96))
}

func TestDecodeTransparentDtc(t *testing.T) {
	frame := mustHex(t, "7e090000344f07788ef87d0138f02305151230460102020001ffffffff000100001457000000020006503134353700000c000a029dc63004b99a98230515132726787e")

	VerifyDecode(t, NewDecoder(Base()), frame,
		PositionWith(ChecksAttributes).
			Attribute(model.KeyArchive, true).
			Attribute(model.KeyDTCs, "P1457"))
}

func TestDecodeTransparentAlarm(t *testing.T) {
	frame := mustHex(t, "7e0900001f4f07788ef87d000cf0230223150215010203013800000c000b029dc58c04b99b60230223171822507e")

	VerifyDecode(t, NewDecoder(Base()), frame,
		PositionWith(ChecksAttributes).Attribute(model.KeyArchive, true))
}

func TestDecodeExtensionAdasAlarm(t *testing.T) {
	frame := mustHex(t, "7E020000834EB6FB4AF2C1010F00000800000C000B01AAB48204EA7E57000801AD00B626032715524501040000045130011F310112EB095554432D30343A3030140400000001150400000004642F0000000B00040100000000002A000801AAB33804EA7E542603271552450400303037343234322603271552450B0500E81120050E200102008C0006010400070200069B7E")

	VerifyDecode(t, NewDecoder(Base()), frame,
		Position().
			Attribute("adasAlarm", 4).
			Attribute("alarmLabel", "303037343234322603271552450b0500"))
}

func TestDecodeExtensionTirePressure(t *testing.T) {
	frame := mustHex(t, "7e02000070012040000004019c00000000000004030158df9706c9606b002c000000ab250417170023010400000c5305030000003001153101158202008b530b0101cc00262600000ff1137730017e19f100384d08026e73e0003a4d08037e19e800000000047e3b950000000005409017000000000640a69600000000337e")

	VerifyDecode(t, NewDecoder(Base()), frame,
		Position().
			Attribute("tire1Pressure", 1.4).
			Attribute("tire1SensorId", "7e19f1").
			Attribute("tire1Temp", 27))
}

func TestDecodeExtensionObdRt(t *testing.T) {
	frame := mustHex(t, "7e0200009e017721028890061b00000000004c000301d9f2d4073ca28d002b01c2000024011017260501040000023c3001193101162504000000001404000000011504000000041604000000001702000018030000002b0400000000e10400735443e20400001211e30a4e61747572654c696e6be635244f42442d52542c2c35352e3030302c3130332e37382c2c302e302c342c2c2c2c302e3030302c2c31323336302e32382c2c2c0d0aa27e")

	VerifyDecode(t, NewDecoder(Base()), frame,
		Position().
			Attribute(model.KeyRPM, 55.0).
			Attribute(model.KeyOBDSpeed, 103.78).
			Attribute(model.KeyCoolant, 4))
}

// TestDecodeTruncatedFramesDoNotPanic guards the new message types and
// extensions: every prefix of a valid frame must decode or fail cleanly.
func TestDecodeTruncatedFramesDoNotPanic(t *testing.T) {
	frames := []string{
		"7e0702002201827061151401bc012510091507510000303032396238383500000000000000000000000000000000002b7e",
		"7e0104018502453031334908d600002c0000000104000000b400000002040000000000000003040000000000000004040000000000000005040000000000000006040000000000000007040000000000000010066d70746e657400000011000000001200000000130f3136382e3134342e3130342e32323400000014000000001500000000160000000017000000001804000013970000001904000000000000002004000000000000002104000000000000002204000000000000002704000001f400000028040000000000000029040000000a0000002c04000000000000003004000000140000005004000000000000005204000000000000005304000000000000005504000000000000005604000000050000005704000000000000005804000000000000005904000000000000005a04000000000000007004000000000000007104000000000000007204000000000000007304000000000000007404000000000000008004000000000000008102002c0000008202012f000000830cd4c142383838383820202020000000840101787e",
		"7e0900001b41305888975883194000184754534c7c367c317c307c31303231383039347c317c0d0ab47e",
		"7e0900004a018178393931007941244f42442d52542c31322e332c302c32302c302e30302c302e30302c302c31302e38392c33312e31352c3138312e36382c3138312c35342e36322c3536352e39362c302c302c300d0ae97e",
		"7e090000344f07788ef87d0138f02305151230460102020001ffffffff000100001457000000020006503134353700000c000a029dc63004b99a98230515132726787e",
		"7e0900001f4f07788ef87d000cf0230223150215010203013800000c000b029dc58c04b99b60230223171822507e",
		"7E020000834EB6FB4AF2C1010F00000800000C000B01AAB48204EA7E57000801AD00B626032715524501040000045130011F310112EB095554432D30343A3030140400000001150400000004642F0000000B00040100000000002A000801AAB33804EA7E542603271552450400303037343234322603271552450B0500E81120050E200102008C0006010400070200069B7E",
		"7e02000070012040000004019c00000000000004030158df9706c9606b002c000000ab250417170023010400000c5305030000003001153101158202008b530b0101cc00262600000ff1137730017e19f100384d08026e73e0003a4d08037e19e800000000047e3b950000000005409017000000000640a69600000000337e",
		"7e0200009e017721028890061b00000000004c000301d9f2d4073ca28d002b01c2000024011017260501040000023c3001193101162504000000001404000000011504000000041604000000001702000018030000002b0400000000e10400735443e20400001211e30a4e61747572654c696e6be635244f42442d52542c2c35352e3030302c3130332e37382c2c302e302c342c2c2c2c302e3030302c2c31323336302e32382c2c2c0d0aa27e",
	}

	for _, hexFrame := range frames {
		frame := mustHex(t, hexFrame)
		for i := 0; i < len(frame); i++ {
			if _, err := NewDecoder(Base()).Decode(frame[:i]); err != nil {
				t.Fatalf("decode prefix %d of %s: %v", i, hexFrame[:16], err)
			}
		}
	}
}

func TestDecodeTransparentArchiveJcModel(t *testing.T) {
	frame := mustHex(t, "7e0900001f4f07788ef87d000cf0230223150215010203013800000c000b029dc58c04b99b60230223171822507e")

	session := &codec.DeviceSession{DeviceID: 1, UniqueID: "40778897807", Model: "JC400"}
	result, err := NewDecoder(Base()).DecodeWithSession(frame, session)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}
	value, ok := result.Positions[0].Get(model.KeyResult)
	if !ok || value == "" {
		t.Fatalf("result = %#v, want the JC body text", value)
	}
}
