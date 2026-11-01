// Package websocket implements the server side of RFC 6455 that the gateway
// needs for /api/socket: the opening handshake, text and binary messages with
// fragmentation, ping/pong and the close handshake. Extensions
// (permessage-deflate) and the client role are deliberately left out; the
// gateway never opens a WebSocket, only accepts one.
package websocket

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Frame opcodes, RFC 6455 section 5.2.
const (
	ContinuationMessage byte = 0x0
	TextMessage         byte = 0x1
	BinaryMessage       byte = 0x2
	CloseMessage        byte = 0x8
	PingMessage         byte = 0x9
	PongMessage         byte = 0xa
)

// Close status codes, RFC 6455 section 7.4.1.
const (
	StatusNormal          = 1000
	StatusGoingAway       = 1001
	StatusProtocolError   = 1002
	StatusUnsupportedData = 1003
	StatusInvalidPayload  = 1007
	StatusTooLarge        = 1009
)

const (
	// DefaultMaxMessageSize caps one assembled message, so a client cannot
	// make the server buffer an unbounded amount of data.
	DefaultMaxMessageSize = 1 << 20
	// acceptGUID is the constant RFC 6455 appends to the client key.
	acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	// writeTimeout bounds one frame write. A client that stops reading must
	// not pin the writer goroutine forever.
	writeTimeout = 10 * time.Second
)

// Errors reported by the package.
var (
	// ErrBadHandshake is returned by Accept when the request is not a valid
	// WebSocket upgrade.
	ErrBadHandshake = errors.New("websocket: bad handshake")
	// ErrClosed is returned by ReadMessage after the peer closed the
	// connection or the close handshake finished.
	ErrClosed = errors.New("websocket: connection closed")
	// ErrProtocol is returned for a frame that violates RFC 6455.
	ErrProtocol = errors.New("websocket: protocol error")
	// ErrMessageTooLarge is returned when a message exceeds MaxMessageSize.
	ErrMessageTooLarge = errors.New("websocket: message too large")
)

// Conn is one accepted WebSocket connection. ReadMessage may run on one
// goroutine while WriteMessage runs on another; the write side is serialised
// internally.
type Conn struct {
	conn   net.Conn
	reader *bufio.Reader

	// MaxMessageSize caps an assembled message. Zero means the default.
	MaxMessageSize int64

	writeMu   sync.Mutex
	closeOnce sync.Once
}

// Accept upgrades an HTTP request to a WebSocket. On error nothing has been
// written, so the caller still owns the response.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if r.Method != http.MethodGet {
		return nil, fmt.Errorf("%w: method %s", ErrBadHandshake, r.Method)
	}
	if !headerHasToken(r.Header, "Connection", "upgrade") {
		return nil, fmt.Errorf("%w: missing Connection: Upgrade", ErrBadHandshake)
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return nil, fmt.Errorf("%w: missing Upgrade: websocket", ErrBadHandshake)
	}
	if version := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")); version != "13" {
		return nil, fmt.Errorf("%w: unsupported version %q", ErrBadHandshake, version)
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return nil, fmt.Errorf("%w: missing Sec-WebSocket-Key", ErrBadHandshake)
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("%w: response writer cannot be hijacked", ErrBadHandshake)
	}
	raw, buffered, err := hijacker.Hijack()
	if err != nil {
		return nil, fmt.Errorf("websocket: hijack: %w", err)
	}

	// The HTTP server keeps the request deadlines on the raw connection; a
	// socket lives far longer than one request.
	_ = raw.SetDeadline(time.Time{})

	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + AcceptKey(key) + "\r\n\r\n"
	if _, err := buffered.WriteString(response); err != nil {
		raw.Close()
		return nil, fmt.Errorf("websocket: write handshake: %w", err)
	}
	if err := buffered.Flush(); err != nil {
		raw.Close()
		return nil, fmt.Errorf("websocket: flush handshake: %w", err)
	}

	return &Conn{conn: raw, reader: buffered.Reader}, nil
}

// AcceptKey computes the Sec-WebSocket-Accept value for a client key.
func AcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + acceptGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// headerHasToken reports whether a comma separated header contains value,
// ignoring case. It is how Connection: keep-alive, Upgrade is parsed.
func headerHasToken(header http.Header, name, value string) bool {
	for _, line := range header.Values(name) {
		for _, token := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(token), value) {
				return true
			}
		}
	}
	return false
}

