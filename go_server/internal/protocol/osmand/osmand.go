// Package osmand implements the OsmAnd HTTP protocol: the device sends one
// request per position with the values in the query string, and the server
// answers with an empty body. The MQTT protocol reuses the same query parser,
// which is why it lives in Decode instead of the HTTP handler.
package osmand

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
)

// ProtocolName is the identifier used in configuration and stored on positions.
const ProtocolName = "osmand"

// Protocol is the HTTP front end of the query decoder.
type Protocol struct {
	codec.Base
}

// New creates the HTTP decoder.
func New(base codec.Base) *Protocol { return &Protocol{Base: base} }

// Name implements codec.HTTPDecoder.
func (p *Protocol) Name() string { return ProtocolName }

// DecodeHTTP parses one request. The device repeats its identifier in every
// request, so there is no session to carry over.
func (p *Protocol) DecodeHTTP(request *codec.HTTPRequest) (*codec.Result, *codec.HTTPResponse, error) {
	result := Decode(p.Base, request.Query)
	// OsmAnd only checks the status; a body keeps a proxy that expects one
	// happy.
	return result, &codec.HTTPResponse{Status: http.StatusOK, Body: []byte("{}")}, nil
}

// Decode turns the query a device sends into a position. Unknown values are
// ignored rather than rejected: a device that adds a field must keep working.
func Decode(base codec.Base, values url.Values) *codec.Result {
	uniqueID := firstNonEmpty(values, "id", "deviceid")
	if uniqueID == "" {
		return nil
	}
	session := resolve(base, uniqueID)
	if session == nil {
		return nil
	}

	position := model.NewPosition(ProtocolName)
	position.DeviceID = session.DeviceID
	position.Time = timestamp(base, values.Get("timestamp"))

	latitude, hasLatitude := floatValue(values.Get("lat"))
	longitude, hasLongitude := floatValue(values.Get("lon"))
	if hasLatitude && hasLongitude {
		position.Latitude = latitude
		position.Longitude = longitude
		position.Valid = true
	}
	if speed, ok := floatValue(values.Get("speed")); ok {
		position.Speed = speed
	}
	if course, ok := floatValue(values.Get("bearing")); ok {
		position.Course = course
	}
	if altitude, ok := floatValue(values.Get("altitude")); ok {
		position.Altitude = altitude
	}
	if hdop, ok := floatValue(values.Get("hdop")); ok {
		position.Set(model.KeyHDOP, hdop)
	}
	if battery, ok := floatValue(values.Get("batt")); ok {
		position.Set(model.KeyBatteryLevel, battery)
	}

	result := codec.NewResult()
	result.AddPosition(position)
	return result
}

// resolve looks the identifier up. An HTTP request has no session to bind to, so
// a device the directory does not know is dropped instead of creating a
// position without a device.
func resolve(base codec.Base, uniqueID string) *codec.DeviceSession {
	if base.Resolver == nil {
		return nil
	}
	session, err := base.Resolver.Resolve(uniqueID)
	if err != nil {
		return nil
	}
	return session
}

// timestamp accepts seconds, milliseconds or microseconds since the epoch and
// falls back to the server clock.
func timestamp(base codec.Base, raw string) time.Time {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return base.Time()
	}
	switch {
	case value > 1e15:
		return time.UnixMicro(value).UTC()
	case value > 1e12:
		return time.UnixMilli(value).UTC()
	default:
		return time.Unix(value, 0).UTC()
	}
}

func firstNonEmpty(values url.Values, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func floatValue(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, false
	}
	return value, true
}
