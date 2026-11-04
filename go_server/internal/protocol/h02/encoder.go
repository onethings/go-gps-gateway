package h02

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands H02 cannot express.
var ErrUnsupportedCommand = errors.New("h02: unsupported command")

// Encoder turns high level commands into H02 ASCII sentences.
type Encoder struct {
	codec.Base
}

// NewEncoder creates an H02 encoder.
func NewEncoder(base codec.Base) *Encoder {
	return &Encoder{Base: base}
}

// now returns the timestamp every sentence carries. H02 devices read it as
// local time, the same way the reference formats it in the server time zone.
func (e *Encoder) now() time.Time {
	return e.Time().In(time.Local)
}

// alternative reads protocol.h02.alternative, which switches the periodic
// reporting command to the D1 variant.
func (e *Encoder) alternative() bool {
	if e.Config == nil {
		return false
	}
	// The spellings the configuration layer accepts for a boolean.
	switch strings.ToLower(strings.TrimSpace(e.Config(config.ProtocolAlternative))) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

func (e *Encoder) uniqueID(deviceID int64) (string, error) {
	if e.UniqueIDs == nil {
		return "", errors.New("h02: no unique id provider configured")
	}
	return e.UniqueIDs.UniqueID(deviceID)
}

func (e *Encoder) formatCommand(uniqueID, commandType string, params ...string) []byte {
	now := e.now()
	var builder strings.Builder
	fmt.Fprintf(&builder, "*%s,%s,%s,%02d%02d%02d",
		"HQ", uniqueID, commandType, now.Hour(), now.Minute(), now.Second())
	for _, param := range params {
		builder.WriteString(",")
		builder.WriteString(param)
	}
	builder.WriteString("#")
	return []byte(builder.String())
}

// EncodeCommand implements codec.Encoder.
func (e *Encoder) EncodeCommand(command *model.Command) ([]byte, error) {
	// A custom command is a whole sentence the operator wrote, so it goes out
	// unchanged and needs no device identifier.
	if command.Type == model.CommandCustom {
		if !command.Attributes.Has(model.CmdKeyData) {
			return nil, fmt.Errorf("%w: missing %s", ErrUnsupportedCommand, model.CmdKeyData)
		}
		data := command.Attributes.GetString(model.CmdKeyData, "")
		if data == "" {
			return nil, fmt.Errorf("%w: empty %s", ErrUnsupportedCommand, model.CmdKeyData)
		}
		return []byte(data), nil
	}

	uniqueID, err := e.uniqueID(command.DeviceID)
	if err != nil {
		return nil, err
	}

	switch command.Type {
	case model.CommandAlarmArm:
		return e.formatCommand(uniqueID, "SCF", "0", "0"), nil
	case model.CommandAlarmDisarm:
		return e.formatCommand(uniqueID, "SCF", "1", "1"), nil
	case model.CommandEngineStop:
		return e.formatCommand(uniqueID, "S20", "1", "1"), nil
	case model.CommandEngineResume:
		return e.formatCommand(uniqueID, "S20", "1", "0"), nil
	case model.CommandPositionPeriodic:
		frequency, ok := command.GetAttribute(model.CmdKeyFrequency)
		if !ok {
			return nil, fmt.Errorf("%w: missing %s", ErrUnsupportedCommand, model.CmdKeyFrequency)
		}
		value := strconv.FormatFloat(toFloat(frequency), 'f', -1, 64)
		if e.alternative() {
			return e.formatCommand(uniqueID, "D1", value), nil
		}
		return e.formatCommand(uniqueID, "S71", "22", value), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	}
}

func toFloat(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		result, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return result
		}
	}
	return 0
}

var _ codec.Encoder = (*Encoder)(nil)
