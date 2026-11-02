package ruptela

import (
	"encoding/hex"
	"testing"

	"traccar-server_go/go_server/internal/model"
	. "traccar-server_go/go_server/internal/protocol/protocaltest"
)

func TestEncode(t *testing.T) {
	cases := []struct {
		commandType string
		attributes  map[string]any
		expected    string
	}{
		{model.CommandCustom, map[string]any{model.CmdKeyData: " Setio 2,1"}, "000b6c20536574696f20322c31eb3e"},
		{model.CommandCustom, map[string]any{model.CmdKeyData: "000b890100000000007fffffff89f0"}, "000b890100000000007fffffff89f0"},
	}

	for _, testCase := range cases {
		command := model.NewCommand(1, testCase.commandType)
		for key, value := range testCase.attributes {
			command.SetAttribute(key, value)
		}
		data, err := NewEncoder(Base()).EncodeCommand(command)
		if err != nil {
			t.Fatalf("%v: %v", testCase.commandType, err)
		}
		got := hex.EncodeToString(data)
		if got != testCase.expected {
			t.Fatalf("encode = %s, want %s", got, testCase.expected)
		}
	}
}
