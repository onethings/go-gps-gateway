package gt06

import (
	"errors"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands GT06 cannot express.
var ErrUnsupportedCommand = errors.New("gt06: unsupported command")

// Encoder turns high level commands into GT06 0x80 command frames.
type Encoder struct {
	codec.Base
	// Alternative switches engine commands to the "DYD,<password>" form.
	Alternative bool
	// Password is used by the alternative engine commands.
	Password string
	// Language appends the optional english language field.
	Language bool
}

// NewEncoder creates a GT06 encoder with the factory default password.
func NewEncoder(base codec.Base) *Encoder {
	return &Encoder{Base: base, Password: "123456"}
}

// EncodeCommand implements codec.Encoder.
func (e *Encoder) EncodeCommand(command *model.Command) ([]byte, error) {
	switch command.Type {
	case model.CommandEngineStop:
		if e.Alternative {
			return e.encodeContent("DYD," + e.Password + "#"), nil
		}
		return e.encodeContent("Relay,1#"), nil
	case model.CommandEngineResume:
		if e.Alternative {
			return e.encodeContent("HFYD," + e.Password + "#"), nil
		}
		return e.encodeContent("Relay,0#"), nil
	case model.CommandCustom:
		data, ok := command.GetAttribute(model.CmdKeyData)
		if !ok {
			return nil, ErrUnsupportedCommand
		}
		content, ok := data.(string)
		if !ok {
			return nil, ErrUnsupportedCommand
		}
		return e.encodeContent(content), nil
	default:
		return nil, ErrUnsupportedCommand
	}
}

func (e *Encoder) encodeContent(content string) []byte {
	length := 1 + 1 + 4 + len(content) + 2 + 2
	if e.Language {
		length += 2
	}

	frame := make([]byte, 0, length+5)
	frame = append(frame, 0x78, 0x78, byte(length), msgCommand0, byte(4+len(content)))
	frame = append(frame, 0, 0, 0, 0) // command index
	frame = append(frame, content...)
	if e.Language {
		frame = append(frame, 0, 2) // english
	}
	frame = append(frame, 0, 0) // message index

	crc := helper.CRC16X25(frame[2:])
	frame = append(frame, byte(crc>>8), byte(crc), 0x0d, 0x0a)
	return frame
}

var _ codec.Encoder = (*Encoder)(nil)
