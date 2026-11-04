package gt06

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/protocol/protocaltest"
)

// modelResolver reports a device model, which selects the G109 engine layout.
type modelResolver struct{ model string }

func (r modelResolver) Resolve(string) (*codec.DeviceSession, error) {
	return &codec.DeviceSession{DeviceID: 1, Model: r.model}, nil
}

func testBase(options ...func(*codec.Base)) codec.Base {
	base := codec.Base{UniqueIDs: protocaltest.NewStubUniqueIDs()}
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

func makeCommand(commandType string, attributes map[string]any) *model.Command {
	command := model.NewCommand(1, commandType)
	for key, value := range attributes {
		command.SetAttribute(key, value)
	}
	return command
}

func encodeFrame(t *testing.T, base codec.Base, command *model.Command) []byte {
	t.Helper()
	frame, err := NewEncoder(base).EncodeCommand(command)
	if err != nil {
		t.Fatalf("encode %s: %v", command.Type, err)
	}
	return frame
}

// frameContent returns the command content of a frame and verifies the header,
// the length byte and the checksum around it.
func frameContent(t *testing.T, frame []byte) string {
	t.Helper()
	if len(frame) < 15 || frame[0] != 0x78 || frame[1] != 0x78 {
		t.Fatalf("frame = %x", frame)
	}
	if got, want := int(frame[2]), len(frame)-5; got != want {
		t.Fatalf("length byte %d, want %d: %x", got, want, frame)
	}
	if frame[3] != msgCommand0 {
		t.Fatalf("message type %02x, want %02x", frame[3], msgCommand0)
	}
	// The content length field also counts the four command index bytes, which
	// sit between the header and the content.
	if !bytes.Equal(frame[5:9], []byte{0, 0, 0, 0}) {
		t.Fatalf("command index %x", frame[5:9])
	}
	end := 5 + int(frame[4])
	if end > len(frame)-6 {
		t.Fatalf("content length %d overruns frame %x", frame[4], frame)
	}
	tail := frame[end : len(frame)-4]
	if len(tail) == 4 {
		if tail[0] != 0 || tail[1] != 2 {
			t.Fatalf("language field %x, want 0002", tail[:2])
		}
		tail = tail[2:]
	}
	if len(tail) != 2 || tail[0] != 0 || tail[1] != 0 {
		t.Fatalf("message index %x, want 0000", tail)
	}
	if got, want := uint16(frame[len(frame)-4])<<8|uint16(frame[len(frame)-3]), uint16(helper.CRC16X25(frame[2:len(frame)-4])); got != want {
		t.Fatalf("crc %04x, want %04x: %x", got, want, frame)
	}
	if frame[len(frame)-2] != 0x0d || frame[len(frame)-1] != 0x0a {
		t.Fatalf("terminator = %x", frame[len(frame)-2:])
	}
	return string(frame[9:end])
}

// TestEncodeEngineStopFrame pins the whole frame, including the checksum and
// the terminator, of the default engine stop command.
func TestEncodeEngineStopFrame(t *testing.T) {
	frame := encodeFrame(t, testBase(), makeCommand(model.CommandEngineStop, nil))
	want, err := hex.DecodeString("787812800c0000000052656c61792c312300009dee0d0a")
	if err != nil {
		t.Fatalf("decode want: %v", err)
	}
	if !bytes.Equal(frame, want) {
		t.Fatalf("frame = %x, want %x", frame, want)
	}
}

// TestEncodeLanguageField checks that the optional english field sits before
// the message index and is counted by the length byte.
func TestEncodeLanguageField(t *testing.T) {
	plain := encodeFrame(t, testBase(), makeCommand(model.CommandEngineStop, nil))
	translated := encodeFrame(t,
		testBase(withConfig(map[string]string{config.ProtocolLanguage: "on"})),
		makeCommand(model.CommandEngineStop, nil))

	if got, want := int(translated[2]), int(plain[2])+2; got != want {
		t.Fatalf("length byte %d, want %d", got, want)
	}
	contentEnd := 5 + int(translated[4])
	if !bytes.Equal(translated[contentEnd:contentEnd+2], []byte{0, 2}) {
		t.Fatalf("language field = %x", translated[contentEnd:contentEnd+2])
	}
	if !bytes.Equal(translated[len(translated)-6:len(translated)-4], []byte{0, 0}) {
		t.Fatalf("message index = %x", translated[len(translated)-6:len(translated)-4])
	}
	if !bytes.Equal(translated[3:contentEnd], plain[3:5+int(plain[4])]) {
		t.Fatalf("body changed: %x, want %x", translated[3:contentEnd], plain[3:5+int(plain[4])])
	}
	if got := frameContent(t, translated); got != "Relay,1#" {
		t.Fatalf("content = %q, want %q", got, "Relay,1#")
	}
}

func TestEncodeCommands(t *testing.T) {
	for _, tc := range []struct {
		name        string
		commandType string
		attributes  map[string]any
		want        string
	}{
		{"engine stop", model.CommandEngineStop, nil, "Relay,1#"},
		{"engine resume", model.CommandEngineResume, nil, "Relay,0#"},
		{"position single", model.CommandPositionSingle, nil, "WHERE#"},
		{"position periodic", model.CommandPositionPeriodic, map[string]any{model.CmdKeyFrequency: 60}, "TIMER,60#"},
		{"position periodic zero", model.CommandPositionPeriodic, map[string]any{model.CmdKeyFrequency: 0}, "TIMER,0#"},
		{"position stop", model.CommandPositionStop, nil, "TIMER,0#"},
		{"reboot", model.CommandRebootDevice, nil, "RESET#"},
		{"factory reset", model.CommandFactoryReset, nil, "FACTORY#"},
		{"power off", model.CommandPowerOff, nil, "POWEROFF#"},
		{"device status", model.CommandGetDeviceStatus, nil, "STATUS#"},
		{"version", model.CommandGetVersion, nil, "VERSION#"},
		{"configuration", model.CommandConfiguration, nil, "PARAM#"},
		{"sos", model.CommandSOSNumber, map[string]any{model.CmdKeyIndex: 0, model.CmdKeyPhone: "10086"}, "SOS,A,10086#"},
		{"phonebook", model.CommandSetPhonebook, map[string]any{model.CmdKeyIndex: 3, model.CmdKeyPhone: "10086"}, "CENTER,D,10086#"},
		{"voice monitoring", model.CommandVoiceMonitoring, map[string]any{model.CmdKeyPhone: "10086"}, "LISTEN,10086#"},
		{"server", model.CommandSetConnection, map[string]any{model.CmdKeyServer: "192.168.1.5", model.CmdKeyPort: 5023}, "SERVER,192.168.1.5,5023#"},
		{"domain", model.CommandSetConnection, map[string]any{model.CmdKeyServer: "gps.example.com", model.CmdKeyPort: 5023}, "DOMAIN,gps.example.com,5023#"},
		{"custom passthrough", model.CommandCustom, map[string]any{model.CmdKeyData: "WHERE#"}, "WHERE#"},
		{"custom without hash", model.CommandCustom, map[string]any{model.CmdKeyData: "RAW"}, "RAW"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := encodeFrame(t, testBase(), makeCommand(tc.commandType, tc.attributes))
			if got := frameContent(t, frame); got != tc.want {
				t.Fatalf("content = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEncodeEngineAlternative(t *testing.T) {
	base := testBase(withConfig(map[string]string{
		config.ProtocolAlternative:    "true",
		config.ProtocolDevicePassword: "secret",
	}))
	if got := frameContent(t, encodeFrame(t, base, makeCommand(model.CommandEngineStop, nil))); got != "DYD,secret#" {
		t.Fatalf("stop = %q", got)
	}
	if got := frameContent(t, encodeFrame(t, base, makeCommand(model.CommandEngineResume, nil))); got != "HFYD,secret#" {
		t.Fatalf("resume = %q", got)
	}

	defaults := testBase(withConfig(map[string]string{config.ProtocolAlternative: "yes"}))
	if got := frameContent(t, encodeFrame(t, defaults, makeCommand(model.CommandEngineStop, nil))); got != "DYD,123456#" {
		t.Fatalf("stop without password = %q", got)
	}
}

func TestEncodeEngineAlternativeSpellings(t *testing.T) {
	for _, value := range []string{"true", "TRUE", " yes ", "on", "1"} {
		base := testBase(withConfig(map[string]string{config.ProtocolAlternative: value}))
		if got := frameContent(t, encodeFrame(t, base, makeCommand(model.CommandEngineStop, nil))); got != "DYD,123456#" {
			t.Errorf("alternative %q: stop = %q", value, got)
		}
	}
	for _, value := range []string{"", "false", "0", "no"} {
		base := testBase(withConfig(map[string]string{config.ProtocolAlternative: value}))
		if got := frameContent(t, encodeFrame(t, base, makeCommand(model.CommandEngineStop, nil))); got != "Relay,1#" {
			t.Errorf("alternative %q: stop = %q", value, got)
		}
	}
}

func TestEncodeEngineG109(t *testing.T) {
	base := testBase(
		withModel("G109"),
		withConfig(map[string]string{
			config.ProtocolAlternative:    "true",
			config.ProtocolDevicePassword: "secret",
		}),
	)
	if got := frameContent(t, encodeFrame(t, base, makeCommand(model.CommandEngineStop, nil))); got != "DYD#" {
		t.Fatalf("stop = %q", got)
	}
	if got := frameContent(t, encodeFrame(t, base, makeCommand(model.CommandEngineResume, nil))); got != "HFYD#" {
		t.Fatalf("resume = %q", got)
	}

	other := testBase(withModel("GT06N"), withConfig(map[string]string{config.ProtocolAlternative: "true"}))
	if got := frameContent(t, encodeFrame(t, other, makeCommand(model.CommandEngineStop, nil))); got != "DYD,123456#" {
		t.Fatalf("other model stop = %q", got)
	}
}

func TestEncodeUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func() *model.Command
	}{
		{"unknown type", func() *model.Command {
			return makeCommand(model.CommandAlarmArm, nil)
		}},
		{"missing frequency", func() *model.Command {
			return makeCommand(model.CommandPositionPeriodic, nil)
		}},
		{"frequency not a number", func() *model.Command {
			return makeCommand(model.CommandPositionPeriodic, map[string]any{model.CmdKeyFrequency: "soon"})
		}},
		{"frequency negative", func() *model.Command {
			return makeCommand(model.CommandPositionPeriodic, map[string]any{model.CmdKeyFrequency: -5})
		}},
		{"missing index", func() *model.Command {
			return makeCommand(model.CommandSOSNumber, map[string]any{model.CmdKeyPhone: "10086"})
		}},
		{"index too high", func() *model.Command {
			return makeCommand(model.CommandSOSNumber, map[string]any{model.CmdKeyIndex: 4, model.CmdKeyPhone: "10086"})
		}},
		{"index negative", func() *model.Command {
			return makeCommand(model.CommandSetPhonebook, map[string]any{model.CmdKeyIndex: -1, model.CmdKeyPhone: "10086"})
		}},
		{"missing phone", func() *model.Command {
			return makeCommand(model.CommandVoiceMonitoring, nil)
		}},
		{"empty phone", func() *model.Command {
			command := makeCommand(model.CommandSOSNumber, map[string]any{model.CmdKeyIndex: 1})
			command.Attributes[model.CmdKeyPhone] = ""
			return command
		}},
		{"missing server", func() *model.Command {
			return makeCommand(model.CommandSetConnection, map[string]any{model.CmdKeyPort: 5023})
		}},
		{"empty server", func() *model.Command {
			command := makeCommand(model.CommandSetConnection, map[string]any{model.CmdKeyPort: 5023})
			command.Attributes[model.CmdKeyServer] = ""
			return command
		}},
		{"missing port", func() *model.Command {
			return makeCommand(model.CommandSetConnection, map[string]any{model.CmdKeyServer: "gps.example.com"})
		}},
		{"port zero", func() *model.Command {
			return makeCommand(model.CommandSetConnection, map[string]any{model.CmdKeyServer: "gps.example.com", model.CmdKeyPort: 0})
		}},
		{"port too high", func() *model.Command {
			return makeCommand(model.CommandSetConnection, map[string]any{model.CmdKeyServer: "gps.example.com", model.CmdKeyPort: 70000})
		}},
		{"custom without data", func() *model.Command {
			return makeCommand(model.CommandCustom, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewEncoder(testBase()).EncodeCommand(tc.build()); !errors.Is(err, ErrUnsupportedCommand) {
				t.Fatalf("err = %v, want ErrUnsupportedCommand", err)
			}
		})
	}
}
