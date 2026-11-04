package h02

import (
	"errors"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/protocol/protocaltest"
)

// testBase pins the clock so every assertion sees the same HHMMSS field.
func testBase(options ...func(*codec.Base)) codec.Base {
	base := codec.Base{
		UniqueIDs: protocaltest.NewStubUniqueIDs(),
		Now:       func() time.Time { return time.Date(2026, 1, 2, 1, 2, 3, 0, time.Local) },
	}
	for _, option := range options {
		option(&base)
	}
	return base
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

func TestEncoder(t *testing.T) {
	cases := []struct {
		name     string
		command  *model.Command
		config   map[string]string
		expected string
	}{
		{
			"alarm arm",
			makeCommand(model.CommandAlarmArm, nil), nil,
			"*HQ,123456789012345,SCF,010203,0,0#",
		},
		{
			"alarm disarm",
			makeCommand(model.CommandAlarmDisarm, nil), nil,
			"*HQ,123456789012345,SCF,010203,1,1#",
		},
		{
			"engine stop",
			makeCommand(model.CommandEngineStop, nil), nil,
			"*HQ,123456789012345,S20,010203,1,1#",
		},
		{
			"engine resume",
			makeCommand(model.CommandEngineResume, nil), nil,
			"*HQ,123456789012345,S20,010203,1,0#",
		},
		{
			"periodic",
			makeCommand(model.CommandPositionPeriodic, map[string]any{model.CmdKeyFrequency: 60}), nil,
			"*HQ,123456789012345,S71,010203,22,60#",
		},
		{
			"periodic alternative",
			makeCommand(model.CommandPositionPeriodic, map[string]any{model.CmdKeyFrequency: 60}),
			map[string]string{config.ProtocolAlternative: "true"},
			"*HQ,123456789012345,D1,010203,60#",
		},
		{
			"custom",
			makeCommand(model.CommandCustom, map[string]any{
				model.CmdKeyData: "*HQ,123456789012345,S18,010203,60#",
			}), nil,
			"*HQ,123456789012345,S18,010203,60#",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			base := testBase()
			if testCase.config != nil {
				withConfig(testCase.config)(&base)
			}
			data, err := NewEncoder(base).EncodeCommand(testCase.command)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got := string(data); got != testCase.expected {
				t.Fatalf("encode = %q, want %q", got, testCase.expected)
			}
		})
	}
}

func TestEncoderCustomNeedsNoUniqueID(t *testing.T) {
	command := makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: "RAW"})
	data, err := NewEncoder(codec.Base{}).EncodeCommand(command)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if string(data) != "RAW" {
		t.Fatalf("encode = %q", data)
	}
}

func TestEncoderRejectsCommands(t *testing.T) {
	cases := []struct {
		name    string
		command *model.Command
	}{
		{"unmodelled type", makeCommand(model.CommandRebootDevice, nil)},
		{
			"periodic without frequency",
			makeCommand(model.CommandPositionPeriodic, nil),
		},
		{"custom without data", makeCommand(model.CommandCustom, nil)},
		{
			"custom with empty data",
			makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: ""}),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewEncoder(testBase()).EncodeCommand(testCase.command); !errors.Is(err, ErrUnsupportedCommand) {
				t.Fatalf("error = %v, want %v", err, ErrUnsupportedCommand)
			}
		})
	}
}
