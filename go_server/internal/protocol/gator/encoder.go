package gator

import (
	"errors"
	"fmt"
	"strconv"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands Gator cannot express.
var ErrUnsupportedCommand = errors.New("gator: unsupported command")

// The message types the encoder writes. Their Java names describe different
// commands than the ones mapped to them; the mapping below keeps that quirk.
const (
	msgPositionRequest = 0x30
	msgOverspeedAlarm  = 0x3F
	msgResetMileage    = 0x6B
	msgRestoreOilDuct  = 0x38
	msgCloseOilDuct    = 0x39
)

// Encoder turns high level commands into Gator frames.
type Encoder struct {
	codec.Base
}

// NewEncoder creates a Gator encoder.
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
	case model.CommandPositionSingle:
		return encodeContent(uniqueID, msgPositionRequest, nil)
	case model.CommandEngineStop:
		return encodeContent(uniqueID, msgCloseOilDuct, nil)
	case model.CommandEngineResume:
		return encodeContent(uniqueID, msgRestoreOilDuct, nil)
	case model.CommandSetSpeedLimit:
		data := command.Attributes.GetInteger(model.CmdKeyData, 0)
		return encodeContent(uniqueID, msgResetMileage, []byte{byte(data)})
	case model.CommandSetOdometer:
		data := command.Attributes.GetInteger(model.CmdKeyData, 0)
		return encodeContent(uniqueID, msgOverspeedAlarm, []byte{byte(data >> 8), byte(data)})
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	}
}

func (e *Encoder) uniqueID(deviceID int64) (string, error) {
	if e.UniqueIDs == nil {
		return "", errors.New("gator: no unique id provider configured")
	}
	return e.UniqueIDs.UniqueID(deviceID)
}

// encodeID turns the ten digit identifier into the four pseudo-IP bytes that
// carry it, the inverse of decodeID.
func encodeID(uniqueID string) ([]byte, error) {
	if len(uniqueID) < 10 {
		return nil, fmt.Errorf("gator: unique id %q is too short", uniqueID)
	}

	digits := make([]int, 5)
	pairs := [][2]int{{1, 3}, {3, 5}, {5, 7}, {7, 9}}
	for i, pair := range pairs {
		value, err := strconv.Atoi(uniqueID[pair[0]:pair[1]])
		if err != nil {
			return nil, fmt.Errorf("gator: unique id %q is not numeric", uniqueID)
		}
		digits[i] = value
	}
	last, err := strconv.Atoi(uniqueID[9:])
	if err != nil {
		return nil, fmt.Errorf("gator: unique id %q is not numeric", uniqueID)
	}

	first := digits[0] - 30
	return []byte{
		byte(digits[1] | ((first>>3)&1)<<7),
		byte(digits[2] | ((first>>2)&1)<<7),
		byte(digits[3] | ((first>>1)&1)<<7),
		byte(last | (first&1)<<7),
	}, nil
}

func encodeContent(uniqueID string, messageType int, content []byte) ([]byte, error) {
	id, err := encodeID(uniqueID)
	if err != nil {
		return nil, err
	}

	// The length covers the four byte identifier, the content, the checksum and
	// the 0D terminator, so the frame is 5 + length bytes with no correction.
	frame := make([]byte, 0, 12+len(content))
	frame = append(frame, 0x24, 0x24, byte(messageType), 0x00, byte(6+len(content)))
	frame = append(frame, id...)
	frame = append(frame, content...)
	frame = append(frame, helper.XOR(frame))
	return append(frame, 0x0D), nil
}

var _ codec.Encoder = (*Encoder)(nil)
