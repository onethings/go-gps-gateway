package websocket

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// hijackWriter is a ResponseWriter whose Hijack hands out one end of a
// net.Pipe. The sandbox forbids binding a TCP port, so the tests cannot use
// httptest.NewServer.
type hijackWriter struct {
	header   http.Header
	conn     net.Conn
	buffered *bufio.ReadWriter
	status   int
	body     bytes.Buffer
}

func newHijackWriter(conn net.Conn) *hijackWriter {
	return &hijackWriter{
		header:   make(http.Header),
		conn:     conn,
		buffered: bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn)),
	}
}

func (w *hijackWriter) Header() http.Header            { return w.header }
func (w *hijackWriter) Write(data []byte) (int, error) { return w.body.Write(data) }
func (w *hijackWriter) WriteHeader(status int)         { w.status = status }
func (w *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, w.buffered, nil
}

func upgradeRequest() *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/socket", nil)
	request.Header.Set("Connection", "keep-alive, Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	return request
}

// peer is the client side of one connection. It masks its frames the way a
// browser does and reads the unmasked frames the server sends.
type peer struct {
	conn   net.Conn
	reader *bufio.Reader
}

func (p *peer) sendFrame(fin bool, opcode byte, payload []byte) error {
	first := opcode
	if fin {
		first |= 0x80
	}
	frame := []byte{first}
	switch {
	case len(payload) < 126:
		frame = append(frame, 0x80|byte(len(payload)))
	case len(payload) <= 0xffff:
		frame = append(frame, 0x80|126, byte(len(payload)>>8), byte(len(payload)))
	default:
		frame = append(frame, 0x80|127)
		var extended [8]byte
		binary.BigEndian.PutUint64(extended[:], uint64(len(payload)))
		frame = append(frame, extended[:]...)
	}
	mask := [4]byte{0x01, 0x02, 0x03, 0x04}
	frame = append(frame, mask[:]...)
	masked := make([]byte, len(payload))
	for index := range payload {
		masked[index] = payload[index] ^ mask[index%4]
	}
	frame = append(frame, masked...)

	_, err := p.conn.Write(frame)
	return err
}

func (p *peer) readFrame() (byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(p.reader, header[:]); err != nil {
		return 0, nil, err
	}
	opcode := header[0] & 0x0f
	length := int64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(p.reader, extended[:]); err != nil {
			return 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(p.reader, extended[:]); err != nil {
			return 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(extended[:]))
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(p.reader, payload); err != nil {
		return 0, nil, err
	}
	return opcode, payload, nil
}

func readHandshake(t *testing.T, reader *bufio.Reader) http.Header {
	t.Helper()

	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.HasPrefix(line, "HTTP/1.1 101") {
		t.Fatalf("status line = %q, want 101", line)
	}

	header := make(http.Header)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return header
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			header.Add(strings.TrimSpace(key), strings.TrimSpace(value))
		}
	}
}

// acceptOverPipe runs Accept against a net.Pipe and returns both ends.
func acceptOverPipe(t *testing.T) (*Conn, *peer) {
	t.Helper()

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		serverConn.Close()
		clientConn.Close()
	})

	writer := newHijackWriter(serverConn)
	accepted := make(chan *Conn, 1)
	failures := make(chan error, 1)
	go func() {
		conn, err := Accept(writer, upgradeRequest())
		if err != nil {
			failures <- err
			return
		}
		accepted <- conn
	}()

	reader := bufio.NewReader(clientConn)
	header := readHandshake(t, reader)
	if accept := header.Get("Sec-WebSocket-Accept"); accept != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("accept = %q, want the RFC 6455 example", accept)
	}

	select {
	case conn := <-accepted:
		return conn, &peer{conn: clientConn, reader: reader}
	case err := <-failures:
		t.Fatalf("accept: %v", err)
	}
	return nil, nil
}

func TestAcceptKeyMatchesRFCExample(t *testing.T) {
	if got := AcceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("accept key = %q", got)
	}
}

