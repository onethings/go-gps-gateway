package jt808

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands JT808 cannot express.
var ErrUnsupportedCommand = errors.New("jt808: unsupported command")

// Configuration keys this encoder reads. The router resolves them through the
// per protocol chain, so alternative matches jt808.alternative as well as
// protocol.jt808.alternative.
const (
	keyAlternative = config.ProtocolAlternative
	keyWebURL      = config.WebURL
	keyJT1078Port  = config.JT1078Port
)

// The ways a device model accepts a raw custom payload. They mirror the model
// branches of Jt808ProtocolEncoder.
const (
	customHex = iota
	customParameter
	customText
	customTransparent
)

// Encoder builds JT808 downlink messages.
type Encoder struct {
	codec.Base
}

// NewEncoder creates a JT808 encoder.
func NewEncoder(base codec.Base) *Encoder {
	return &Encoder{Base: base}
}

// EncodeCommand implements codec.Encoder. The byte layouts are field for field
// copies of Jt808ProtocolEncoder, including the parameter identifiers the Java
// server writes with a one byte or a four byte width.
func (e *Encoder) EncodeCommand(command *model.Command) ([]byte, error) {
	if e.UniqueIDs == nil {
		return nil, ErrUnsupportedCommand
	}
	uniqueID, err := e.UniqueIDs.UniqueID(command.DeviceID)
	if err != nil {
		return nil, err
	}
	id, err := encodeID(uniqueID, idWidth(uniqueID))
	if err != nil {
		return nil, err
	}
	h := &header{delimiter: 0x7e, id: id}
	if len(id) == 10 {
		// A JT808-2019 header carries a version byte behind the 0x4000
		// attribute flag. The gateway records no negotiated version and every
		// terminal that registers a ten byte identifier speaks that layout, so
		// the command uses version 1.
		version := byte(0x01)
		h.version = &version
	}

	// Every frame this encoder builds carries its own serial, recorded on the
	// command so the terminal's answer can be paired with it. A command that
	// already carries one keeps it: a queued command that is encoded again
	// after a restart must still match the answer to its first frame. A raw
	// custom payload is already a whole frame, so it keeps whatever serial the
	// caller embedded and stays outside the pairing path.
	build := func(messageType int, payload []byte) []byte {
		if command.Serial == 0 {
			command.Serial = e.NextSerial()
		}
		h.serial = command.Serial
		return buildMessage(h, messageType, payload)
	}

	switch command.Type {
	case model.CommandPositionSingle:
		return build(msgLocationQuery, nil), nil

	case model.CommandPositionStop:
		return build(msgTemporaryTracking, []byte{0, 0, 0, 0, 0, 0}), nil

	case model.CommandRebootDevice:
		return build(msgParameterSetting, []byte{1, 0x23, 1, 0x03}), nil

	case model.CommandPositionPeriodic:
		frequency, err := attributeInt(command, model.CmdKeyFrequency)
		if err != nil {
			return nil, err
		}
		payload := append([]byte{1, 0x06, 4}, bigEndian(frequency)...)
		return build(msgParameterSetting, payload), nil

	case model.CommandAlarmArm, model.CommandAlarmDisarm:
		flag := byte(0x00)
		if command.Type == model.CommandAlarmArm {
			flag = 0x01
		}
		return build(msgParameterSetting, []byte{1, 0x24, 1 + 4, flag, 'u', 's', 'e', 'r'}), nil

	case model.CommandAlarmDismiss:
		return build(msgAlarmAck, []byte{0, 0, 0, 0, 0, 0}), nil

	case model.CommandEngineStop, model.CommandEngineResume:
		messageType, payload := e.engineControl(command, uniqueID)
		return build(messageType, payload), nil

	case model.CommandSetConnection:
		server, err := attributeString(command, model.CmdKeyServer)
		if err != nil {
			return nil, err
		}
		port, err := attributeInt(command, model.CmdKeyPort)
		if err != nil {
			return nil, err
		}
		payload := []byte{2, 0, 0, 0, 0x13, byte(len(server))}
		payload = append(payload, server...)
		payload = append(payload, 0, 0, 0, 0x18, 4)
		payload = append(payload, bigEndian(port)...)
		return build(msgConfigurationParams, payload), nil

	case model.CommandPowerOff:
		return build(msgTerminalControl, []byte{0x02}), nil

	case model.CommandFactoryReset:
		return build(msgTerminalControl, []byte{0x03}), nil

	case model.CommandMessage, model.CommandVoiceMessage:
		message, err := attributeString(command, model.CmdKeyMessage)
		if err != nil {
			return nil, err
		}
		text, err := gbk(message)
		if err != nil {
			return nil, err
		}
		flag := byte(0x04)
		if command.Type == model.CommandVoiceMessage {
			flag = 0x08
		}
		return build(msgSendTextMessage, append([]byte{flag}, text...)), nil

	case model.CommandRequestPhoto:
		channel := command.Attributes.GetInteger(model.CmdKeyIndex, 0)
		payload := []byte{byte(channel), 0, 1}
		payload = append(payload, make([]byte, 9)...)
		return build(msgTakePhoto, payload), nil

	case model.CommandSetSpeedLimit:
		limit, err := attributeInt(command, model.CmdKeyData)
		if err != nil {
			return nil, err
		}
		payload := []byte{1, 0, 0, 0, 0x55, 4}
		payload = append(payload, bigEndian(limit)...)
		if command.Attributes.Has(model.CmdKeyDuration) {
			payload[0] = 2
			duration := command.Attributes.GetInteger(model.CmdKeyDuration, 0)
			payload = append(payload, 0, 0, 0, 0x56, 4)
			payload = append(payload, bigEndian(duration)...)
		}
		return build(msgConfigurationParams, payload), nil

	case model.CommandOutputControl:
		channel := command.Attributes.GetInteger(model.CmdKeyIndex, 0)
		return build(msgVehicleControl, []byte{byte(channel)}), nil

	case model.CommandConfiguration:
		return build(msgParameterQueryAll, nil), nil

	case model.CommandVideoStart:
		host, port, err := e.videoEndpoint()
		if err != nil {
			return nil, err
		}
		channel := command.Attributes.GetInteger(model.CmdKeyIndex, 1)
		payload := []byte{byte(len(host))}
		payload = append(payload, host...)
		payload = append(payload, byte(port>>8), byte(port), 0, 0, byte(channel), 1, 0)
		return build(msgVideoRequest, payload), nil

	case model.CommandVideoStop:
		channel := command.Attributes.GetInteger(model.CmdKeyIndex, 1)
		return build(msgVideoControl, []byte{byte(channel), 0, 0, 0}), nil

	case model.CommandCustom:
		data, err := attributeString(command, model.CmdKeyData)
		if err != nil {
			return nil, err
		}
		return e.custom(uniqueID, data, build)
	}

	return nil, ErrUnsupportedCommand
}

