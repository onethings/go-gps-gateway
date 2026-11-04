package gt06

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands GT06 cannot express.
var ErrUnsupportedCommand = errors.New("gt06: unsupported command")

// defaultPassword is the factory device password the alternative engine
// commands carry when none is configured.
const defaultPassword = "123456"

// Encoder turns high level commands into GT06 0x80 command frames.
type Encoder struct {
	codec.Base
}

// NewEncoder creates a GT06 encoder.
func NewEncoder(base codec.Base) *Encoder {
	return &Encoder{Base: base}
}

// EncodeCommand implements codec.Encoder. Every content string below is one
// entry of the Concox command set the GT06 family reads out of a 0x80 frame.
func (e *Encoder) EncodeCommand(command *model.Command) ([]byte, error) {
	switch command.Type {
	case model.CommandEngineStop, model.CommandEngineResume:
		return e.encodeContent(e.engine(command)), nil

	case model.CommandPositionSingle:
		return e.encodeContent("WHERE#"), nil

	case model.CommandPositionPeriodic:
		frequency, err := attributeInt(command, model.CmdKeyFrequency)
		if err != nil {
			return nil, err
		}
		if frequency < 0 {
			return nil, fmt.Errorf("%w: %s is negative", ErrUnsupportedCommand, model.CmdKeyFrequency)
		}
		return e.encodeContent("TIMER," + strconv.Itoa(frequency) + "#"), nil

	case model.CommandPositionStop:
		return e.encodeContent("TIMER,0#"), nil

	case model.CommandRebootDevice:
		return e.encodeContent("RESET#"), nil

	case model.CommandFactoryReset:
		return e.encodeContent("FACTORY#"), nil

	case model.CommandPowerOff:
		return e.encodeContent("POWEROFF#"), nil

	case model.CommandGetDeviceStatus:
		return e.encodeContent("STATUS#"), nil

	case model.CommandGetVersion:
		return e.encodeContent("VERSION#"), nil

	case model.CommandConfiguration:
		return e.encodeContent("PARAM#"), nil

	case model.CommandSOSNumber, model.CommandSetPhonebook:
		prefix := "SOS"
		if command.Type == model.CommandSetPhonebook {
			prefix = "CENTER"
		}
		slot, err := slotName(command)
		if err != nil {
			return nil, err
		}
		phone, err := phoneNumber(command)
		if err != nil {
			return nil, err
		}
		return e.encodeContent(prefix + "," + slot + "," + phone + "#"), nil

	case model.CommandVoiceMonitoring:
		phone, err := phoneNumber(command)
		if err != nil {
			return nil, err
		}
		return e.encodeContent("LISTEN," + phone + "#"), nil

	case model.CommandSetConnection:
		return e.connection(command)

	case model.CommandCustom:
		data, err := attributeString(command, model.CmdKeyData)
		if err != nil {
			return nil, err
		}
		return e.encodeContent(data), nil
	}

	return nil, fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
}

// engine returns the engine control content. The G109 reads the DYD/HFYD
// spelling without a password; other models use it only when the alternative
// layout is configured.
func (e *Encoder) engine(command *model.Command) string {
	stop := command.Type == model.CommandEngineStop
	if e.modelOf(command) == "G109" {
		if stop {
			return "DYD#"
		}
		return "HFYD#"
	}
	if e.flag(config.ProtocolAlternative) {
		if stop {
			return "DYD," + e.devicePassword() + "#"
		}
		return "HFYD," + e.devicePassword() + "#"
	}
	if stop {
		return "Relay,1#"
	}
	return "Relay,0#"
}

