package jt808

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/protocol/protocaltest"
)

// modelResolver reports a device model, which selects the model specific
// payloads.
type modelResolver struct{ model string }

func (r modelResolver) Resolve(string) (*codec.DeviceSession, error) {
	return &codec.DeviceSession{DeviceID: 1, Model: r.model}, nil
}

func testBase(options ...func(*codec.Base)) codec.Base {
	base := codec.Base{
		UniqueIDs: protocaltest.NewStubUniqueIDs(),
		Serials:   codec.NewSerialSource(),
	}
	for _, option := range options {
		option(&base)
	}
	return base
}

func withModel(name string) func(*codec.Base) {
	return func(base *codec.Base) {
		base.Resolver = modelResolver{model: name}
	}
}

func withConfig(values map[string]string) func(*codec.Base) {
	return func(base *codec.Base) {
		base.Config = func(key string) string { return values[key] }
	}
}

func withClock(now time.Time) func(*codec.Base) {
	return func(base *codec.Base) {
		base.Now = func() time.Time { return now }
	}
}

func makeCommand(commandType string, attributes map[string]any) *model.Command {
	command := model.NewCommand(1, commandType)
	for key, value := range attributes {
		command.SetAttribute(key, value)
	}
	return command
}

// encode returns the message type and the payload of the frame a command
// produces, with the checksum verified.
func encode(t *testing.T, base codec.Base, command *model.Command) (int, string) {
	t.Helper()
	frame, err := NewEncoder(base).EncodeCommand(command)
	if err != nil {
		t.Fatalf("encode %s: %v", command.Type, err)
	}
	body, _ := NewFrameDecoder().DecodeFrame(frame)
	if body == nil {
		t.Fatalf("%s: frame not decodable: %x", command.Type, frame)
	}
	if got, want := helper.XOR(body[1:len(body)-2]), body[len(body)-2]; got != want {
		t.Fatalf("%s: checksum %02x, want %02x", command.Type, got, want)
	}
	attribute := binary.BigEndian.Uint16(body[3:])
	idLength, versionLength := 6, 0
	if attribute&0x4000 != 0 {
		idLength, versionLength = 10, 1
	}
	payload := body[1+2+2+versionLength+idLength+2 : len(body)-2]
	if declared := int(attribute & 0x03ff); declared != len(payload) {
		t.Fatalf("%s: body length %d, want %d", command.Type, declared, len(payload))
	}
	return int(binary.BigEndian.Uint16(body[1:])), hex.EncodeToString(payload)
}

