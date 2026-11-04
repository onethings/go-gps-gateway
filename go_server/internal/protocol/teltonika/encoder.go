package teltonika

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands Teltonika cannot express.
var ErrUnsupportedCommand = errors.New("teltonika: unsupported command")

// Encoder wraps commands into Codec 12 packets.
type Encoder struct {
	codec.Base
}

// NewEncoder creates a Teltonika encoder.
func NewEncoder(base codec.Base) *Encoder {
	return &Encoder{Base: base}
}

// EncodeCommand implements codec.Encoder.
func (e *Encoder) EncodeCommand(command *model.Command) ([]byte, error) {
	switch command.Type {
	case model.CommandEngineStop:
		return encodeContent([]byte("setdigout 1")), nil
	case model.CommandEngineResume:
		return encodeContent([]byte("setdigout 0")), nil
	case model.CommandCustom:
		if !command.Attributes.Has(model.CmdKeyData) {
			return nil, fmt.Errorf("%w: missing %s", ErrUnsupportedCommand, model.CmdKeyData)
		}
		text := command.Attributes.GetString(model.CmdKeyData, "")
		if isHex(text) {
			decoded, err := hex.DecodeString(text)
			if err != nil {
				return nil, ErrUnsupportedCommand
			}
			return encodeContent(decoded), nil
		}
		return encodeContent([]byte(text)), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	}
}

// encodeContent builds the 0x0C packet: four zero bytes, the data length, the
// codec block, and the CRC16/IBM over everything after the length field.
func encodeContent(content []byte) []byte {
	frame := make([]byte, 0, len(content)+20)
	frame = append(frame, 0, 0, 0, 0)
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(content)+8))
	frame = append(frame, codec12, 1, 5) // codec, quantity, type
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(content)))
	frame = append(frame, content...)
	frame = append(frame, 1) // quantity

	crc := helper.CRC16IBM(frame[8:])
	frame = binary.BigEndian.AppendUint32(frame, uint32(crc))
	return frame
}

func isHex(value string) bool {
	if len(value) == 0 || len(value)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

var _ codec.Encoder = (*Encoder)(nil)
