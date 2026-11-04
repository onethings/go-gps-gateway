package suntech

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands Suntech cannot express.
var ErrUnsupportedCommand = errors.New("suntech: unsupported command")

// Encoder turns high level commands into the legacy Suntech text sentences.
type Encoder struct {
	codec.Base
}

// NewEncoder creates a Suntech encoder.
func NewEncoder(base codec.Base) *Encoder {
	return &Encoder{Base: base}
}

// EncodeCommand implements codec.Encoder. The reference encoder reads the
// universal flag and live prefix from the connection decoder; the Go router
// builds the encoder per command, so it uses the documented legacy prefix.
func (e *Encoder) EncodeCommand(command *model.Command) ([]byte, error) {
	uniqueID, err := e.uniqueID(command.DeviceID)
	if err != nil {
		return nil, err
	}

	var content string
	switch command.Type {
	case model.CommandRebootDevice:
		content = "02;Reboot"
	case model.CommandPositionSingle:
		content = "02;StatusReq"
	case model.CommandOutputControl:
		data, err := attributeString(command, model.CmdKeyData)
		if err != nil {
			return nil, err
		}
		index, err := attributeInt(command, model.CmdKeyIndex)
		if err != nil {
			return nil, err
		}
		action := "Disable"
		if data == "1" {
			action = "Enable"
		}
		content = "02;" + action + strconv.Itoa(index)
	case model.CommandEngineStop:
		content = "02;Enable1"
	case model.CommandEngineResume:
		content = "02;Disable1"
	case model.CommandAlarmArm:
		content = "02;Enable2"
	case model.CommandAlarmDisarm:
		content = "02;Disable2"
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	}
	return []byte(defaultPrefix + "CMD;" + uniqueID + ";" + content + "\r"), nil
}

func (e *Encoder) uniqueID(deviceID int64) (string, error) {
	if e.UniqueIDs == nil {
		return "", errors.New("suntech: no unique id provider configured")
	}
	return e.UniqueIDs.UniqueID(deviceID)
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

func attributeInt(command *model.Command, key string) (int, error) {
	value, ok := command.GetAttribute(key)
	if !ok {
		return 0, fmt.Errorf("%w: missing %s", ErrUnsupportedCommand, key)
	}
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case float64:
		return int(typed), nil
	case string:
		number, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, fmt.Errorf("%w: %s is not a number", ErrUnsupportedCommand, key)
		}
		return number, nil
	}
	return 0, fmt.Errorf("%w: %s is not a number", ErrUnsupportedCommand, key)
}

var _ codec.Encoder = (*Encoder)(nil)
