package jt808

import (
	"encoding/hex"
	"errors"
	"strconv"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands JT808 cannot express.
var ErrUnsupportedCommand = errors.New("jt808: unsupported command")

// Encoder builds JT808 downlink messages.
type Encoder struct {
	codec.Base
}

// NewEncoder creates a JT808 encoder.
func NewEncoder(base codec.Base) *Encoder {
	return &Encoder{Base: base}
}

// EncodeCommand implements codec.Encoder.
func (e *Encoder) EncodeCommand(command *model.Command) ([]byte, error) {
	if e.UniqueIDs == nil {
		return nil, ErrUnsupportedCommand
	}
	uniqueID, err := e.UniqueIDs.UniqueID(command.DeviceID)
	if err != nil {
		return nil, err
	}
	id, err := encodeID(uniqueID, 6)
	if err != nil {
		return nil, err
	}
	h := &header{delimiter: 0x7e, id: id}

	switch command.Type {
	case model.CommandPositionSingle:
		return buildMessage(h, msgLocationQuery, nil), nil

	case model.CommandPositionStop:
		payload := []byte{0, 0, 0, 0, 0, 0}
		return buildMessage(h, msgTemporaryTracking, payload), nil

	case model.CommandRebootDevice:
		payload := []byte{1, 0x23, 1, 0x03}
		return buildMessage(h, msgParameterSetting, payload), nil

	case model.CommandPositionPeriodic:
		frequency, ok := command.GetAttribute(model.CmdKeyFrequency)
		if !ok {
			return nil, ErrUnsupportedCommand
		}
		seconds, err := intValue(frequency)
		if err != nil {
			return nil, err
		}
		payload := []byte{1, 0x06, 4,
			byte(seconds >> 24), byte(seconds >> 16), byte(seconds >> 8), byte(seconds)}
		return buildMessage(h, msgParameterSetting, payload), nil

	case model.CommandAlarmArm, model.CommandAlarmDisarm:
		flag := byte(0x00)
		if command.Type == model.CommandAlarmArm {
			flag = 0x01
		}
		payload := []byte{1, 0x24, 1 + 4, flag, 'u', 's', 'e', 'r'}
		return buildMessage(h, msgParameterSetting, payload), nil

	case model.CommandAlarmDismiss:
		return buildMessage(h, msgAlarmAck, []byte{0, 0, 0, 0, 0, 0}), nil

	case model.CommandEngineStop, model.CommandEngineResume:
		flag := byte(0xf1)
		if command.Type == model.CommandEngineStop {
			flag = 0xf0
		}
		return buildMessage(h, msgTerminalControl, []byte{flag}), nil

	case model.CommandCustom:
		data, ok := command.GetAttribute(model.CmdKeyData)
		if !ok {
			return nil, ErrUnsupportedCommand
		}
		text, ok := data.(string)
		if !ok {
			return nil, ErrUnsupportedCommand
		}
		if !isHexString(text) {
			return nil, ErrUnsupportedCommand
		}
		return hex.DecodeString(text)
	}

	return nil, ErrUnsupportedCommand
}

func intValue(value any) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	case string:
		return strconv.Atoi(v)
	}
	return 0, ErrUnsupportedCommand
}

func isHexString(value string) bool {
	if len(value) == 0 || len(value)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

var _ codec.Encoder = (*Encoder)(nil)
