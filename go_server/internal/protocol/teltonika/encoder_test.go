package teltonika

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

func makeCommand(commandType string, attributes map[string]any) *model.Command {
	command := model.NewCommand(1, commandType)
	for key, value := range attributes {
		command.SetAttribute(key, value)
	}
	return command
}

func TestEncoderCommandVectors(t *testing.T) {
	cases := []struct {
		name     string
		command  *model.Command
		expected string
	}{
		{
			"custom data",
			makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: "setdigout 11"}),
			"00000000000000140c01050000000c7365746469676f7574203131010000bed5",
		},
		{
			"custom hex",
			makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: "03030000000185E8"}),
			"00000000000000100c01050000000803030000000185e8010000da8b",
		},
		{
			"engine stop",
			makeCommand(model.CommandEngineStop, nil),
			"00000000000000130c01050000000b7365746469676f7574203101000087a2",
		},
		{
			"engine resume",
			makeCommand(model.CommandEngineResume, nil),
			"00000000000000130c01050000000b7365746469676f7574203001000017a3",
		},
	}

	encoder := NewEncoder(codec.Base{})
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			frame, err := encoder.EncodeCommand(testCase.command)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got := hex.EncodeToString(frame); got != testCase.expected {
				t.Fatalf("frame = %s, want %s", got, testCase.expected)
			}
		})
	}
}

// checkFrame walks the Codec 12 packet the way a device parses it.
func checkFrame(t *testing.T, frame []byte, content []byte) {
	t.Helper()
	if len(frame) != 4+4+3+4+len(content)+1+4 {
		t.Fatalf("frame length = %d, want %d", len(frame), 4+4+3+4+len(content)+1+4)
	}
	if !bytes.Equal(frame[:4], []byte{0, 0, 0, 0}) {
		t.Fatalf("preamble = %x", frame[:4])
	}
	if got := binary.BigEndian.Uint32(frame[4:8]); got != uint32(len(content)+8) {
		t.Fatalf("length = %d, want %d", got, len(content)+8)
	}
	if frame[8] != 0x0c || frame[9] != 1 || frame[10] != 5 {
		t.Fatalf("codec block = %x, want 0c0105", frame[8:11])
	}
	if got := binary.BigEndian.Uint32(frame[11:15]); got != uint32(len(content)) {
		t.Fatalf("content length = %d, want %d", got, len(content))
	}
	if !bytes.Equal(frame[15:15+len(content)], content) {
		t.Fatalf("content = %x, want %x", frame[15:15+len(content)], content)
	}
	if frame[15+len(content)] != 1 {
		t.Fatalf("quantity = %d, want 1", frame[15+len(content)])
	}
	if got, want := binary.BigEndian.Uint32(frame[len(frame)-4:]), uint32(helper.CRC16IBM(frame[8:len(frame)-4])); got != want {
		t.Fatalf("crc = %08x, want %08x", got, want)
	}
}

func TestEncoderFrameLayout(t *testing.T) {
	frame, err := NewEncoder(codec.Base{}).EncodeCommand(makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: "setdigout 11"}))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	checkFrame(t, frame, []byte("setdigout 11"))
}

func TestEncoderCustomDataTypes(t *testing.T) {
	cases := []struct {
		name    string
		data    any
		content []byte
	}{
		{"integer", 123, []byte("123")},
		{"odd length hex", "ABC", []byte("ABC")},
	}

	encoder := NewEncoder(codec.Base{})
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			frame, err := encoder.EncodeCommand(makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: testCase.data}))
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			checkFrame(t, frame, testCase.content)
		})
	}
}

func TestEncoderUnsupported(t *testing.T) {
	cases := []struct {
		name    string
		command *model.Command
	}{
		{"custom without data", makeCommand(model.CommandCustom, nil)},
		{"alarm arm", makeCommand(model.CommandAlarmArm, nil)},
	}

	encoder := NewEncoder(codec.Base{})
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := encoder.EncodeCommand(testCase.command); !errors.Is(err, ErrUnsupportedCommand) {
				t.Fatalf("err = %v, want ErrUnsupportedCommand", err)
			}
		})
	}
}