func TestEncodeCommands(t *testing.T) {
	for _, tc := range []struct {
		name     string
		command  *model.Command
		starts   func(*codec.Base)
		wantType int
		want     string
	}{
		{
			name: "position single", command: makeCommand(model.CommandPositionSingle, nil),
			wantType: msgLocationQuery, want: "",
		},
		{
			name: "position stop", command: makeCommand(model.CommandPositionStop, nil),
			wantType: msgTemporaryTracking, want: "000000000000",
		},
		{
			name: "reboot", command: makeCommand(model.CommandRebootDevice, nil),
			wantType: msgParameterSetting, want: "01230103",
		},
		{
			name:     "position periodic",
			command:  makeCommand(model.CommandPositionPeriodic, map[string]any{model.CmdKeyFrequency: 60}),
			wantType: msgParameterSetting, want: "0106040000003c",
		},
		{
			name: "alarm arm", command: makeCommand(model.CommandAlarmArm, nil),
			wantType: msgParameterSetting, want: "0124050175736572",
		},
		{
			name: "alarm disarm", command: makeCommand(model.CommandAlarmDisarm, nil),
			wantType: msgParameterSetting, want: "0124050075736572",
		},
		{
			name: "alarm dismiss", command: makeCommand(model.CommandAlarmDismiss, nil),
			wantType: msgAlarmAck, want: "000000000000",
		},
		{
			name: "engine stop", command: makeCommand(model.CommandEngineStop, nil),
			wantType: msgTerminalControl, want: "f0",
		},
		{
			name: "engine resume", command: makeCommand(model.CommandEngineResume, nil),
			wantType: msgTerminalControl, want: "f1",
		},
		{
			name: "set connection",
			command: makeCommand(model.CommandSetConnection, map[string]any{
				model.CmdKeyServer: "example.com", model.CmdKeyPort: 5027,
			}),
			wantType: msgConfigurationParams,
			want:     "0200000013" + "0b" + "6578616d706c652e636f6d" + "00000018" + "04" + "000013a3",
		},
		{
			name: "power off", command: makeCommand(model.CommandPowerOff, nil),
			wantType: msgTerminalControl, want: "02",
		},
		{
			name: "factory reset", command: makeCommand(model.CommandFactoryReset, nil),
			wantType: msgTerminalControl, want: "03",
		},
		{
			name:     "message",
			command:  makeCommand(model.CommandMessage, map[string]any{model.CmdKeyMessage: "測試"}),
			wantType: msgSendTextMessage, want: "04" + "9c79d487",
		},
		{
			name:     "voice message",
			command:  makeCommand(model.CommandVoiceMessage, map[string]any{model.CmdKeyMessage: "測試"}),
			wantType: msgSendTextMessage, want: "08" + "9c79d487",
		},
		{
			name:     "request photo",
			command:  makeCommand(model.CommandRequestPhoto, map[string]any{model.CmdKeyIndex: 2}),
			wantType: msgTakePhoto, want: "02" + "0001" + "000000000000000000",
		},
		{
			name:     "speed limit",
			command:  makeCommand(model.CommandSetSpeedLimit, map[string]any{model.CmdKeyData: 80}),
			wantType: msgConfigurationParams, want: "01" + "00000055" + "04" + "00000050",
		},
		{
			name: "speed limit with duration",
			command: makeCommand(model.CommandSetSpeedLimit, map[string]any{
				model.CmdKeyData: 80, model.CmdKeyDuration: 5,
			}),
			wantType: msgConfigurationParams,
			want:     "02" + "00000055" + "04" + "00000050" + "00000056" + "04" + "00000005",
		},
		{
			name:     "output control",
			command:  makeCommand(model.CommandOutputControl, map[string]any{model.CmdKeyIndex: 1}),
			wantType: msgVehicleControl, want: "01",
		},
		{
			name: "configuration", command: makeCommand(model.CommandConfiguration, nil),
			wantType: msgParameterQueryAll, want: "",
		},
		{
			name:    "video start",
			command: makeCommand(model.CommandVideoStart, map[string]any{model.CmdKeyIndex: 2}),
			starts: withConfig(map[string]string{
				keyWebURL: "http://video.example.com:8082", keyJT1078Port: "1078",
			}),
			wantType: msgVideoRequest,
			want:     "11" + "766964656f2e6578616d706c652e636f6d" + "0436" + "0000" + "02" + "01" + "00",
		},
		{
			name:     "video stop",
			command:  makeCommand(model.CommandVideoStop, map[string]any{model.CmdKeyIndex: 1}),
			wantType: msgVideoControl, want: "01000000",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := []func(*codec.Base){}
			if tc.starts != nil {
				options = append(options, tc.starts)
			}
			messageType, payload := encode(t, testBase(options...), tc.command)
			if messageType != tc.wantType {
				t.Errorf("message type = %04x, want %04x", messageType, tc.wantType)
			}
			if payload != tc.want {
				t.Errorf("payload = %s, want %s", payload, tc.want)
			}
		})
	}
}

// TestEncodeCustomByModel pins the four ways Jt808ProtocolEncoder sends a raw
// custom payload, selected by the device model.
func TestEncodeCustomByModel(t *testing.T) {
	for _, tc := range []struct {
		model    string
		data     string
		wantType int
		want     string
	}{
		{"AL300", "AT+INFO", msgConfigurationParams, "010000f030" + "07" + "41542b494e464f"},
		{"GL100", "AT+INFO", msgConfigurationParams, "010000f030" + "07" + "41542b494e464f"},
		{"VL300", "AT+INFO", msgConfigurationParams, "010000f030" + "07" + "41542b494e464f"},
		{"BSJ", "測試", msgSendTextMessage, "01" + "9c79d487"},
		{"C5", "測試", msgSendTextMessage, "01" + "9c79d487"},
		{"C5L", "測試", msgSendTextMessage, "01" + "9c79d487"},
		{"JC900", "AT+INFO", msgTransparentDownlink, "f0" + "41542b494e464f"},
		{"", "7e020000", 0, "7e020000"},
	} {
		t.Run(tc.model+"/"+tc.data, func(t *testing.T) {
			command := makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: tc.data})
			base := testBase(withModel(tc.model))
			if tc.wantType == 0 {
				frame, err := NewEncoder(base).EncodeCommand(command)
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				if got := hex.EncodeToString(frame); got != tc.want {
					t.Errorf("frame = %s, want %s", got, tc.want)
				}
				return
			}
			messageType, payload := encode(t, base, command)
			if messageType != tc.wantType {
				t.Errorf("message type = %04x, want %04x", messageType, tc.wantType)
			}
			if payload != tc.want {
				t.Errorf("payload = %s, want %s", payload, tc.want)
			}
		})
	}
}