// engineControl builds the message that cuts or restores the engine. The
// alternative layout and the two model specific ones are the branches of
// Jt808ProtocolEncoder.
func (e *Encoder) engineControl(command *model.Command, uniqueID string) (int, []byte) {
	stop := command.Type == model.CommandEngineStop
	if e.alternative() {
		flag := byte(0x00)
		if stop {
			flag = 0x01
		}
		return msgOilControl, append([]byte{flag}, bcdDate(e.Time())...)
	}
	switch e.modelOf(uniqueID) {
	case "VL300":
		if stop {
			return msgTerminalControl, []byte("#0;1")
		}
		return msgTerminalControl, []byte("#0;0")
	case "W15L":
		if stop {
			return msgTerminalControl, []byte{0x64}
		}
		return msgTerminalControl, []byte{0x65}
	}
	if stop {
		return msgTerminalControl, []byte{0xf0}
	}
	return msgTerminalControl, []byte{0xf1}
}

// custom builds a raw custom command. Some models read it as a parameter, a
// text message or a transparent payload instead of as a whole pre-built frame.
func (e *Encoder) custom(uniqueID, data string, build func(int, []byte) []byte) ([]byte, error) {
	switch customKind(e.modelOf(uniqueID)) {
	case customParameter:
		payload := []byte{1, 0, 0, 0xf0, 0x30, byte(len(data))}
		return build(msgConfigurationParams, append(payload, data...)), nil
	case customText:
		text, err := gbk(data)
		if err != nil {
			return nil, err
		}
		return build(msgSendTextMessage, append([]byte{1}, text...)), nil
	case customTransparent:
		return build(msgTransparentDownlink, append([]byte{0xf0}, data...)), nil
	}
	if !isHexString(data) {
		return nil, ErrUnsupportedCommand
	}
	return hex.DecodeString(data)
}

