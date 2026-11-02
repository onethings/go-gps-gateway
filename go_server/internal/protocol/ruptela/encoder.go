package ruptela

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands Ruptela cannot express.
var ErrUnsupportedCommand = errors.New("ruptela: unsupported command")

// Encoder turns high level commands into Ruptela content frames. The messages
// it builds are re-encoded with the device serial number by the gateway.
type Encoder struct {
	codec.Base
}

// NewEncoder creates a Ruptela encoder.
func NewEncoder(base codec.Base) *Encoder { return &Encoder{Base: base} }

// EncodeCommand implements codec.Encoder.
func (e *Encoder) EncodeCommand(command *model.Command) ([]byte, error) {
	switch command.Type {
	case model.CommandCustom:
		data, ok := command.GetAttribute(model.CmdKeyData)
		if !ok {
			return nil, ErrUnsupportedCommand
		}
		text, ok := data.(string)
		if !ok {
			return nil, ErrUnsupportedCommand
		}
		if isHex(text) {
			payload, err := hex.DecodeString(text)
			if err != nil {
				return nil, ErrUnsupportedCommand
			}
			return payload, nil
		}
		return encodeContent(msgSMSViaGPRS, []byte(text)), nil

	case model.CommandEngineStop:
		return encodeContent(msgSMSViaGPRS, []byte("pass immobilizer 10")), nil

	case model.CommandEngineResume:
		return encodeContent(msgSMSViaGPRS, []byte("pass resetimmob")), nil

	case model.CommandRequestPhoto:
		content := make([]byte, 0, 10)
		content = append(content, 1) // sub-command
		content = append(content, 0) // source
		content = binary.BigEndian.AppendUint32(content, 0)          // start timestamp
		content = binary.BigEndian.AppendUint32(content, ^uint32(0)) // end timestamp
		return encodeContent(msgFiles, content), nil

	case model.CommandConfiguration:
		data, ok := command.GetAttribute(model.CmdKeyData)
		if !ok {
			return nil, ErrUnsupportedCommand
		}
		text, ok := data.(string)
		if !ok {
			return nil, ErrUnsupportedCommand
		}
		return encodeContent(msgDeviceConfiguration, []byte(text+"\r\n")), nil

	case model.CommandGetVersion:
		return encodeContent(msgDeviceVersion, nil), nil

	case model.CommandFirmwareUpdate:
		return encodeContent(msgFirmwareUpdate, []byte("|FU_STRT*\r\n")), nil

	case model.CommandOutputControl:
		index, _ := command.GetAttribute(model.CmdKeyIndex)
		data, _ := command.GetAttribute(model.CmdKeyData)
		content := make([]byte, 0, 8)
		content = binary.BigEndian.AppendUint32(content, uint32(asInt64(index)))
		content = binary.BigEndian.AppendUint32(content, uint32(asInt64(data)))
		return encodeContent(msgSetIO, content), nil

	case model.CommandSetConnection:
		server, _ := command.GetAttribute(model.CmdKeyServer)
		port, _ := command.GetAttribute(model.CmdKeyPort)
		text := fmt.Sprintf("%s,%d,TCP", asString(server), asInt64(port))
		return encodeContent(msgSetConnection, []byte(text)), nil

	case model.CommandSetOdometer:
		data, _ := command.GetAttribute(model.CmdKeyData)
		content := binary.BigEndian.AppendUint32(nil, uint32(asInt64(data)))
		return encodeContent(msgSetOdometer, content), nil

	default:
		return nil, ErrUnsupportedCommand
	}
}

// encodeContent wraps a Ruptela content payload into a complete frame:
// two byte length, a type byte of 100 + type, the content and a two byte
// CRC-16/KERMIT computed over everything after the length field.
func encodeContent(typeID int, content []byte) []byte {
	writerIndex := 2 + 1 + len(content)
	checkStart := 2
	payload := make([]byte, 0, writerIndex+2)
	payload = binary.BigEndian.AppendUint16(payload, uint16(1+len(content)))
	payload = append(payload, byte(100+typeID))
	payload = append(payload, content...)
	crc := helper.CRC16Kermit(payload[checkStart:])
	payload = binary.BigEndian.AppendUint16(payload, uint16(crc))
	return payload
}

// isHex reports whether s is a non empty string of hexadecimal pairs.
func isHex(s string) bool {
	if len(s) == 0 || len(s)%2 != 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(s))
	}
}

var _ codec.Encoder = (*Encoder)(nil)
