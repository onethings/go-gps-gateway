package wondex

import (
	"errors"
	"fmt"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands Wondex cannot express.
var ErrUnsupportedCommand = errors.New("wondex: unsupported command")

// defaultPassword is the factory password every Wondex command carries.
const defaultPassword = "0000"

// Encoder turns high level commands into Wondex text sentences.
type Encoder struct {
	codec.Base
}

// NewEncoder creates a Wondex encoder.
func NewEncoder(base codec.Base) *Encoder {
	return &Encoder{Base: base}
}

// EncodeCommand implements codec.Encoder. Wondex has no unique id in the
// command: the sentence goes back over the device's open connection.
func (e *Encoder) EncodeCommand(command *model.Command) ([]byte, error) {
	var name string
	switch command.Type {
	case model.CommandRebootDevice:
		name = "REBOOT"
	case model.CommandGetDeviceStatus:
		name = "TEST"
	case model.CommandGetModemStatus:
		name = "GSMINFO"
	case model.CommandIdentification:
		name = "IMEI"
	case model.CommandPositionSingle:
		name = "GETLOCATION"
	case model.CommandGetVersion:
		name = "VER"
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	}
	return []byte("$WP+" + name + "=" + e.devicePassword(command)), nil
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

var _ codec.Encoder = (*Encoder)(nil)
