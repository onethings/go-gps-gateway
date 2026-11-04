package tk103

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands TK103 cannot express.
var ErrUnsupportedCommand = errors.New("tk103: unsupported command")

// defaultPassword is the factory password the alternative SMS2 layout carries.
const defaultPassword = "123456"

// Encoder turns high level commands into TK103 text sentences.
type Encoder struct {
	codec.Base
}

// NewEncoder creates a TK103 encoder.
func NewEncoder(base codec.Base) *Encoder {
	return &Encoder{Base: base}
}

// EncodeCommand implements codec.Encoder. The alternative setting selects the
// SMS2 layout; without it the encoder writes the legacy parenthesised
// sentences.
func (e *Encoder) EncodeCommand(command *model.Command) ([]byte, error) {
	if e.alternative() {
		return e.encodeAlternative(command)
	}
	return e.encodeLegacy(command)
}

func (e *Encoder) encodeAlternative(command *model.Command) ([]byte, error) {
	var content string
	switch command.Type {
	case model.CommandCustom:
		data, err := attributeString(command, model.CmdKeyData)
		if err != nil {
			return nil, err
		}
		content = data
	case model.CommandGetVersion:
		content = "*about*"
	case model.CommandPowerOff:
		content = "*turnoff*"
	case model.CommandRebootDevice:
		content = "88888888"
	case model.CommandPositionSingle:
		content = "*getposl*"
	case model.CommandPositionPeriodic:
		content = "*routetrack*99*"
	case model.CommandPositionStop:
		content = "*routetrackoff*"
	case model.CommandGetDeviceStatus:
		content = "*status*"
	case model.CommandIdentification:
		content = "999999"
	case model.CommandModeDeepSleep:
		if command.Attributes.GetBoolean(model.CmdKeyEnable) {
			content = "*sleep*2*"
		} else {
			content = "*sleepoff*"
		}
	case model.CommandModePowerSaving:
		if command.Attributes.GetBoolean(model.CmdKeyEnable) {
			content = "*sleepv*"
		} else {
			content = "*sleepoff*"
		}
	case model.CommandAlarmSOS:
		if command.Attributes.GetBoolean(model.CmdKeyEnable) {
			content = "*soson*"
		} else {
			content = "*sosoff*"
		}
	case model.CommandSetConnection:
		server, err := attributeString(command, model.CmdKeyServer)
		if err != nil {
			return nil, err
		}
		port, err := attributeString(command, model.CmdKeyPort)
		if err != nil {
			return nil, err
		}
		content = "*setip*" + strings.ReplaceAll(server, ".", "*") + "*" + port + "*"
	case model.CommandSOSNumber:
		phone, err := attributeString(command, model.CmdKeyPhone)
		if err != nil {
			return nil, err
		}
		content = "*master*" + e.devicePassword(command) + "*" + phone + "*"
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	}
	return []byte("[begin]sms2," + content + ",[end]"), nil
}

func (e *Encoder) encodeLegacy(command *model.Command) ([]byte, error) {
	uniqueID, err := e.uniqueID(command.DeviceID)
	if err != nil {
		return nil, err
	}

	var content string
	switch command.Type {
	case model.CommandCustom:
		data, err := attributeString(command, model.CmdKeyData)
		if err != nil {
			return nil, err
		}
		content = data
	case model.CommandGetVersion:
		content = "AP07"
	case model.CommandRebootDevice:
		content = "AT00"
	case model.CommandSetOdometer:
		content = "AX01"
	case model.CommandPositionSingle:
		content = "AP00"
	case model.CommandPositionPeriodic:
		frequency, err := attributeInt(command, model.CmdKeyFrequency)
		if err != nil {
			return nil, err
		}
		content = fmt.Sprintf("AR00%04X0000", frequency)
	case model.CommandPositionStop:
		content = "AR0000000000"
	case model.CommandEngineStop:
		content = "AV010"
	case model.CommandEngineResume:
		content = "AV011"
	case model.CommandOutputControl:
		data, err := attributeString(command, model.CmdKeyData)
		if err != nil {
			return nil, err
		}
		content = "AV00" + data
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	}
	return []byte("(" + uniqueID + content + ")"), nil
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

func (e *Encoder) devicePassword(command *model.Command) string {
	if command.Attributes.Has(model.CmdKeyDevicePassword) {
		if password := command.Attributes.GetString(model.CmdKeyDevicePassword, ""); password != "" {
			return password
		}
	}
	if e.Config != nil {
		if password := e.Config(config.ProtocolDevicePassword); password != "" {
			return password
		}
	}
	return defaultPassword
}

func (e *Encoder) uniqueID(deviceID int64) (string, error) {
	if e.UniqueIDs == nil {
		return "", errors.New("tk103: no unique id provider configured")
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