func TestAcceptRejectsBadHandshakes(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*http.Request)
		method string
	}{
		{"wrong method", nil, http.MethodPost},
		{"no connection", func(r *http.Request) { r.Header.Del("Connection") }, http.MethodGet},
		{"no upgrade", func(r *http.Request) { r.Header.Del("Upgrade") }, http.MethodGet},
		{"wrong version", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Version", "8") }, http.MethodGet},
		{"no key", func(r *http.Request) { r.Header.Del("Sec-WebSocket-Key") }, http.MethodGet},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := upgradeRequest()
			request.Method = testCase.method
			if testCase.mutate != nil {
				testCase.mutate(request)
			}
			if _, err := Accept(httptest.NewRecorder(), request); !errors.Is(err, ErrBadHandshake) {
				t.Fatalf("err = %v, want ErrBadHandshake", err)
			}
		})
	}

	// A response writer that cannot be hijacked is a bad handshake too, even
	// when the request itself is valid.
	if _, err := Accept(httptest.NewRecorder(), upgradeRequest()); !errors.Is(err, ErrBadHandshake) {
		t.Fatalf("err = %v, want ErrBadHandshake", err)
	}
}

type readResult struct {
	messageType byte
	message     []byte
	err         error
}

func TestTextMessagesRoundTrip(t *testing.T) {
	conn, client := acceptOverPipe(t)

	writeErr := make(chan error, 1)
	go func() { writeErr <- conn.WriteText([]byte("hello")) }()
	opcode, payload, err := client.readFrame()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if opcode != TextMessage || string(payload) != "hello" {
		t.Fatalf("frame = %d %q", opcode, payload)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write: %v", err)
	}

	results := make(chan readResult, 1)
	go func() {
		messageType, message, err := conn.ReadMessage()
		results <- readResult{messageType, message, err}
	}()
	if err := client.sendFrame(true, TextMessage, []byte(`{"logs":true}`)); err != nil {
		t.Fatalf("send: %v", err)
	}
	result := <-results
	if result.err != nil {
		t.Fatalf("read message: %v", result.err)
	}
	if result.messageType != TextMessage || string(result.message) != `{"logs":true}` {
		t.Fatalf("message = %d %q", result.messageType, result.message)
	}
}

func TestFragmentedMessage(t *testing.T) {
	conn, client := acceptOverPipe(t)

	results := make(chan readResult, 1)
	go func() {
		messageType, message, err := conn.ReadMessage()
		results <- readResult{messageType, message, err}
	}()

	if err := client.sendFrame(false, TextMessage, []byte("he")); err != nil {
		t.Fatalf("send first fragment: %v", err)
	}
	if err := client.sendFrame(true, ContinuationMessage, []byte("llo")); err != nil {
		t.Fatalf("send second fragment: %v", err)
	}

	result := <-results
	if result.err != nil {
		t.Fatalf("read: %v", result.err)
	}
	if string(result.message) != "hello" {
		t.Fatalf("message = %q, want hello", result.message)
	}
}

func TestPingIsAnsweredWithPong(t *testing.T) {
	conn, client := acceptOverPipe(t)

	results := make(chan readResult, 1)
	go func() {
		messageType, message, err := conn.ReadMessage()
		results <- readResult{messageType, message, err}
	}()

	if err := client.sendFrame(true, PingMessage, []byte("beat")); err != nil {
		t.Fatalf("send ping: %v", err)
	}
	opcode, payload, err := client.readFrame()
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if opcode != PongMessage || string(payload) != "beat" {
		t.Fatalf("frame = %d %q, want a pong", opcode, payload)
	}

	if err := client.sendFrame(true, TextMessage, []byte("after")); err != nil {
		t.Fatalf("send text: %v", err)
	}
	result := <-results
	if result.err != nil || string(result.message) != "after" {
		t.Fatalf("result = %+v", result)
	}
}

