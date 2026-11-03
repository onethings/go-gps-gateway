package forward

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// AMQP 0-9-1 frame types and the frame end marker.
const (
	amqpFrameMethod    = 1
	amqpFrameHeader    = 2
	amqpFrameBody      = 3
	amqpFrameHeartbeat = 8
	amqpFrameEnd       = 0xCE
	// amqpMaxFrame caps the payload of one frame in both directions.
	amqpMaxFrame = 1 << 20
	// amqpBasicContentClassID is the class a content header frame refers to.
	amqpBasicContentClassID = 60
)

// amqpMethod is the class and method pair that identifies a method frame.
type amqpMethod struct {
	class  uint16
	method uint16
}

// The methods a publishing client needs. Ids are from the AMQP 0-9-1 spec,
// confirm is the RabbitMQ extension the Java client also relies on.
var (
	amqpConnectionStart   = amqpMethod{10, 10}
	amqpConnectionStartOK = amqpMethod{10, 11}
	amqpConnectionTune    = amqpMethod{10, 30}
	amqpConnectionTuneOK  = amqpMethod{10, 31}
	amqpConnectionOpen    = amqpMethod{10, 40}
	amqpConnectionOpenOK  = amqpMethod{10, 41}
	amqpConnectionClose   = amqpMethod{10, 50}
	amqpChannelOpen       = amqpMethod{20, 10}
	amqpChannelOpenOK     = amqpMethod{20, 11}
	amqpChannelClose      = amqpMethod{20, 40}
	amqpExchangeDeclare   = amqpMethod{40, 10}
	amqpExchangeDeclareOK = amqpMethod{40, 11}
	amqpBasicPublish      = amqpMethod{60, 40}
	amqpBasicAck          = amqpMethod{60, 80}
	amqpBasicNack         = amqpMethod{60, 120}
	amqpConfirmSelect     = amqpMethod{85, 10}
	amqpConfirmSelectOK   = amqpMethod{85, 11}
)

// AMQPConfig configures the AMQP 0-9-1 forwarder. It is the Go counterpart of
// the Java AmqpClient: URL is amqp://user:password@host:port/vhost, Exchange is
// declared as a durable topic exchange and Topic is the routing key.
type AMQPConfig struct {
	URL      string
	Exchange string
	Topic    string
	// Dial defaults to net.Dialer.DialContext; tests pass net.Pipe.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// AMQPTransport publishes the position or event JSON to a RabbitMQ exchange.
// Publisher confirms make a rejected or unreachable message a Send error, so
// the forwarder retries instead of silently losing it.
type AMQPTransport struct {
	config AMQPConfig
	dial   func(ctx context.Context, network, address string) (net.Conn, error)

	mu       sync.Mutex
	conn     net.Conn
	reader   *bufio.Reader
	frameMax int
	// delivery is the delivery tag the next message expects a confirm for.
	delivery uint64
}

// NewAMQP creates the forwarder.
func NewAMQP(config AMQPConfig) (*AMQPTransport, error) {
	if config.URL == "" {
		return nil, fmt.Errorf("forward amqp: empty URL")
	}
	if config.Exchange == "" {
		return nil, fmt.Errorf("forward amqp: empty exchange")
	}
	if config.Topic == "" {
		return nil, fmt.Errorf("forward amqp: empty topic")
	}
	dial := config.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		dial = dialer.DialContext
	}
	return &AMQPTransport{config: config, dial: dial}, nil
}

// Name implements Transport.
func (t *AMQPTransport) Name() string { return "amqp" }

