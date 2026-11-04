package startek

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
			"custom",
			makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: "123456"}),
			"$$:23,123456789012345,12345680\r\n",
		},
		{
			"output control",
			makeCommand(model.CommandOutputControl, map[string]any{model.CmdKeyIndex: 1, model.CmdKeyData: 1}),
			"$$:24,123456789012345,900,1,19F\r\n",
		},
		{
			// The reference encoder sends the same sentence for both.
			"engine stop",
			makeCommand(model.CommandEngineStop, nil),
			"$$:24,123456789012345,900,1,19F\r\n",
		},
		{
			"engine resume",
			makeCommand(model.CommandEngineResume, nil),
			"$$:24,123456789012345,900,1,09E\r\n",
		},
		{
			"output control second output",
			makeCommand(model.CommandOutputControl, map[string]any{model.CmdKeyIndex: 2, model.CmdKeyData: 0}),
			"$$:24,123456789012345,900,2,09F\r\n",
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
	encoder := NewEncoder(testBase())

	if _, err := encoder.EncodeCommand(makeCommand(model.CommandRebootDevice, nil)); !errors.Is(err, ErrUnsupportedCommand) {
		t.Fatalf("error = %v, want %v", err, ErrUnsupportedCommand)
	}
}

func TestEncoderNeedsUniqueID(t *testing.T) {
	if _, err := NewEncoder(codec.Base{}).EncodeCommand(makeCommand(model.CommandEngineStop, nil)); err == nil {
		t.Fatal("expected an error without a unique id provider")
	}
}
