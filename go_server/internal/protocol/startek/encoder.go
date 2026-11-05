package startek

import (
	"errors"
	"fmt"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands Startek cannot express.
var ErrUnsupportedCommand = errors.New("startek: unsupported command")

// Encoder turns high level commands into Startek sentences.
type Encoder struct {
	codec.Base
}

// NewEncoder creates a Startek encoder.
func NewEncoder(base codec.Base) *Encoder { return &Encoder{Base: base} }

// EncodeCommand implements codec.Encoder.
func (e *Encoder) EncodeCommand(command *model.Command) ([]byte, error) {
	uniqueID, err := e.uniqueID(command.DeviceID)
	if err != nil {
		return nil, err
	}

	switch command.Type {
	case model.CommandCustom:
		return sentence(uniqueID, command.Attributes.GetString(model.CmdKeyData, "")), nil
	case model.CommandOutputControl:
		return sentence(uniqueID, fmt.Sprintf("900,%s,%s",
			command.Attributes.GetString(model.CmdKeyIndex, ""),
			command.Attributes.GetString(model.CmdKeyData, ""))), nil
	case model.CommandEngineStop:
		return sentence(uniqueID, "900,1,1"), nil
	case model.CommandEngineResume:
		return sentence(uniqueID, "900,1,0"), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	}
}

func (e *Encoder) uniqueID(deviceID int64) (string, error) {
	if e.UniqueIDs == nil {
		return "", errors.New("startek: no unique id provider configured")
	}
	return e.UniqueIDs.UniqueID(deviceID)
}

// sentence frames the payload with the length that counts everything after the
// index, and the 8 bit sum of the ASCII bytes as two uppercase hex digits.
func sentence(uniqueID string, payload string) []byte {
	body := fmt.Sprintf("$$:%d,%s,%s", 1+len(uniqueID)+1+len(payload), uniqueID, payload)
	var sum byte
	for index := 0; index < len(body); index++ {
		sum += body[index]
	}
	return []byte(fmt.Sprintf("%s%02X\r\n", body, sum))
}

var _ codec.Encoder = (*Encoder)(nil)