// Send implements Transport.
func (t *AMQPTransport) Send(ctx context.Context, message Message) error {
	encoded, err := json.Marshal(payload(message))
	if err != nil {
		return fmt.Errorf("forward amqp: encode: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.connect(ctx); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = t.conn.SetDeadline(deadline)
	}
	t.delivery++
	if err := t.publish(encoded); err != nil {
		t.reset()
		return err
	}
	if err := t.await(); err != nil {
		t.reset()
		return err
	}
	_ = t.conn.SetDeadline(time.Time{})
	return nil
}

// connect opens the connection, declares the exchange and switches the channel
// into confirm mode.
func (t *AMQPTransport) connect(ctx context.Context) error {
	if t.conn != nil {
		return nil
	}
	target, err := url.Parse(t.config.URL)
	if err != nil {
		return fmt.Errorf("forward amqp: url %q: %w", t.config.URL, err)
	}
	address := target.Host
	if !strings.Contains(address, ":") {
		address += ":5672"
	}
	conn, err := t.dial(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("forward amqp: dial %s: %w", address, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	t.conn, t.reader, t.delivery = conn, bufio.NewReader(conn), 0
	if err := t.handshake(target); err != nil {
		t.reset()
		return err
	}
	_ = conn.SetDeadline(time.Time{})
	return nil
}

// handshake runs connection.start through exchange.declare.
func (t *AMQPTransport) handshake(target *url.URL) error {
	if _, err := t.conn.Write([]byte("AMQP\x00\x00\x09\x01")); err != nil {
		return fmt.Errorf("forward amqp: hello: %w", err)
	}
	if _, err := t.expect(amqpConnectionStart); err != nil {
		return fmt.Errorf("forward amqp: start: %w", err)
	}

	var startOK amqpWriter
	startOK.long(0) // client properties: empty table
	startOK.shortstr("PLAIN")
	startOK.longstr([]byte(amqpCredentials(target)))
	startOK.shortstr("en_US")
	if err := t.sendMethod(0, amqpConnectionStartOK, startOK.data); err != nil {
		return fmt.Errorf("forward amqp: start-ok: %w", err)
	}

	tune, err := t.expect(amqpConnectionTune)
	if err != nil {
		return fmt.Errorf("forward amqp: tune: %w", err)
	}
	if len(tune) < 8 {
		return fmt.Errorf("forward amqp: short tune")
	}
	frameMax := binary.BigEndian.Uint32(tune[2:6])
	if frameMax == 0 || frameMax > amqpMaxFrame {
		frameMax = amqpMaxFrame
	}
	if frameMax < 4096 {
		return fmt.Errorf("forward amqp: negotiated frame max %d is too small", frameMax)
	}
	t.frameMax = int(frameMax)

	var tuneOK amqpWriter
	tuneOK.short(binary.BigEndian.Uint16(tune[0:2])) // channel max: keep the server's
	tuneOK.long(frameMax)
	tuneOK.short(0) // heartbeat: the forwarder publishes on demand, so a reader goroutine would be wasted
	if err := t.sendMethod(0, amqpConnectionTuneOK, tuneOK.data); err != nil {
		return fmt.Errorf("forward amqp: tune-ok: %w", err)
	}

	var open amqpWriter
	open.shortstr(amqpVhost(target))
	open.shortstr("") // deprecated capabilities field
	open.octet(0)     // insist bit
	if err := t.sendMethod(0, amqpConnectionOpen, open.data); err != nil {
		return fmt.Errorf("forward amqp: open: %w", err)
	}
	if _, err := t.expect(amqpConnectionOpenOK); err != nil {
		return fmt.Errorf("forward amqp: open-ok: %w", err)
	}

	if err := t.sendMethod(1, amqpChannelOpen, []byte{0}); err != nil { // reserved shortstr
		return fmt.Errorf("forward amqp: channel open: %w", err)
	}
	if _, err := t.expect(amqpChannelOpenOK); err != nil {
		return fmt.Errorf("forward amqp: channel open-ok: %w", err)
	}

	if err := t.sendMethod(1, amqpConfirmSelect, nil); err != nil {
		return fmt.Errorf("forward amqp: confirm select: %w", err)
	}
	if _, err := t.expect(amqpConfirmSelectOK); err != nil {
		return fmt.Errorf("forward amqp: confirm select-ok: %w", err)
	}

	var declare amqpWriter
	declare.short(0) // reserved
	declare.shortstr(t.config.Exchange)
	declare.shortstr("topic")
	declare.octet(0x02) // passive off, durable on, auto-delete/internal/no-wait off
	declare.long(0)     // arguments: empty table
	if err := t.sendMethod(1, amqpExchangeDeclare, declare.data); err != nil {
		return fmt.Errorf("forward amqp: exchange declare: %w", err)
	}
	if _, err := t.expect(amqpExchangeDeclareOK); err != nil {
		return fmt.Errorf("forward amqp: exchange declare-ok: %w", err)
	}
	return nil
}

// publish writes the method, the content header and the body frames. A body
// larger than the negotiated frame size is split.
func (t *AMQPTransport) publish(body []byte) error {
	var publish amqpWriter
	publish.short(0)
	publish.shortstr(t.config.Exchange)
	publish.shortstr(t.config.Topic)
	publish.octet(0) // mandatory and immediate off
	frame := amqpFrame(amqpFrameMethod, 1, amqpMethodPayload(amqpBasicPublish, publish.data))

	var header amqpWriter
	header.short(amqpBasicContentClassID)
	header.short(0) // weight
	header.longlong(uint64(len(body)))
	header.short(0x9000) // content-type and delivery-mode are present
	header.shortstr("text/plain")
	header.octet(2) // delivery mode: persistent
	frame = append(frame, amqpFrame(amqpFrameHeader, 1, header.data)...)

	limit := t.frameMax - 8
	for len(body) > 0 {
		size := len(body)
		if size > limit {
			size = limit
		}
		frame = append(frame, amqpFrame(amqpFrameBody, 1, body[:size])...)
		body = body[size:]
	}
	if _, err := t.conn.Write(frame); err != nil {
		return fmt.Errorf("forward amqp: publish: %w", err)
	}
	return nil
}

// await reads until the broker confirms the message.
func (t *AMQPTransport) await() error {
	for {
		kind, payload, err := t.readFrame()
		if err != nil {
			return fmt.Errorf("forward amqp: confirm: %w", err)
		}
		if kind == amqpFrameHeartbeat {
			continue
		}
		if kind != amqpFrameMethod || len(payload) < 4 {
			return fmt.Errorf("forward amqp: unexpected frame %d while waiting for a confirm", kind)
		}
		switch method := amqpMethodOf(payload); method {
		case amqpBasicAck:
			if len(payload) < 12 {
				return fmt.Errorf("forward amqp: short ack")
			}
			if binary.BigEndian.Uint64(payload[4:12]) >= t.delivery {
				return nil
			}
		case amqpBasicNack:
			return fmt.Errorf("forward amqp: broker rejected the message")
		case amqpChannelClose, amqpConnectionClose:
			return fmt.Errorf("forward amqp: broker closed the connection (reply code %d)",
				amqpReplyCode(payload[4:]))
		}
	}
}

// expect reads one method frame and checks it is the one the handshake wants.
func (t *AMQPTransport) expect(want amqpMethod) ([]byte, error) {
	kind, payload, err := t.readFrame()
	if err != nil {
		return nil, err
	}
	if kind != amqpFrameMethod || len(payload) < 4 {
		return nil, fmt.Errorf("unexpected frame type %d", kind)
	}
	if got := amqpMethodOf(payload); got != want {
		return nil, fmt.Errorf("unexpected method %d.%d, want %d.%d",
			got.class, got.method, want.class, want.method)
	}
	return payload[4:], nil
}

func (t *AMQPTransport) sendMethod(channel uint16, method amqpMethod, args []byte) error {
	_, err := t.conn.Write(amqpFrame(amqpFrameMethod, channel, amqpMethodPayload(method, args)))
	return err
}

func (t *AMQPTransport) readFrame() (byte, []byte, error) {
	var header [7]byte
	if _, err := io.ReadFull(t.reader, header[:]); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(header[3:7])
	if size > amqpMaxFrame {
		return 0, nil, fmt.Errorf("frame too large (%d bytes)", size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(t.reader, payload); err != nil {
		return 0, nil, err
	}
	var end [1]byte
	if _, err := io.ReadFull(t.reader, end[:]); err != nil {
		return 0, nil, err
	}
	if end[0] != amqpFrameEnd {
		return 0, nil, fmt.Errorf("bad frame end 0x%02x", end[0])
	}
	return header[0], payload, nil
}

// reset drops a broken connection so the next send reconnects and re-declares.
func (t *AMQPTransport) reset() {
	if t.conn != nil {
		_ = t.conn.Close()
	}
	t.conn = nil
	t.reader = nil
	t.frameMax = 0
	t.delivery = 0
}

// Close implements Transport.
func (t *AMQPTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reset()
	return nil
}

// amqpCredentials is the PLAIN response: NUL, login, NUL, password.
func amqpCredentials(target *url.URL) string {
	login, password := "guest", "guest"
	if target.User != nil {
		if name := target.User.Username(); name != "" {
			login = name
		}
		if secret, ok := target.User.Password(); ok {
			password = secret
		}
	}
	return "\x00" + login + "\x00" + password
}

// amqpVhost reads the virtual host from the URL path, which defaults to "/".
func amqpVhost(target *url.URL) string {
	if target.Path == "" || target.Path == "/" {
		return "/"
	}
	return strings.TrimPrefix(target.Path, "/")
}

func amqpMethodOf(payload []byte) amqpMethod {
	return amqpMethod{
		class:  binary.BigEndian.Uint16(payload[0:2]),
		method: binary.BigEndian.Uint16(payload[2:4]),
	}
}

// amqpReplyCode is the reply-code that starts the arguments of a close method.
func amqpReplyCode(args []byte) uint16 {
	if len(args) < 2 {
		return 0
	}
	return binary.BigEndian.Uint16(args[0:2])
}

// amqpMethodPayload prepends the class and method ids to a method's arguments.
func amqpMethodPayload(method amqpMethod, args []byte) []byte {
	payload := make([]byte, 4, 4+len(args))
	binary.BigEndian.PutUint16(payload[0:2], method.class)
	binary.BigEndian.PutUint16(payload[2:4], method.method)
	return append(payload, args...)
}

// amqpFrame wraps a payload in the generic frame header and end marker.
func amqpFrame(kind byte, channel uint16, payload []byte) []byte {
	frame := make([]byte, 7, 8+len(payload))
	frame[0] = kind
	binary.BigEndian.PutUint16(frame[1:3], channel)
	binary.BigEndian.PutUint32(frame[3:7], uint32(len(payload)))
	frame = append(frame, payload...)
	return append(frame, amqpFrameEnd)
}

// amqpWriter builds the AMQP field types.
type amqpWriter struct{ data []byte }

func (w *amqpWriter) octet(value byte) {
	w.data = append(w.data, value)
}

func (w *amqpWriter) short(value uint16) {
	w.data = binary.BigEndian.AppendUint16(w.data, value)
}

func (w *amqpWriter) long(value uint32) {
	w.data = binary.BigEndian.AppendUint32(w.data, value)
}

func (w *amqpWriter) longlong(value uint64) {
	w.data = binary.BigEndian.AppendUint64(w.data, value)
}

func (w *amqpWriter) shortstr(value string) {
	w.octet(byte(len(value)))
	w.data = append(w.data, value...)
}

func (w *amqpWriter) longstr(value []byte) {
	w.long(uint32(len(value)))
	w.data = append(w.data, value...)
}
