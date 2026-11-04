package gps103

import (
	"errors"
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
			"position periodic",
			makeCommand(model.CommandPositionPeriodic, map[string]any{model.CmdKeyFrequency: 300}),
			"**,imei:123456789012345,C,05m",
		},
		{
			"custom",
			makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: "H,080"}),
			"**,imei:123456789012345,H,080",
		},
		{
			"position stop",
			makeCommand(model.CommandPositionStop, nil),
			"**,imei:123456789012345,D",
		},
		{
			"position single",
			makeCommand(model.CommandPositionSingle, nil),
			"**,imei:123456789012345,B",
		},
		{
			"engine stop",
			makeCommand(model.CommandEngineStop, nil),
			"**,imei:123456789012345,J",
		},
		{
			"engine resume",
			makeCommand(model.CommandEngineResume, nil),
			"**,imei:123456789012345,K",
		},
		{
			"alarm arm",
			makeCommand(model.CommandAlarmArm, nil),
			"**,imei:123456789012345,L",
		},
		{
			"alarm disarm",
			makeCommand(model.CommandAlarmDisarm, nil),
			"**,imei:123456789012345,M",
		},
		{
			"request photo",
			makeCommand(model.CommandRequestPhoto, nil),
			"**,imei:123456789012345,160",
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

func TestFormatFrequency(t *testing.T) {
	cases := []struct {
		frequency int
		expected  string
	}{
		{30, "30s"},
		{60, "01m"},
		{300, "05m"},
		{3600, "01h"},
		{7200, "02h"},
	}

	for _, testCase := range cases {
		if got := formatFrequency(testCase.frequency); got != testCase.expected {
			t.Errorf("formatFrequency(%d) = %q, want %q", testCase.frequency, got, testCase.expected)
		}
	}
}

func TestEncoderRejectsCommands(t *testing.T) {
	cases := []struct {
		name    string
		command *model.Command
	}{
		{"unmodelled type", makeCommand(model.CommandRebootDevice, nil)},
		{"periodic without frequency", makeCommand(model.CommandPositionPeriodic, nil)},
		{"custom without data", makeCommand(model.CommandCustom, nil)},
		{"custom with empty data", makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: ""})},
	}

	encoder := NewEncoder(testBase())
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := encoder.EncodeCommand(testCase.command); !errors.Is(err, ErrUnsupportedCommand) {
				t.Fatalf("error = %v, want %v", err, ErrUnsupportedCommand)
			}
		})
	}
}

func TestEncoderNeedsUniqueID(t *testing.T) {
	if _, err := NewEncoder(codec.Base{}).EncodeCommand(makeCommand(model.CommandPositionSingle, nil)); err == nil {
		t.Fatal("expected an error without a unique id provider")
	}
}
