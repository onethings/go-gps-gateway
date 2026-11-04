package wondex

import (
	"errors"
	"testing"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/model"
)

func makeCommand(commandType string, attributes map[string]any) *model.Command {
	command := model.NewCommand(1, commandType)
	for key, value := range attributes {
		command.SetAttribute(key, value)
	}
	return command
}

func TestEncoderVectors(t *testing.T) {
	cases := []struct {
		name     string
		command  *model.Command
		expected string
	}{
		{
			"position single",
			makeCommand(model.CommandPositionSingle, map[string]any{model.CmdKeyDevicePassword: "0000"}),
			"$WP+GETLOCATION=0000",
		},
		{"reboot", makeCommand(model.CommandRebootDevice, nil), "$WP+REBOOT=0000"},
		{"device status", makeCommand(model.CommandGetDeviceStatus, nil), "$WP+TEST=0000"},
		{"modem status", makeCommand(model.CommandGetModemStatus, nil), "$WP+GSMINFO=0000"},
		{"identification", makeCommand(model.CommandIdentification, nil), "$WP+IMEI=0000"},
		{"get version", makeCommand(model.CommandGetVersion, nil), "$WP+VER=0000"},
	}

	encoder := NewEncoder(codec.Base{})
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

func TestEncoderPasswordSources(t *testing.T) {
	base := codec.Base{Config: func(key string) string {
		if key == config.ProtocolDevicePassword {
			return "1111"
		}
		return ""
	}}
	data, err := NewEncoder(base).EncodeCommand(makeCommand(model.CommandPositionSingle, nil))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if got := string(data); got != "$WP+GETLOCATION=1111" {
		t.Fatalf("encode = %q", got)
	}

	command := makeCommand(model.CommandPositionSingle, map[string]any{model.CmdKeyDevicePassword: "2222"})
	data, err = NewEncoder(base).EncodeCommand(command)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if got := string(data); got != "$WP+GETLOCATION=2222" {
		t.Fatalf("encode = %q", got)
	}
}

func TestEncoderRejectsCommands(t *testing.T) {
	if _, err := NewEncoder(codec.Base{}).EncodeCommand(makeCommand(model.CommandEngineStop, nil)); !errors.Is(err, ErrUnsupportedCommand) {
		t.Fatalf("error = %v, want %v", err, ErrUnsupportedCommand)
	}
}