// TestEncodeEngineControlByModel pins the model specific engine payloads and
// the alternative oil control layout.
func TestEncodeEngineControlByModel(t *testing.T) {
	for _, tc := range []struct {
		name        string
		model       string
		options     []func(*codec.Base)
		command     string
		wantType    int
		wantPayload string
	}{
		{
			name: "vl300 stop", model: "VL300", command: model.CommandEngineStop,
			wantType: msgTerminalControl, wantPayload: "23303b31",
		},
		{
			name: "vl300 resume", model: "VL300", command: model.CommandEngineResume,
			wantType: msgTerminalControl, wantPayload: "23303b30",
		},
		{
			name: "w15l stop", model: "W15L", command: model.CommandEngineStop,
			wantType: msgTerminalControl, wantPayload: "64",
		},
		{
			name: "w15l resume", model: "W15L", command: model.CommandEngineResume,
			wantType: msgTerminalControl, wantPayload: "65",
		},
		{
			name: "alternative stop", command: model.CommandEngineStop,
			options: []func(*codec.Base){
				withConfig(map[string]string{keyAlternative: "true"}),
				withClock(time.Date(2026, 11, 4, 14, 32, 51, 0, time.UTC)),
			},
			wantType: msgOilControl, wantPayload: "01" + "261104143251",
		},
		{
			name: "alternative beats the vl300 model", model: "VL300", command: model.CommandEngineStop,
			options: []func(*codec.Base){
				withConfig(map[string]string{keyAlternative: "true"}),
				withClock(time.Date(2026, 11, 4, 14, 32, 51, 0, time.UTC)),
			},
			wantType: msgOilControl, wantPayload: "01" + "261104143251",
		},
		{
			name: "alternative resume", command: model.CommandEngineResume,
			options: []func(*codec.Base){
				withConfig(map[string]string{keyAlternative: "true"}),
				withClock(time.Date(2026, 11, 4, 14, 32, 51, 0, time.UTC)),
			},
			wantType: msgOilControl, wantPayload: "00" + "261104143251",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := append([]func(*codec.Base){withModel(tc.model)}, tc.options...)
			messageType, payload := encode(t, testBase(options...), makeCommand(tc.command, nil))
			if messageType != tc.wantType {
				t.Errorf("message type = %04x, want %04x", messageType, tc.wantType)
			}
			if payload != tc.wantPayload {
				t.Errorf("payload = %s, want %s", payload, tc.wantPayload)
			}
		})
	}
}

// TestEncodeTenByteID pins the JT808-2019 header: a terminal that registered a
// ten byte identifier is addressed with one, the header carries a version, and
// the serial and payload keep their place behind the longer identifier.
func TestEncodeTenByteID(t *testing.T) {
	base := testBase()
	base.UniqueIDs = &protocaltest.StubUniqueIDs{ID: "01234567890123456789"}

	command := makeCommand(model.CommandRebootDevice, nil)
	frame, err := NewEncoder(base).EncodeCommand(command)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	body, _ := NewFrameDecoder().DecodeFrame(frame)
	if attribute := binary.BigEndian.Uint16(body[3:]); attribute&0x4000 == 0 {
		t.Fatalf("attribute %04x has no version flag", attribute)
	}
	if body[5] != 0x01 {
		t.Errorf("version = %02x, want 01", body[5])
	}
	if got := hex.EncodeToString(body[6:16]); got != "01234567890123456789" {
		t.Errorf("id = %s, want 01234567890123456789", got)
	}
	if serial := int(binary.BigEndian.Uint16(body[16:])); serial != command.Serial {
		t.Errorf("serial = %d, want %d", serial, command.Serial)
	}
	if got := hex.EncodeToString(body[18 : len(body)-2]); got != "01230103" {
		t.Errorf("payload = %s, want 01230103", got)
	}
}

// TestEncodeID pins the identifier widths, including the decimal IMEI form
// decodeID renders for a binary identifier. Sending eight bytes where the
// terminal expects six shifts the whole header.
func TestEncodeID(t *testing.T) {
	for _, tc := range []struct {
		name     string
		uniqueID string
		length   int
		want     string
	}{
		{"twelve hexadecimal digits", "012345678901", 6, "012345678901"},
		{"imei with luhn digit", "1234567890123456", 6, "7048860ddf79"},
		{"imei without luhn digit", "123456789012345", 6, "7048860ddf79"},
		{"ten byte identifier", "01234567890123456789", 10, "01234567890123456789"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := encodeID(tc.uniqueID, tc.length)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got := hex.EncodeToString(id); got != tc.want {
				t.Errorf("id = %s, want %s", got, tc.want)
			}
			if len(id) != tc.length {
				t.Errorf("width = %d, want %d", len(id), tc.length)
			}
		})
	}
}