func TestCloseFrameIsEchoed(t *testing.T) {
	conn, client := acceptOverPipe(t)

	results := make(chan error, 1)
	go func() {
		_, _, err := conn.ReadMessage()
		results <- err
	}()

	if err := client.sendFrame(true, CloseMessage, []byte{0x03, 0xe8}); err != nil {
		t.Fatalf("send close: %v", err)
	}
	opcode, payload, err := client.readFrame()
	if err != nil {
		t.Fatalf("read close: %v", err)
	}
	if opcode != CloseMessage || len(payload) != 2 || binary.BigEndian.Uint16(payload) != StatusNormal {
		t.Fatalf("frame = %d %v, want close 1000", opcode, payload)
	}
	if err := <-results; !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

func TestUnmaskedClientFrameIsRejected(t *testing.T) {
	conn, client := acceptOverPipe(t)

	results := make(chan error, 1)
	go func() {
		_, _, err := conn.ReadMessage()
		results <- err
	}()

	// A client frame without the mask bit violates RFC 6455.
	go func() { _, _ = client.conn.Write([]byte{0x81, 0x02, 'h', 'i'}) }()

	opcode, payload, err := client.readFrame()
	if err != nil {
		t.Fatalf("read close: %v", err)
	}
	if opcode != CloseMessage || binary.BigEndian.Uint16(payload) != StatusProtocolError {
		t.Fatalf("frame = %d %v, want close 1002", opcode, payload)
	}
	if err := <-results; !errors.Is(err, ErrProtocol) {
		t.Fatalf("err = %v, want ErrProtocol", err)
	}
}

func TestOversizedMessageIsRejected(t *testing.T) {
	conn, client := acceptOverPipe(t)
	conn.MaxMessageSize = 8

	results := make(chan error, 1)
	go func() {
		_, _, err := conn.ReadMessage()
		results <- err
	}()

	// The server refuses the frame after its header, so the peer's write of
	// the payload stays blocked until the connection is closed.
	go func() { _ = client.sendFrame(true, TextMessage, []byte("way more than eight bytes")) }()

	opcode, payload, err := client.readFrame()
	if err != nil {
		t.Fatalf("read close: %v", err)
	}
	if opcode != CloseMessage || binary.BigEndian.Uint16(payload) != StatusTooLarge {
		t.Fatalf("frame = %d %v, want close 1009", opcode, payload)
	}
	if err := <-results; !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err = %v, want ErrMessageTooLarge", err)
	}
}

func TestInvalidTextIsRejected(t *testing.T) {
	conn, client := acceptOverPipe(t)

	results := make(chan error, 1)
	go func() {
		_, _, err := conn.ReadMessage()
		results <- err
	}()

	if err := client.sendFrame(true, TextMessage, []byte{0xff, 0xfe}); err != nil {
		t.Fatalf("send: %v", err)
	}
	opcode, payload, err := client.readFrame()
	if err != nil {
		t.Fatalf("read close: %v", err)
	}
	if opcode != CloseMessage || binary.BigEndian.Uint16(payload) != StatusInvalidPayload {
		t.Fatalf("frame = %d %v, want close 1007", opcode, payload)
	}
	if err := <-results; !errors.Is(err, ErrProtocol) {
		t.Fatalf("err = %v, want ErrProtocol", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	conn, client := acceptOverPipe(t)

	// Close writes a close frame before it drops the socket, so the peer reads
	// it first; a second Close must neither block nor write another frame.
	closed := make(chan error, 1)
	go func() { closed <- conn.Close() }()
	opcode, payload, err := client.readFrame()
	if err != nil {
		t.Fatalf("read close: %v", err)
	}
	if opcode != CloseMessage || binary.BigEndian.Uint16(payload) != StatusNormal {
		t.Fatalf("frame = %d %v, want close 1000", opcode, payload)
	}
	if err := <-closed; err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	client.conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := client.readFrame(); err == nil {
		t.Fatal("peer still open after close")
	}
}
