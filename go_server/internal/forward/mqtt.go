package forward

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// MQTTConfig configures the MQTT forwarder. The client speaks MQTT 3.1.1 with a
// clean session and QoS 0, which is enough for a fire-and-forget feed; delivery
// guarantees come from the forwarder queue, not from the broker.
type MQTTConfig struct {
	Broker   string
	ClientID string
	Username string
	Password string
	Topic    string
	// Dial defaults to net.Dialer.DialContext.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// MQTTTransport publishes the position or event JSON to a topic.
type MQTTTransport struct {
	config MQTTConfig
	dial   func(ctx context.Context, network, address string) (net.Conn, error)

	mu   sync.Mutex
	conn net.Conn
}

// NewMQTT creates the forwarder.
func NewMQTT(config MQTTConfig) (*MQTTTransport, error) {
	if config.Broker == "" {
		return nil, fmt.Errorf("forward mqtt: empty broker")
	}
	if config.Topic == "" {
		return nil, fmt.Errorf("forward mqtt: empty topic")
	}
	if config.ClientID == "" {
		config.ClientID = "traccar-gateway"
	}
	dial := config.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		dial = dialer.DialContext
	}
	return &MQTTTransport{config: config, dial: dial}, nil
}

// Name implements Transport.
func (t *MQTTTransport) Name() string { return "mqtt" }

// Send implements Transport.
func (t *MQTTTransport) Send(ctx context.Context, message Message) error {
	encoded, err := json.Marshal(payload(message))
	if err != nil {
		return fmt.Errorf("forward mqtt: encode: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.connect(ctx); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = t.conn.SetWriteDeadline(deadline)
	}
	if _, err := t.conn.Write(mqttPublish(t.config.Topic, encoded)); err != nil {
		t.reset()
		return fmt.Errorf("forward mqtt: publish: %w", err)
	}
	return nil
}

// connect opens the broker connection and waits for CONNACK.
func (t *MQTTTransport) connect(ctx context.Context) error {
	if t.conn != nil {
		return nil
	}
	conn, err := t.dial(ctx, "tcp", t.config.Broker)
	if err != nil {
		return fmt.Errorf("forward mqtt: dial %s: %w", t.config.Broker, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write(mqttConnect(t.config)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("forward mqtt: connect: %w", err)
	}

	// CONNACK is a fixed two byte payload after the fixed header.
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		_ = conn.Close()
		return fmt.Errorf("forward mqtt: connack: %w", err)
	}
	if header[0] != 0x20 || header[1] != 0x02 {
		_ = conn.Close()
		return fmt.Errorf("forward mqtt: unexpected connack %x", header)
	}
	if header[3] != 0 {
		_ = conn.Close()
		return fmt.Errorf("forward mqtt: broker refused the connection (code %d)", header[3])
	}
	_ = conn.SetDeadline(time.Time{})
	t.conn = conn
	return nil
}

// reset drops a broken connection so the next send reconnects.
func (t *MQTTTransport) reset() {
	if t.conn != nil {
		_ = t.conn.Close()
	}
	t.conn = nil
}

// Close implements Transport.
func (t *MQTTTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn != nil {
		// DISCONNECT, then close.
		_, _ = t.conn.Write([]byte{0xE0, 0x00})
	}
	t.reset()
	return nil
}

// mqttConnect builds the CONNECT packet.
func mqttConnect(config MQTTConfig) []byte {
	flags := byte(0x02) // clean session
	if config.Username != "" {
		flags |= 0x80
	}
	if config.Password != "" {
		flags |= 0x40
	}

	var body []byte
	body = append(body, mqttString("MQTT")...)
	body = append(body, 4, flags)
	body = binary.BigEndian.AppendUint16(body, 60) // keepalive seconds
	body = append(body, mqttString(config.ClientID)...)
	if config.Username != "" {
		body = append(body, mqttString(config.Username)...)
	}
	if config.Password != "" {
		body = append(body, mqttString(config.Password)...)
	}
	return mqttPacket(0x10, body)
}

// mqttPacket prefixes a body with its fixed header.
func mqttPacket(header byte, body []byte) []byte {
	packet := []byte{header}
	packet = appendLength(packet, len(body))
	return append(packet, body...)
}

// mqttString encodes a UTF-8 length prefixed string.
func mqttString(value string) []byte {
	encoded := make([]byte, 2, 2+len(value))
	binary.BigEndian.PutUint16(encoded, uint16(len(value)))
	return append(encoded, value...)
}

// mqttPublish builds a QoS 0 PUBLISH packet.
func mqttPublish(topic string, payload []byte) []byte {
	body := mqttString(topic)
	body = append(body, payload...)
	return mqttPacket(0x30, body)
}

// appendLength writes the MQTT remaining length varint.
func appendLength(buffer []byte, length int) []byte {
	for {
		digit := byte(length % 128)
		length /= 128
		if length > 0 {
			digit |= 0x80
		}
		buffer = append(buffer, digit)
		if length == 0 {
			return buffer
		}
	}
}