// TestEncodeCustomRejectsNonHex pins that a model without a special custom
// layout still has to be handed a whole frame.
func TestEncodeCustomRejectsNonHex(t *testing.T) {
	command := makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: "AT+INFO"})
	if _, err := NewEncoder(testBase()).EncodeCommand(command); !errors.Is(err, ErrUnsupportedCommand) {
		t.Fatalf("err = %v, want ErrUnsupportedCommand", err)
	}
}

// TestEncodeNonNumericAttributeFails pins that a parameter that is present but
// not a number refuses the command instead of sending a zeroed field.
func TestEncodeNonNumericAttributeFails(t *testing.T) {
	for _, tc := range []struct {
		commandType string
		attributes  map[string]any
	}{
		{model.CommandPositionPeriodic, map[string]any{model.CmdKeyFrequency: "abc"}},
		{model.CommandSetSpeedLimit, map[string]any{model.CmdKeyData: true}},
		{model.CommandSetConnection, map[string]any{model.CmdKeyServer: "example.com", model.CmdKeyPort: "http"}},
	} {
		t.Run(tc.commandType, func(t *testing.T) {
			_, err := NewEncoder(testBase()).EncodeCommand(makeCommand(tc.commandType, tc.attributes))
			if !errors.Is(err, ErrUnsupportedCommand) {
				t.Fatalf("err = %v, want ErrUnsupportedCommand", err)
			}
		})
	}
}

// TestEncodeMissingAttributeFails pins that a command whose layout needs a
// parameter is refused instead of sent with a zeroed field.
func TestEncodeMissingAttributeFails(t *testing.T) {
	for _, commandType := range []string{
		model.CommandPositionPeriodic,
		model.CommandSetConnection,
		model.CommandMessage,
		model.CommandVoiceMessage,
		model.CommandSetSpeedLimit,
		model.CommandCustom,
	} {
		t.Run(commandType, func(t *testing.T) {
			_, err := NewEncoder(testBase()).EncodeCommand(makeCommand(commandType, nil))
			if !errors.Is(err, ErrUnsupportedCommand) {
				t.Fatalf("err = %v, want ErrUnsupportedCommand", err)
			}
		})
	}
}

// TestEncodeVideoStartNeedsEndpoint pins that the video command reports the
// missing configuration instead of addressing a zero port.
func TestEncodeVideoStartNeedsEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]string
	}{
		{"no configuration", nil},
		{"no web url", map[string]string{keyJT1078Port: "1078"}},
		{"no port", map[string]string{keyWebURL: "http://video.example.com"}},
		{"bad port", map[string]string{keyWebURL: "http://video.example.com", keyJT1078Port: "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := []func(*codec.Base){}
			if tc.values != nil {
				options = append(options, withConfig(tc.values))
			}
			_, err := NewEncoder(testBase(options...)).
				EncodeCommand(makeCommand(model.CommandVideoStart, nil))
			if !errors.Is(err, ErrUnsupportedCommand) {
				t.Fatalf("err = %v, want ErrUnsupportedCommand", err)
			}
		})
	}
}

// TestEncodeVideoStartUsesTheReferencePortByDefault pins that videoStart names
// the port the reference serves jt1078 on without extra configuration.
func TestEncodeVideoStartUsesTheReferencePortByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traccar.xml")
	content := `<?xml version='1.0' encoding='UTF-8'?>
<properties>
	<entry key='web.url'>http://video.example.com</entry>
</properties>`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	cfg, err := config.Load(path, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	base := testBase()
	base.Config = cfg.Protocol("jt808").String
	messageType, payload := encode(t, base,
		makeCommand(model.CommandVideoStart, map[string]any{model.CmdKeyIndex: 2}))
	if messageType != msgVideoRequest {
		t.Errorf("message type = %04x, want %04x", messageType, msgVideoRequest)
	}
	want := "11" + "766964656f2e6578616d706c652e636f6d" + "148f" + "0000" + "02" + "01" + "00"
	if payload != want {
		t.Errorf("payload = %s, want %s", payload, want)
	}
}
