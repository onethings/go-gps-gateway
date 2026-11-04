package gps103

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands GPS103 cannot express.
var ErrUnsupportedCommand = errors.New("gps103: unsupported command")

// Encoder turns high level commands into GPS103 text sentences.
type Encoder struct {
	codec.Base
}

// NewEncoder creates a GPS103 encoder.
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
		return sentence(uniqueID, data), nil
	case model.CommandPositionStop:
		return sentence(uniqueID, "D"), nil
	case model.CommandPositionSingle:
		return sentence(uniqueID, "B"), nil
	case model.CommandPositionPeriodic:
		frequency, err := attributeInt(command, model.CmdKeyFrequency)
		if err != nil {
			return nil, err
		}
		return sentence(uniqueID, "C,"+formatFrequency(frequency)), nil
	case model.CommandEngineStop:
		return sentence(uniqueID, "J"), nil
	case model.CommandEngineResume:
		return sentence(uniqueID, "K"), nil
	case model.CommandAlarmArm:
		return sentence(uniqueID, "L"), nil
	case model.CommandAlarmDisarm:
		return sentence(uniqueID, "M"), nil
	case model.CommandRequestPhoto:
		return sentence(uniqueID, "160"), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	}
}

func (e *Encoder) uniqueID(deviceID int64) (string, error) {
	if e.UniqueIDs == nil {
		return "", errors.New("gps103: no unique id provider configured")
	}
	return e.UniqueIDs.UniqueID(deviceID)
}

func sentence(uniqueID, content string) []byte {
	return []byte(fmt.Sprintf("**,imei:%s,%s", uniqueID, content))
}

// formatFrequency renders the reporting interval in the unit the reference
// encoder picks: whole hours, then whole minutes, then seconds.
func formatFrequency(frequency int) string {
	if frequency/60/60 > 0 {
		return fmt.Sprintf("%02dh", frequency/60/60)
	}
	if frequency/60 > 0 {
		return fmt.Sprintf("%02dm", frequency/60)
	}
	return fmt.Sprintf("%02ds", frequency)
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
