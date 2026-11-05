package teltonika

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

func testSession() *codec.DeviceSession {
	return &codec.DeviceSession{DeviceID: 1, UniqueID: "123456789012345"}
}

// TestChecksum validates the CRC16/IBM parameters against a frame recorded
// from a real device: the last four bytes hold the checksum of everything
// between the length field and the checksum itself.
func TestChecksum(t *testing.T) {
	frame := mustHex(t, "00000000000000240d01060000001c642b3ad14754534c7c367c317c307c31323734393838347c317c0d0a010000ec11")

	data := frame[8 : len(frame)-4]
	want := int(binary.BigEndian.Uint32(frame[len(frame)-4:]))
	if got := helper.CRC16IBM(data); got != want {
		t.Errorf("crc = %04x, want %04x", got, want)
	}
}

func TestDecodeIdentification(t *testing.T) {
	result, err := NewDecoder(Base()).Decode(mustHex(t, "000F313233343536373839303132333435"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.UniqueID != "123456789012345" {
		t.Errorf("unique id = %q, want %q", result.UniqueID, "123456789012345")
	}
	if len(result.Positions) != 0 {
		t.Errorf("positions = %d, want 0", len(result.Positions))
	}
	if len(result.Responses) != 1 || result.Responses[0][0] != 0x01 {
		t.Fatalf("responses = %x, want 01", result.Responses)
	}
}

func TestDecodeIdentificationUnknownDevice(t *testing.T) {
	resolver := NewStubResolver()
	resolver.Reject = true
	decoder := NewDecoder(codec.Base{Resolver: resolver})

	result, err := decoder.Decode(mustHex(t, "000F313233343536373839303132333435"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Responses) != 1 || result.Responses[0][0] != 0x00 {
		t.Fatalf("responses = %x, want 00", result.Responses)
	}
}

func TestDecodeHeartbeat(t *testing.T) {
	result, err := NewDecoder(Base()).DecodeWithSession([]byte{0xff}, testSession())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Consumed || len(result.Positions) != 0 || len(result.Responses) != 0 {
		t.Fatalf("heartbeat produced %+v", result)
	}
}

// buildRecord creates one Codec 8 AVL record with known values.
func buildRecord() []byte {
	record := make([]byte, 0, 64)
	record = binary.BigEndian.AppendUint64(record, 1704067200000) // 2024-01-01T00:00:00Z
	record = append(record, 1)                                    // priority
	record = binary.BigEndian.AppendUint32(record, 240176200)     // longitude 24.01762
	record = binary.BigEndian.AppendUint32(record, 549453500)     // latitude 54.94535
	record = binary.BigEndian.AppendUint16(record, 100)           // altitude
	record = binary.BigEndian.AppendUint16(record, 180)           // course
	record = append(record, 9)                                    // satellites
	record = binary.BigEndian.AppendUint16(record, 50)            // speed km/h
	record = append(record, 1)                                    // event id
	record = append(record, 4)                                    // total IO records

	record = append(record, 2, 239, 1, 240, 0) // 1 byte: ignition on, motion off
	record = append(record, 1, 66)             // 2 byte: external voltage
	record = binary.BigEndian.AppendUint16(record, 12500)
	record = append(record, 1, 16) // 4 byte: odometer
	record = binary.BigEndian.AppendUint32(record, 123456)
	record = append(record, 0) // 8 byte: none
	return record
}

// buildCodec8Frame wraps records into a TCP data packet. The length field
// counts everything after the four byte field except the checksum, which is
// what the devices send.
func buildCodec8Frame(codecID byte, count byte, records ...[]byte) []byte {
	block := []byte{codecID, count}
	for _, record := range records {
		block = append(block, record...)
	}
	block = append(block, count)

	// TCP packets start with four zero bytes, then the data length.
	frame := make([]byte, 0, len(block)+12)
	frame = append(frame, 0, 0, 0, 0)
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(block)))
	frame = append(frame, block...)
	frame = binary.BigEndian.AppendUint32(frame, uint32(helper.CRC16IBM(block)))
	return frame
}

func TestDecodeCodec8(t *testing.T) {
	frame := buildCodec8Frame(codec8, 1, buildRecord())

	result, err := NewDecoder(Base()).DecodeWithSession(frame, testSession())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}

	position := result.Positions[0]
	want := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if !position.Time.Equal(want) {
		t.Errorf("time = %s, want %s", position.Time.UTC(), want)
	}
	if !position.Valid {
		t.Error("valid = false, want true")
	}
	if math.Abs(position.Latitude-54.94535) > 1e-6 {
		t.Errorf("latitude = %.7f, want 54.9453500", position.Latitude)
	}
	if math.Abs(position.Longitude-24.01762) > 1e-6 {
		t.Errorf("longitude = %.7f, want 24.0176200", position.Longitude)
	}
	if position.Altitude != 100 || position.Course != 180 {
		t.Errorf("altitude = %.0f, course = %.0f, want 100/180", position.Altitude, position.Course)
	}
	if math.Abs(position.Speed-helper.KnotsFromKph(50)) > 1e-9 {
		t.Errorf("speed = %.6f, want %.6f", position.Speed, helper.KnotsFromKph(50))
	}

	for key, wantValue := range map[string]any{
		model.KeySatellites: 9,
		model.KeyEvent:      1,
		model.KeyOdometer:   uint32(123456),
		model.KeyIgnition:   true,
		model.KeyMotion:     false,
		model.KeyPriority:   1,
	} {
		got, ok := position.Get(key)
		if !ok {
			t.Errorf("attribute %q missing", key)
			continue
		}
		if got != wantValue {
			t.Errorf("attribute %q = %#v, want %#v", key, got, wantValue)
		}
	}

	// io66 is promoted to the power attribute by decodeParameter.
	if power, ok := position.Get(model.KeyPower); !ok || power != 12.5 {
		t.Errorf("power = %v, want 12.5", power)
	}

	if len(result.Responses) != 1 || len(result.Responses[0]) != 4 || result.Responses[0][3] != 1 {
		t.Fatalf("response = %x, want 00000001", result.Responses)
	}
}

