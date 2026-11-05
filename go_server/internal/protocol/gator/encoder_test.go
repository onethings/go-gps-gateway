package gator

import (
	"encoding/hex"
	"errors"
	"testing"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
)

type stubUniqueIDs struct {
	id string
}

func (s stubUniqueIDs) UniqueID(int64) (string, error) {
	return s.id, nil
}

func makeCommand(commandType string, attributes map[string]any) *model.Command {
	command := model.NewCommand(1, commandType)
	for key, value := range attributes {
		command.SetAttribute(key, value)
	}
	return command
}

func TestEncodeCommand(t *testing.T) {
	cases := []struct {
		name     string
		command  *model.Command
		expected string
	}{
		{
			"position single",
			makeCommand(model.CommandPositionSingle, nil),
			"24243000060ca232862c0d",
		},
		{
			"engine stop",
			makeCommand(model.CommandEngineStop, nil),
			"24243900060ca23286250d",
		},
		{
			"engine resume",
			makeCommand(model.CommandEngineResume, nil),
			"24243800060ca23286240d",
		},
		{
			"set speed limit",
			makeCommand(model.CommandSetSpeedLimit, map[string]any{model.CmdKeyData: 30}),
			"24246b00070ca232861e680d",
		},
		{
			"set odometer",
			makeCommand(model.CommandSetOdometer, map[string]any{model.CmdKeyData: 1000}),
			"24243f00080ca2328603e8c60d",
		},
	}

	encoder := NewEncoder(codec.Base{UniqueIDs: stubUniqueIDs{id: "13512345006"}})
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			data, err := encoder.EncodeCommand(testCase.command)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got := hex.EncodeToString(data); got != testCase.expected {
				t.Fatalf("encode = %s, want %s", got, testCase.expected)
			}
		})
	}
}

func TestEncoderRejectsCommands(t *testing.T) {
	encoder := NewEncoder(codec.Base{UniqueIDs: stubUniqueIDs{id: "13512345006"}})

	if _, err := encoder.EncodeCommand(makeCommand(model.CommandRebootDevice, nil)); !errors.Is(err, ErrUnsupportedCommand) {
		t.Fatalf("error = %v, want %v", err, ErrUnsupportedCommand)
	}
}

func TestEncoderNeedsUsableIdentifier(t *testing.T) {
	single := makeCommand(model.CommandPositionSingle, nil)

	if _, err := NewEncoder(codec.Base{}).EncodeCommand(single); err == nil {
		t.Fatal("expected an error without a unique id provider")
	}

	encoder := NewEncoder(codec.Base{UniqueIDs: stubUniqueIDs{id: "12345"}})
	if _, err := encoder.EncodeCommand(single); err == nil {
		t.Fatal("expected an error for a short unique id")
	}
}
