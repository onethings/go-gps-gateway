package tk103

import (
	"errors"
	"strings"
	"testing"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/protocol/protocaltest"
)

func testBase(alternative bool) codec.Base {
	base := codec.Base{UniqueIDs: protocaltest.NewStubUniqueIDs()}
	if alternative {
		base.Config = func(key string) string {
			if key == config.ProtocolAlternative {
				return "true"
			}
			return ""
		}
	}
	return base
}

func makeCommand(commandType string, attributes map[string]any) *model.Command {
	command := model.NewCommand(1, commandType)
	for key, value := range attributes {
		command.SetAttribute(key, value)
	}
	return command
}

func TestEncoderLegacyVectors(t *testing.T) {
	cases := []struct {
		name     string
		command  *model.Command
		expected string
	}{
		{
			"output control",
			makeCommand(model.CommandOutputControl, map[string]any{model.CmdKeyData: "1"}),
			"(123456789012345AV001)",
		},
		{"engine stop", makeCommand(model.CommandEngineStop, nil), "(123456789012345AV010)"},
		{"position single", makeCommand(model.CommandPositionSingle, nil), "(123456789012345AP00)"},
		{
			"position periodic",
			makeCommand(model.CommandPositionPeriodic, map[string]any{model.CmdKeyFrequency: 60}),
			"(123456789012345AR00003C0000)",
		},
		{"position stop", makeCommand(model.CommandPositionStop, nil), "(123456789012345AR0000000000)"},
		{"get version", makeCommand(model.CommandGetVersion, nil), "(123456789012345AP07)"},
		{"reboot", makeCommand(model.CommandRebootDevice, nil), "(123456789012345AT00)"},
		{"set odometer", makeCommand(model.CommandSetOdometer, nil), "(123456789012345AX01)"},
		{
			"custom",
			makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: "AA00"}),
			"(123456789012345AA00)",
		},
	}

	encoder := NewEncoder(testBase(false))
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			data, err := encoder.EncodeCommand(testCase.command)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got := string(data); got != testCase.expected {
				t.Fatalf("encode = %q, want %q", got, testCase.expected)
			}
		})
	}
}

func TestEncoderAlternativeVectors(t *testing.T) {
	cases := []struct {
		name     string
		command  *model.Command
		expected string
	}{
		{"position single", makeCommand(model.CommandPositionSingle, nil), "[begin]sms2,*getposl*,[end]"},
		{"position periodic", makeCommand(model.CommandPositionPeriodic, nil), "[begin]sms2,*routetrack*99*,[end]"},
		{"position stop", makeCommand(model.CommandPositionStop, nil), "[begin]sms2,*routetrackoff*,[end]"},
		{"get version", makeCommand(model.CommandGetVersion, nil), "[begin]sms2,*about*,[end]"},
		{"reboot", makeCommand(model.CommandRebootDevice, nil), "[begin]sms2,88888888,[end]"},
		{"identification", makeCommand(model.CommandIdentification, nil), "[begin]sms2,999999,[end]"},
		{"power off", makeCommand(model.CommandPowerOff, nil), "[begin]sms2,*turnoff*,[end]"},
		{"device status", makeCommand(model.CommandGetDeviceStatus, nil), "[begin]sms2,*status*,[end]"},
		{
			"deep sleep on",
			makeCommand(model.CommandModeDeepSleep, map[string]any{model.CmdKeyEnable: true}),
			"[begin]sms2,*sleep*2*,[end]",
		},
		{
			"deep sleep off",
			makeCommand(model.CommandModeDeepSleep, map[string]any{model.CmdKeyEnable: false}),
			"[begin]sms2,*sleepoff*,[end]",
		},
		{
			"power saving on",
			makeCommand(model.CommandModePowerSaving, map[string]any{model.CmdKeyEnable: true}),
			"[begin]sms2,*sleepv*,[end]",
		},
		{
			"sos on",
			makeCommand(model.CommandAlarmSOS, map[string]any{model.CmdKeyEnable: true}),
			"[begin]sms2,*soson*,[end]",
		},
		{
			"sos off",
			makeCommand(model.CommandAlarmSOS, map[string]any{model.CmdKeyEnable: false}),
			"[begin]sms2,*sosoff*,[end]",
		},
		{
			"custom",
			makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: "any text is ok"}),
			"[begin]sms2,any text is ok,[end]",
		},
		{
			"set connection",
			makeCommand(model.CommandSetConnection, map[string]any{
				model.CmdKeyServer: "1.2.3.4",
				model.CmdKeyPort:   "5555",
			}),
			"[begin]sms2,*setip*1*2*3*4*5555*,[end]",
		},
		{
			"sos number",
			makeCommand(model.CommandSOSNumber, map[string]any{
				model.CmdKeyPhone:          "+55555555555",
				model.CmdKeyDevicePassword: "232323",
			}),
			"[begin]sms2,*master*232323*+55555555555*,[end]",
		},
	}

	encoder := NewEncoder(testBase(true))
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			data, err := encoder.EncodeCommand(testCase.command)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got := string(data); got != testCase.expected {
				t.Fatalf("encode = %q, want %q", got, testCase.expected)
			}
		})
	}
}

func TestEncoderRejectsCommands(t *testing.T) {
	cases := []struct {
		name        string
		alternative bool
		command     *model.Command
		missingKey  string
	}{
		{
			"legacy periodic without frequency",
			false,
			makeCommand(model.CommandPositionPeriodic, nil),
			model.CmdKeyFrequency,
		},
		{
			"legacy output control without data",
			false,
			makeCommand(model.CommandOutputControl, nil),
			model.CmdKeyData,
		},
		{
			"legacy custom without data",
			false,
			makeCommand(model.CommandCustom, nil),
			model.CmdKeyData,
		},
		{
			"alternative connection without server",
			true,
			makeCommand(model.CommandSetConnection, map[string]any{model.CmdKeyPort: "5555"}),
			model.CmdKeyServer,
		},
		{
			"alternative connection without port",
			true,
			makeCommand(model.CommandSetConnection, map[string]any{model.CmdKeyServer: "1.2.3.4"}),
			model.CmdKeyPort,
		},
		{
			"alternative sos number without phone",
			true,
			makeCommand(model.CommandSOSNumber, nil),
			model.CmdKeyPhone,
		},
		{
			"legacy unmodelled type",
			false,
			makeCommand(model.CommandFactoryReset, nil),
			"",
		},
		{
			"alternative legacy only type",
			true,
			makeCommand(model.CommandSetOdometer, nil),
			"",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewEncoder(testBase(testCase.alternative)).EncodeCommand(testCase.command)
			if !errors.Is(err, ErrUnsupportedCommand) {
				t.Fatalf("error = %v, want %v", err, ErrUnsupportedCommand)
			}
			if testCase.missingKey != "" && !strings.Contains(err.Error(), testCase.missingKey) {
				t.Fatalf("error = %v, want key %q", err, testCase.missingKey)
			}
		})
	}
}

func TestEncoderNeedsUniqueIDInLegacyLayout(t *testing.T) {
	command := makeCommand(model.CommandPositionSingle, nil)
	if _, err := NewEncoder(codec.Base{}).EncodeCommand(command); err == nil {
		t.Fatal("expected an error without a unique id provider")
	}
}

func TestEncoderAlternativeNeedsNoUniqueID(t *testing.T) {
	command := makeCommand(model.CommandPositionSingle, nil)
	if _, err := NewEncoder(testBase(true)).EncodeCommand(command); err != nil {
		t.Fatalf("encode: %v", err)
	}
}