// ReadMessage returns the next complete message. Ping and pong frames are
// handled inside and never surface to the caller. A close frame is answered
// with a close frame and reported as ErrClosed.
func (c *Conn) ReadMessage() (byte, []byte, error) {
	var (
		messageType byte
		message     []byte
		fragmented  bool
	)

	for {
		current, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}

		switch current.opcode {
		case PingMessage:
			if err := c.WriteControl(PongMessage, current.payload); err != nil {
				return 0, nil, err
			}
			continue
		case PongMessage:
			continue
		case CloseMessage:
			code := StatusNormal
			if len(current.payload) >= 2 {
				code = int(binary.BigEndian.Uint16(current.payload[:2]))
			}
			_ = c.writeClose(code)
			return 0, nil, ErrClosed
		case TextMessage, BinaryMessage:
			if fragmented {
				c.fail(StatusProtocolError)
				return 0, nil, fmt.Errorf("%w: new message inside a fragmented one", ErrProtocol)
			}
			messageType = current.opcode
			message = current.payload
			fragmented = !current.fin
		case ContinuationMessage:
			if messageType == 0 {
				c.fail(StatusProtocolError)
				return 0, nil, fmt.Errorf("%w: continuation without a message", ErrProtocol)
			}
			message = append(message, current.payload...)
			fragmented = !current.fin
		default:
			c.fail(StatusProtocolError)
			return 0, nil, fmt.Errorf("%w: reserved opcode %d", ErrProtocol, current.opcode)
		}

		if int64(len(message)) > c.maxMessageSize() {
			c.fail(StatusTooLarge)
			return 0, nil, ErrMessageTooLarge
		}
		if fragmented {
			continue
		}
		if messageType == TextMessage && !utf8.Valid(message) {
			c.fail(StatusInvalidPayload)
			return 0, nil, fmt.Errorf("%w: text message is not UTF-8", ErrProtocol)
		}
		return messageType, message, nil
	}
}

func (c *Conn) maxMessageSize() int64 {
	if c.MaxMessageSize > 0 {
		return c.MaxMessageSize
	}
	return DefaultMaxMessageSize
}

type frame struct {
	fin     bool
	opcode  byte
	payload []byte
}

// readFrame reads one frame and enforces the server side rules: client frames
// must be masked, reserved bits must be clear and control frames must be small
// and unfragmented.
func (c *Conn) readFrame() (frame, error) {
	var header [2]byte
	if _, err := io.ReadFull(c.reader, header[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return frame{}, ErrClosed
		}
		return frame{}, err
	}

	fin := header[0]&0x80 != 0
	opcode := header[0] & 0x0f
	if header[0]&0x70 != 0 {
		c.fail(StatusProtocolError)
		return frame{}, fmt.Errorf("%w: reserved bits set", ErrProtocol)
	}
	masked := header[1]&0x80 != 0
	if !masked {
		c.fail(StatusProtocolError)
		return frame{}, fmt.Errorf("%w: client frame is not masked", ErrProtocol)
	}
	if opcode >= CloseMessage && !fin {
		c.fail(StatusProtocolError)
		return frame{}, fmt.Errorf("%w: fragmented control frame", ErrProtocol)
	}

	length := int64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(c.reader, extended[:]); err != nil {
			return frame{}, err
		}
		length = int64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(c.reader, extended[:]); err != nil {
			return frame{}, err
		}
		length = int64(binary.BigEndian.Uint64(extended[:]))
	}
	if opcode >= CloseMessage && length > 125 {
		c.fail(StatusProtocolError)
		return frame{}, fmt.Errorf("%w: control frame of %d bytes", ErrProtocol, length)
	}
	if length < 0 || length > c.maxMessageSize() {
		c.fail(StatusTooLarge)
		return frame{}, ErrMessageTooLarge
	}

	var mask [4]byte
	if _, err := io.ReadFull(c.reader, mask[:]); err != nil {
		return frame{}, err
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return frame{}, err
	}
	for index := range payload {
		payload[index] ^= mask[index%4]
	}

	return frame{fin: fin, opcode: opcode, payload: payload}, nil
}

// WriteText sends one text message.
func (c *Conn) WriteText(payload []byte) error { return c.WriteMessage(TextMessage, payload) }

// WriteMessage sends one unfragmented message of the given type.
func (c *Conn) WriteMessage(messageType byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeFrame(messageType, payload)
}

// WriteControl sends a ping, pong or close frame. A payload longer than 125
// bytes is refused, as the RFC requires.
func (c *Conn) WriteControl(messageType byte, payload []byte) error {
	if len(payload) > 125 {
		return fmt.Errorf("%w: control frame of %d bytes", ErrProtocol, len(payload))
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeFrame(messageType, payload)
}

func (c *Conn) writeFrame(messageType byte, payload []byte) error {
	header := make([]byte, 0, 10)
	header = append(header, 0x80|messageType)
	switch {
	case len(payload) < 126:
		header = append(header, byte(len(payload)))
	case len(payload) <= 0xffff:
		header = append(header, 126, byte(len(payload)>>8), byte(len(payload)))
	default:
		header = append(header, 127)
		var extended [8]byte
		binary.BigEndian.PutUint64(extended[:], uint64(len(payload)))
		header = append(header, extended[:]...)
	}

	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := c.conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func (c *Conn) writeClose(code int) error {
	payload := []byte{byte(code >> 8), byte(code)}
	return c.WriteControl(CloseMessage, payload)
}

// fail reports a protocol violation with a close frame. The close frame is best
// effort: the connection is about to be dropped anyway.
func (c *Conn) fail(code int) {
	_ = c.writeClose(code)
}

// Close finishes the close handshake and closes the underlying connection. It
// is safe to call more than once.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		_ = c.writeClose(StatusNormal)
	})
	return c.conn.Close()
}