func TestDecodeCodec8MultipleRecords(t *testing.T) {
	frame := buildCodec8Frame(codec8, 2, buildRecord(), buildRecord())

	result, err := NewDecoder(Base()).DecodeWithSession(frame, testSession())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 2 {
		t.Fatalf("positions = %d, want 2", len(result.Positions))
	}
	if len(result.Responses) != 1 || binary.BigEndian.Uint32(result.Responses[0]) != 2 {
		t.Fatalf("response = %x, want 00000002", result.Responses)
	}
}

// TestDecodeCodec13 uses a real device frame carrying a GTSL driver id.
func TestDecodeCodec13(t *testing.T) {
	frame := mustHex(t, "00000000000000240d01060000001c642b3ad14754534c7c367c317c307c31323734393838347c317c0d0a010000ec11")

	result, err := NewDecoder(Base()).DecodeWithSession(frame, testSession())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}
	if value, _ := result.Positions[0].Get(model.KeyDriverID); value != "12749884" {
		t.Errorf("driver id = %v, want 12749884", value)
	}
	if len(result.Responses) != 0 {
		t.Errorf("responses = %x, want none", result.Responses)
	}
}

func TestDecodeCodec12(t *testing.T) {
	// Codec 12 packet carrying the ASCII payload "getver".
	block := []byte{codec12, 1, 5, 0, 0, 0, 6}
	block = append(block, []byte("getver")...)
	block = append(block, 1)

	frame := make([]byte, 0, len(block)+12)
	frame = append(frame, 0, 0, 0, 0)
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(block)))
	frame = append(frame, block...)
	frame = binary.BigEndian.AppendUint32(frame, uint32(helper.CRC16IBM(block)))

	result, err := NewDecoder(Base()).DecodeWithSession(frame, testSession())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}
	if value, _ := result.Positions[0].Get(model.KeyResult); value != "getver" {
		t.Errorf("result = %v, want getver", value)
	}
	if len(result.Responses) != 0 {
		t.Errorf("responses = %x, want none", result.Responses)
	}
}

func TestEncodeCommand(t *testing.T) {
	encoder := NewEncoder(Base())

	frame, err := encoder.EncodeCommand(model.NewCommand(1, model.CommandEngineStop))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(frame) != 4+4+3+4+len("setdigout 1")+1+4 {
		t.Fatalf("frame length = %d", len(frame))
	}
	if string(frame[15:26]) != "setdigout 1" {
		t.Errorf("payload = %q", frame[15:26])
	}
	if binary.BigEndian.Uint32(frame[len(frame)-4:]) != uint32(helper.CRC16IBM(frame[8:len(frame)-4])) {
		t.Errorf("crc mismatch in %x", frame)
	}

	custom := model.NewCommand(1, model.CommandCustom)
	custom.SetAttribute(model.CmdKeyData, "0A0B0C")
	if frame, err = encoder.EncodeCommand(custom); err != nil {
		t.Fatalf("encode custom: %v", err)
	}
	if string(frame[15:18]) != "\x0a\x0b\x0c" {
		t.Errorf("hex payload = %x", frame[15:18])
	}
}

func TestFrameDecoder(t *testing.T) {
	decoder := NewFrameDecoder()
	stream := append([]byte{0xff}, mustHex(t, "000F313233343536373839303132333435")...)

	first, consumed := decoder.DecodeFrame(stream)
	if len(first) != 1 || first[0] != 0xff || consumed != 1 {
		t.Fatalf("heartbeat = %x, consumed = %d", first, consumed)
	}

	second, consumed := decoder.DecodeFrame(stream[1:])
	if hex.EncodeToString(second) != "000f313233343536373839303132333435" || consumed != 17 {
		t.Fatalf("identification = %x, consumed = %d", second, consumed)
	}

	frame := buildCodec8Frame(codec8, 1, buildRecord())
	got, consumed := decoder.DecodeFrame(frame[:len(frame)-1])
	if got != nil || consumed != 0 {
		t.Fatalf("partial frame = %x, consumed = %d", got, consumed)
	}
	got, consumed = decoder.DecodeFrame(frame)
	if hex.EncodeToString(got) != hex.EncodeToString(frame) || consumed != len(frame) {
		t.Fatalf("data frame = %x, consumed = %d", got, consumed)
	}
}

