package forward

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

// WialonConfig configures the Wialon IPS forwarder. Address is host:port and
// Version may be "2" to emit the checksummed v2 message.
type WialonConfig struct {
	Address string
	Version string
	Dial    func(ctx context.Context, network, address string) (net.Conn, error)
}

// WialonTransport sends one UDP datagram per position in the Wialon IPS text
// format, which is what the Java PositionForwarderWialon does.
type WialonTransport struct {
	config WialonConfig
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
}

// NewWialon creates the forwarder.
func NewWialon(config WialonConfig) (*WialonTransport, error) {
	if config.Address == "" {
		return nil, fmt.Errorf("forward wialon: empty address")
	}
	dial := config.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		dial = dialer.DialContext
	}
	return &WialonTransport{config: config, dial: dial}, nil
}

// Name implements Transport.
func (t *WialonTransport) Name() string { return "wialon" }

// Send implements Transport.
func (t *WialonTransport) Send(ctx context.Context, message Message) error {
	if message.Position == nil || message.Device == nil {
		return fmt.Errorf("forward wialon: needs a position and a device")
	}
	payload := wialonPayload(*message.Position)

	text := message.Device.UniqueID + "#D#" + payload
	if strings.HasPrefix(t.config.Version, "2") {
		// The IPS 2.0 data message appends the CRC16 of the body.
		text += fmt.Sprintf(";%04x", helper.CRC16IBM([]byte(payload)))
	}
	text += "\r\n"

	conn, err := t.dial(ctx, "udp", t.config.Address)
	if err != nil {
		return fmt.Errorf("forward wialon: dial %s: %w", t.config.Address, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	}
	if _, err := conn.Write([]byte(text)); err != nil {
		return fmt.Errorf("forward wialon: write: %w", err)
	}
	return nil
}

// Close implements Transport.
func (t *WialonTransport) Close() error { return nil }

// wialonPayload renders the semicolon separated IPS body. The four NA fields
// are the fields the Java implementation leaves empty (LBS and counters).
func wialonPayload(position model.Position) string {
	speedKph := position.Speed / 0.539957
	return fmt.Sprintf("%s;%02d%.5f;%s;%03d%.5f;%s;%d;%d;%d;NA;NA;NA;NA;;%s;%s",
		position.Time.UTC().Format("020106;150405"),
		int(abs(position.Latitude)),
		degreesToMinutes(position.Latitude),
		hemisphere(position.Latitude, "N", "S"),
		int(abs(position.Longitude)),
		degreesToMinutes(position.Longitude),
		hemisphere(position.Longitude, "E", "W"),
		int(speedKph),
		int(position.Course),
		int(position.Altitude),
		position.Attributes.GetString("driverUniqueId", "NA"),
		wialonAttributes(position.Attributes),
	)
}

// degreesToMinutes renders the fractional degree part as minutes, which is how
// the IPS format expects a coordinate.
func degreesToMinutes(value float64) float64 {
	return (abs(value) - float64(int(abs(value)))) * 60
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func hemisphere(value float64, positive, negative string) string {
	if value >= 0 {
		return positive
	}
	return negative
}

// wialonAttributes renders the scalar attributes as key:value|key:value, in key
// order so the datagram is deterministic.
func wialonAttributes(attributes model.Attributes) string {
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var parts []string
	for _, key := range keys {
		switch attributes[key].(type) {
		case string, bool, float64, float32, int, int64:
		default:
			continue
		}
		parts = append(parts, key+":"+fmt.Sprint(attributes[key]))
	}
	return strings.Join(parts, "|")
}
