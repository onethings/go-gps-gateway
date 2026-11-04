package meitrack

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

func TestEncoderVectors(t *testing.T) {
	cases := []struct {
		name     string
		command  *model.Command
		expected string
	}{
		{
			"position single",
			makeCommand(model.CommandPositionSingle, nil),
			"@@A25,123456789012345,A10*58\r\n",
		},
		{
			"request photo",
			makeCommand(model.CommandRequestPhoto, nil),
			"@@A46,123456789012345,D03,1,camera_picture.jpg*1C\r\n",
		},
		{
			"send sms",
			makeCommand(model.CommandSendSMS, map[string]any{
				model.CmdKeyPhone:   "15360853789",
				model.CmdKeyMessage: "Meitrack",
			}),
			"@@A48,123456789012345,C02,0,15360853789,Meitrack*8B\r\n",
		},
		{
			"engine stop",
			makeCommand(model.CommandEngineStop, nil),
			"@@A33,123456789012345,C01,0,12222*DA\r\n",
		},
		{
			"engine resume",
			makeCommand(model.CommandEngineResume, nil),
			"@@A33,123456789012345,C01,0,02222*D9\r\n",
		},
		{
			"custom",
			makeCommand(model.CommandCustom, map[string]any{model.CmdKeyData: "H,080"}),
			"@@A27,123456789012345,H,080*C4\r\n",
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

func TestEncoderAlarmLayouts(t *testing.T) {
	cases := []struct {
		name        string
		alternative bool
		command     *model.Command
		expected    string
	}{
		{
			"arm legacy",
			false,
			makeCommand(model.CommandAlarmArm, nil),
			"@@A33,123456789012345,C01,0,22122*DA\r\n",
		},
		{
			"disarm legacy",
			false,
			makeCommand(model.CommandAlarmDisarm, nil),
			"@@A33,123456789012345,C01,0,22022*D9\r\n",
		},
		{
			"arm alternative",
			true,
			makeCommand(model.CommandAlarmArm, nil),
			"@@A27,123456789012345,B21,1*BA\r\n",
		},
		{
			"disarm alternative",
			true,
			makeCommand(model.CommandAlarmDisarm, nil),
			"@@A27,123456789012345,B21,0*B9\r\n",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			data, err := NewEncoder(testBase(testCase.alternative)).EncodeCommand(testCase.command)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got := string(data); got != testCase.expected {
				t.Fatalf("encode = %q, want %q", got, testCase.expected)
			}
		})
	}
}

func TestEncoderRequestPhotoIndex(t *testing.T) {
	command := makeCommand(model.CommandRequestPhoto, map[string]any{model.CmdKeyIndex: 2})
	data, err := NewEncoder(testBase(false)).EncodeCommand(command)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(data), "D03,2,camera_picture.jpg") {
		t.Fatalf("encode = %q", data)
	}
}

func TestEncoderRejectsCommands(t *testing.T) {
	cases := []struct {
		name       string
		command    *model.Command
		missingKey string
	}{
		{
			"custom without data",
			makeCommand(model.CommandCustom, nil),
			model.CmdKeyData,
		},
		{
			"send sms without phone",
			makeCommand(model.CommandSendSMS, map[string]any{model.CmdKeyMessage: "Meitrack"}),
			model.CmdKeyPhone,
		},
		{
			"send sms without message",
			makeCommand(model.CommandSendSMS, map[string]any{model.CmdKeyPhone: "15360853789"}),
			model.CmdKeyMessage,
		},
		{
			"unmodelled type",
			makeCommand(model.CommandRebootDevice, nil),
			"",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewEncoder(testBase(false)).EncodeCommand(testCase.command)
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
	if _, err := NewEncoder(codec.Base{}).EncodeCommand(makeCommand(model.CommandPositionSingle, nil)); err == nil {
		t.Fatal("expected an error without a unique id provider")
	}
}