// connection tells the device where to report. A dotted address uses the
// SERVER form, a host name the DOMAIN one.
func (e *Encoder) connection(command *model.Command) ([]byte, error) {
	server, err := attributeString(command, model.CmdKeyServer)
	if err != nil {
		return nil, err
	}
	server = strings.TrimSpace(server)
	if server == "" {
		return nil, fmt.Errorf("%w: empty %s", ErrUnsupportedCommand, model.CmdKeyServer)
	}
	port, err := attributeInt(command, model.CmdKeyPort)
	if err != nil {
		return nil, err
	}
	if port < 1 || port > 0xffff {
		return nil, fmt.Errorf("%w: %s is not a port", ErrUnsupportedCommand, model.CmdKeyPort)
	}

	kind := "DOMAIN"
	if net.ParseIP(server) != nil {
		kind = "SERVER"
	}
	return e.encodeContent(kind + "," + server + "," + strconv.Itoa(port) + "#"), nil
}

// modelOf returns the stored model of the command's device, which selects the
// G109 engine layout. A missing link in the lookup leaves the generic layout.
func (e *Encoder) modelOf(command *model.Command) string {
	if e.UniqueIDs == nil || e.Resolver == nil {
		return ""
	}
	uniqueID, err := e.UniqueIDs.UniqueID(command.DeviceID)
	if err != nil {
		return ""
	}
	session, err := e.Resolver.Resolve(uniqueID)
	if err != nil || session == nil {
		return ""
	}
	return session.Model
}

// flag reads one of the boolean protocol settings. A missing configuration
// layer reads as false, which keeps the default layouts.
func (e *Encoder) flag(key string) bool {
	if e.Config == nil {
		return false
	}
	// The spellings the configuration layer accepts for a boolean.
	switch strings.ToLower(strings.TrimSpace(e.Config(key))) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// devicePassword reads protocol.gt06.devicePassword. The Java encoder also
// lets a per device devicePassword attribute win; the Go session carries no
// device attributes, so only the protocol section can override the default.
func (e *Encoder) devicePassword() string {
	if e.Config != nil {
		if password := e.Config(config.ProtocolDevicePassword); password != "" {
			return password
		}
	}
	return defaultPassword
}

func (e *Encoder) encodeContent(content string) []byte {
	language := e.flag(config.ProtocolLanguage)
	length := 1 + 1 + 4 + len(content) + 2 + 2
	if language {
		length += 2
	}

	frame := make([]byte, 0, length+5)
	frame = append(frame, 0x78, 0x78, byte(length), msgCommand0, byte(4+len(content)))
	frame = append(frame, 0, 0, 0, 0) // command index
	frame = append(frame, content...)
	if language {
		frame = append(frame, 0, 2) // english
	}
	frame = append(frame, 0, 0) // message index

	crc := helper.CRC16X25(frame[2:])
	frame = append(frame, byte(crc>>8), byte(crc), 0x0d, 0x0a)
	return frame
}

// slotName renders the phonebook slot the device reads as a letter.
func slotName(command *model.Command) (string, error) {
	index, err := attributeInt(command, model.CmdKeyIndex)
	if err != nil {
		return "", err
	}
	if index < 0 || index > 3 {
		return "", fmt.Errorf("%w: %s outside 0..3", ErrUnsupportedCommand, model.CmdKeyIndex)
	}
	return string(rune('A' + index)), nil
}

func phoneNumber(command *model.Command) (string, error) {
	phone, err := attributeString(command, model.CmdKeyPhone)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(phone) == "" {
		return "", fmt.Errorf("%w: empty %s", ErrUnsupportedCommand, model.CmdKeyPhone)
	}
	return phone, nil
}

// attributeInt reads an integer command parameter. A missing parameter and one
// that is not a number both refuse the command: a zeroed field would silently
// report a different interval or slot than the caller asked for.
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

// attributeString reads a string command parameter the command cannot be built
// without.
func attributeString(command *model.Command, key string) (string, error) {
	if !command.Attributes.Has(key) {
		return "", fmt.Errorf("%w: missing %s", ErrUnsupportedCommand, key)
	}
	return command.Attributes.GetString(key, ""), nil
}

var _ codec.Encoder = (*Encoder)(nil)
