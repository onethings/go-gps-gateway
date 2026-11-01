package model

import "errors"

// Transport values of one protocol server, matching the Java server's
// ProtocolSettings.Transport enum. TCP is the default.
const (
	TransportTCP = "tcp"
	TransportUDP = "udp"
	TransportTLS = "tls"
)

// ErrProtocolNotFound is returned when no protocol settings row has the id.
var ErrProtocolNotFound = errors.New("protocol settings not found")

// ProtocolSettings is one row of protocol_settings: the listen port and the
// transport of one protocol. The Java server keeps the same row in
// tc_protocol_settings and lets an admin change it at runtime.
type ProtocolSettings struct {
	ID        int64
	Protocol  string
	Port      int
	Transport string
}

// ValidTransport reports whether value is one of the transports the gateway
// can listen on.
func ValidTransport(value string) bool {
	switch value {
	case TransportTCP, TransportUDP, TransportTLS:
		return true
	default:
		return false
	}
}
