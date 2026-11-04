package meitrack

import (
	"errors"
	"fmt"
	"strings"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands Meitrack cannot express.
var ErrUnsupportedCommand = errors.New("meitrack: unsupported command")

// Encoder turns high level commands into Meitrack frames.
type Encoder struct {
	codec.Base
}

// NewEncoder creates a Meitrack encoder.
func NewEncoder(base codec.Base) *Encoder {
	return &Encoder{Base: base}
}

// EncodeCommand implements codec.Encoder.
func (e *Encoder) EncodeCommand(command *model.Command) ([]byte, error) {
	uniqueID, err := e.uniqueID(command.DeviceID)
	if err != nil {
		return nil, err
	}

	switch command.Type {
	case model.CommandCustom:
		data, err := attributeString(command, model.CmdKeyData)
		if err != nil {
			return nil, err
		}
		return formatCommand(uniqueID, data), nil
	case model.CommandPositionSingle:
		return formatCommand(uniqueID, "A10"), nil
	case model.CommandEngineStop:
		return formatCommand(uniqueID, "C01,0,12222"), nil
	case model.CommandEngineResume:
		return formatCommand(uniqueID, "C01,0,02222"), nil
	case model.CommandAlarmArm:
		if e.alternative() {
			return formatCommand(uniqueID, "B21,1"), nil
		}
		return formatCommand(uniqueID, "C01,0,22122"), nil
	case model.CommandAlarmDisarm:
		if e.alternative() {
			return formatCommand(uniqueID, "B21,0"), nil
		}
		return formatCommand(uniqueID, "C01,0,22022"), nil
	case model.CommandRequestPhoto:
		index := command.Attributes.GetInteger(model.CmdKeyIndex, 0)
		if index <= 0 {
			index = 1
		}
		return formatCommand(uniqueID, fmt.Sprintf("D03,%d,camera_picture.jpg", index)), nil
	case model.CommandSendSMS:
		phone, err := attributeString(command, model.CmdKeyPhone)
		if err != nil {
			return nil, err
		}
		message, err := attributeString(command, model.CmdKeyMessage)
		if err != nil {
			return nil, err
		}
		return formatCommand(uniqueID, "C02,0,"+phone+","+message), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	}
}

func (e *Encoder) uniqueID(deviceID int64) (string, error) {
	if e.UniqueIDs == nil {
		return "", errors.New("meitrack: no unique id provider configured")
	}
	return e.UniqueIDs.UniqueID(deviceID)
}

func (e *Encoder) alternative() bool {
	if e.Config == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(e.Config(config.ProtocolAlternative))) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// formatCommand builds the reference frame: "@@A" and a length, the unique id,
// the content, an ASCII sum checksum and CRLF.
func formatCommand(uniqueID, content string) []byte {
	length := 1 + len(uniqueID) + 1 + len(content) + 5
	result := fmt.Sprintf("@@A%02d,%s,%s*", length, uniqueID, content)
	var checksum byte
	for index := 0; index < len(result); index++ {
		checksum += result[index]
	}
	return []byte(fmt.Sprintf("%s%02X\r\n", result, checksum))
}

func attributeString(command *model.Command, key string) (string, error) {
	if !command.Attributes.Has(key) {
		return "", fmt.Errorf("%w: missing %s", ErrUnsupportedCommand, key)
	}
	value := command.Attributes.GetString(key, "")
	if value == "" {
		return "", fmt.Errorf("%w: empty %s", ErrUnsupportedCommand, key)
	}
	return value, nil
}

var _ codec.Encoder = (*Encoder)(nil)