// TestDecodeBLETagVoltage uses the Codec 8E frame from
// TeltonikaProtocolDecoderTest that carries a BLE tag with a voltage field.
func TestDecodeBLETagVoltage(t *testing.T) {
	frame := mustHex(t, "00000000000000768e010000018fdc4b27cb015b3e33ceefa529030009013f0e0000022400010000000000000000000102240049010f0001c60106babbf36300550202806d0f0001ca01063456555565690202806b0f0001d10106467975425450020280690b0001c90106fa54ba8d00550b0001cf0106cabbf36300550100005455")

	result, err := NewDecoder(Base()).DecodeWithSession(frame, testSession())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}
	if value, ok := result.Positions[0].Get("tag1Voltage"); !ok || value != 3090 {
		t.Errorf("tag1Voltage = %v, want 3090", value)
	}
	if value, ok := result.Positions[0].Get("tag1Id"); !ok || value != "babbf3630055" {
		t.Errorf("tag1Id = %v, want babbf3630055", value)
	}
}

// TestDecodeBLETagBattery uses the Codec 8E frame from
// TeltonikaProtocolDecoderTest that carries a BLE tag with a battery field.
func TestDecodeBLETagBattery(t *testing.T) {
	frame := mustHex(t, "000000000000004b8e010000018368952793000f0e54fc209ab05800b300b40e00002a4f0001000000000000000000012a4f001e011c0001a40110eb47706aa38255aa96f21a154e2d00550d01000e020bd6")

	result, err := NewDecoder(Base()).DecodeWithSession(frame, testSession())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}
	if value, ok := result.Positions[0].Get("tag1Battery"); !ok || value != 3030 {
		t.Errorf("tag1Battery = %v, want 3030", value)
	}
	if value, ok := result.Positions[0].Get("tag1Id"); !ok || value != "eb47706aa38255aa96f21a154e2d0055" {
		t.Errorf("tag1Id = %v, want eb47706aa38255aa96f21a154e2d0055", value)
	}
}

// TestDecodeBLEBeacon covers element 385. The upstream tests carry no such
// frame, so the payload is built to the layout of the reference decoder.
func TestDecodeBLEBeacon(t *testing.T) {
	data := []byte{0x00}      // data part
	data = append(data, 0x16) // iBeacon, battery and temperature present
	data = append(data, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f)
	data = binary.BigEndian.AppendUint16(data, 1)    // major
	data = binary.BigEndian.AppendUint16(data, 2)    // minor
	data = append(data, 0xc0)                        // rssi -64
	data = binary.BigEndian.AppendUint16(data, 3000) // battery 30.00
	data = binary.BigEndian.AppendUint16(data, 20)   // temperature

	result, err := NewDecoder(Base()).DecodeWithSession(
		buildCodec8ExtFrame(385, data), testSession())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(result.Positions))
	}

	position := result.Positions[0]
	for key, want := range map[string]any{
		"beacon1Uuid":    "000102030405060708090a0b0c0d0e0f",
		"beacon1Major":   1,
		"beacon1Minor":   2,
		"beacon1Rssi":    -64,
		"beacon1Battery": 30.0,
		"beacon1Temp":    20,
	} {
		if got, ok := position.Get(key); !ok || got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
}

// buildCodec8ExtFrame wraps one Codec 8 Extended record with no IO elements
// and a single variable length element.
func buildCodec8ExtFrame(id int, data []byte) []byte {
	record := make([]byte, 0, 64)
	record = binary.BigEndian.AppendUint64(record, 1704067200000)
	record = append(record, 1)
	record = binary.BigEndian.AppendUint32(record, 240176200)
	record = binary.BigEndian.AppendUint32(record, 549453500)
	record = binary.BigEndian.AppendUint16(record, 100)
	record = binary.BigEndian.AppendUint16(record, 180)
	record = append(record, 9)
	record = binary.BigEndian.AppendUint16(record, 50)
	record = binary.BigEndian.AppendUint16(record, 1) // event id
	record = binary.BigEndian.AppendUint16(record, 0) // total IO records
	record = append(record, 0, 0, 0, 0, 0, 0, 0, 0)   // four empty IO groups
	record = binary.BigEndian.AppendUint16(record, 1) // one variable length element
	record = binary.BigEndian.AppendUint16(record, uint16(id))
	record = binary.BigEndian.AppendUint16(record, uint16(len(data)))
	record = append(record, data...)

	return buildCodec8Frame(codec8Ext, 1, record)
}