// customKind groups the models Jt808ProtocolEncoder special-cases by the
// message that carries a raw custom payload.
func customKind(deviceModel string) int {
	switch deviceModel {
	case "AL300", "GL100", "VL300":
		return customParameter
	case "BSJ", "C5", "C5L":
		return customText
	}
	if strings.HasPrefix(deviceModel, "JC") {
		return customTransparent
	}
	return customHex
}

// modelOf returns the stored model of the device, which selects the model
// specific payloads.
func (e *Encoder) modelOf(uniqueID string) string {
	if e.Resolver == nil {
		return ""
	}
	session, err := e.Resolver.Resolve(uniqueID)
	if err != nil || session == nil {
		return ""
	}
	return session.Model
}

// alternative reports jt808.alternative. The Java server also lets a device
// attribute of the same name override the setting; the Go session carries no
// device attributes, so only the configuration layer is read.
func (e *Encoder) alternative() bool {
	if e.Config == nil {
		return false
	}
	// The spellings the configuration layer accepts for a boolean.
	switch strings.ToLower(strings.TrimSpace(e.Config(keyAlternative))) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// videoEndpoint resolves where a terminal should send its JT1078 stream. The
// Java encoder takes the host from web.url and the port of the jt1078
// protocol. The Go gateway runs no JT1078 listener, so both keys must be set
// before the command can be built.
func (e *Encoder) videoEndpoint() (string, int, error) {
	if e.Config == nil {
		return "", 0, fmt.Errorf("%w: %s is not configured", ErrUnsupportedCommand, keyWebURL)
	}
	raw := e.Config(keyWebURL)
	if raw == "" {
		return "", 0, fmt.Errorf("%w: %s is not configured", ErrUnsupportedCommand, keyWebURL)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", 0, fmt.Errorf("%w: %s %q has no host", ErrUnsupportedCommand, keyWebURL, raw)
	}
	port, err := strconv.Atoi(e.Config(keyJT1078Port))
	if err != nil || port <= 0 || port > 0xffff {
		return "", 0, fmt.Errorf("%w: %s is not a port", ErrUnsupportedCommand, keyJT1078Port)
	}
	return parsed.Hostname(), port, nil
}

// attributeInt reads an integer command parameter. A missing parameter and one
// that is not a number both refuse the command: a zeroed field would silently
// report a different interval or limit than the caller asked for.
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

// bigEndian renders an int as the four byte big endian field JT808 uses.
func bigEndian(value int) []byte {
	return []byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)}
}

// bcdDate renders the six packed BCD bytes yyMMddHHmmss that the oil control
// command carries.
func bcdDate(now time.Time) []byte {
	out, _ := hex.DecodeString(now.Format("060102150405"))
	return out
}

// gbk encodes device bound text with the code page the JT808 text messages use.
func gbk(value string) ([]byte, error) {
	out, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(value))
	if err != nil {
		return nil, fmt.Errorf("%w: gbk: %v", ErrUnsupportedCommand, err)
	}
	return out, nil
}

func isHexString(value string) bool {
	if len(value) == 0 || len(value)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

var _ codec.Encoder = (*Encoder)(nil)
