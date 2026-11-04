package suntech

import (
	"errors"
	"strings"
	"testing"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/protocol/protocaltest"
)

func testBase() codec.Base {
	return codec.Base{UniqueIDs: protocaltest.NewStubUniqueIDs()}
}

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
			"reboot",
			makeCommand(model.CommandRebootDevice, nil),
			"SA200CMD;123456789012345;02;Reboot\r",
		},
		{
			"position single",
			makeCommand(model.CommandPositionSingle, nil),
			"SA200CMD;123456789012345;02;StatusReq\r",
		},
		{
			"output control enable",
			makeCommand(model.CommandOutputControl, map[string]any{
				model.CmdKeyData:  "1",
				model.CmdKeyIndex: 2,
			}),
			"SA200CMD;123456789012345;02;Enable2\r",
		},
		{
			"output control disable",
			makeCommand(model.CommandOutputControl, map[string]any{
				model.CmdKeyData:  "0",
				model.CmdKeyIndex: 2,
			}),
			"SA200CMD;123456789012345;02;Disable2\r",
		},
		{
			"engine stop",
			makeCommand(model.CommandEngineStop, nil),
			"SA200CMD;123456789012345;02;Enable1\r",
		},
		{
			"engine resume",
			makeCommand(model.CommandEngineResume, nil),
			"SA200CMD;123456789012345;02;Disable1\r",
		},
		{
			"alarm arm",
			makeCommand(model.CommandAlarmArm, nil),
			"SA200CMD;123456789012345;02;Enable2\r",
		},
		{
			"alarm disarm",
			makeCommand(model.CommandAlarmDisarm, nil),
			"SA200CMD;123456789012345;02;Disable2\r",
		},
	}

	encoder := NewEncoder(testBase())
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
		name       string
		command    *model.Command
		missingKey string
	}{
		{
			"output control without data",
			makeCommand(model.CommandOutputControl, map[string]any{model.CmdKeyIndex: 1}),
			model.CmdKeyData,
		},
		{
			"output control without index",
			makeCommand(model.CommandOutputControl, map[string]any{model.CmdKeyData: "1"}),
			model.CmdKeyIndex,
		},
		{
			"unmodelled type",
			makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: "x"}),
			"",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewEncoder(testBase()).EncodeCommand(testCase.command)
			if !errors.Is(err, ErrUnsupportedCommand) {
				t.Fatalf("error = %v, want %v", err, ErrUnsupportedCommand)
			}
			if testCase.missingKey != "" && !strings.Contains(err.Error(), testCase.missingKey) {
				t.Fatalf("error = %v, want key %q", err, testCase.missingKey)
			}
		})
	}
}

func TestEncoderNeedsUniqueID(t *testing.T) {
	if _, err := NewEncoder(codec.Base{}).EncodeCommand(makeCommand(model.CommandRebootDevice, nil)); err == nil {
		t.Fatal("expected an error without a unique id provider")
	}
}
