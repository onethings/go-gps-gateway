package h02

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnsupportedCommand is returned for commands H02 cannot express.
var ErrUnsupportedCommand = errors.New("h02: unsupported command")

// Encoder turns high level commands into H02 ASCII sentences.
type Encoder struct {
	codec.Base
	// Alternative switches the periodic reporting command to the D1 variant.
	Alternative bool
	// TimeOverride is used by tests to make the encoded timestamp stable.
	TimeOverride *time.Time
	// Location controls the time zone of the encoded timestamp.
	Location *time.Location
}

// NewEncoder creates an H02 encoder.
func NewEncoder(base codec.Base) *Encoder {
	return &Encoder{Base: base, Location: time.Local}
}

func (e *Encoder) now() time.Time {
	if e.TimeOverride != nil {
		return e.TimeOverride.In(e.location())
	}
	return e.Time().In(e.location())
}

func (e *Encoder) location() *time.Location {
	if e.Location == nil {
		return time.Local
	}
	return e.Location
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
			return nil, ErrUnsupportedCommand
		}
		value := strconv.FormatFloat(toFloat(frequency), 'f', -1, 64)
		if e.Alternative {
			return e.formatCommand(uniqueID, "D1", value), nil
		}
		// S71 carries the reporting mode before the timestamp.
		now := e.now()
		return []byte(fmt.Sprintf("*%s,%s,S71,22,%02d%02d%02d,%s#",
			"HQ", uniqueID, now.Hour(), now.Minute(), now.Second(), value)), nil
	default:
		return nil, ErrUnsupportedCommand
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
