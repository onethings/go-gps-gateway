package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

// fakeLatestPositions serves the seed of a socket connection.
type fakeLatestPositions struct {
	mu        sync.Mutex
	results   []model.Position
	err       error
	deviceIDs []int64
	calls     int
}

func (f *fakeLatestPositions) LatestPositions(_ context.Context, deviceIDs []int64) ([]model.Position, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.deviceIDs = deviceIDs
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

func (f *fakeLatestPositions) lastDeviceIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deviceIDs
}

// hijackResponseWriter is a ResponseWriter whose Hijack hands out one end of a
// net.Pipe, because the sandbox cannot bind a TCP port for httptest.NewServer.
type hijackResponseWriter struct {
	header   http.Header
	conn     net.Conn
	buffered *bufio.ReadWriter
	status   int
	body     bytes.Buffer
}

func newHijackResponseWriter(conn net.Conn) *hijackResponseWriter {
	return &hijackResponseWriter{
		header:   make(http.Header),
		conn:     conn,
		buffered: bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn)),
	}
}

func (w *hijackResponseWriter) Header() http.Header            { return w.header }
func (w *hijackResponseWriter) Write(data []byte) (int, error) { return w.body.Write(data) }
func (w *hijackResponseWriter) WriteHeader(status int)         { w.status = status }
func (w *hijackResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, w.buffered, nil
}

// socketPeer is the browser side of one socket connection.
type socketPeer struct {
	conn   net.Conn
	reader *bufio.Reader
}

func (p *socketPeer) send(text string) error {
	payload := []byte(text)
	frame := []byte{0x80 | 0x1}
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
	mask := [4]byte{0x09, 0x08, 0x07, 0x06}
	frame = append(frame, mask[:]...)
	for index := range payload {
		frame = append(frame, payload[index]^mask[index%4])
	}
	_, err := p.conn.Write(frame)
	return err
}

func (p *socketPeer) receive(t *testing.T) []byte {
	t.Helper()
	if err := p.conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	defer p.conn.SetReadDeadline(time.Time{})

	var header [2]byte
	if _, err := io.ReadFull(p.reader, header[:]); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	length := int64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(p.reader, extended[:]); err != nil {
			t.Fatalf("read length: %v", err)
		}
		length = int64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(p.reader, extended[:]); err != nil {
			t.Fatalf("read length: %v", err)
		}
		length = int64(binary.BigEndian.Uint64(extended[:]))
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(p.reader, payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	return payload
}

// expectSilence fails when a frame arrives within the short grace period.
func (p *socketPeer) expectSilence(t *testing.T) {
	t.Helper()
	if err := p.conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	defer p.conn.SetReadDeadline(time.Time{})

	var header [2]byte
	if _, err := io.ReadFull(p.reader, header[:]); err == nil {
		t.Fatal("received a frame the scope should have dropped")
	}
}

// openSocket upgrades through the real handler and returns the browser side.
func openSocket(t *testing.T, handler http.Handler, target string) *socketPeer {
	t.Helper()

	serverConn, clientConn := net.Pipe()
	writer := newHijackResponseWriter(serverConn)
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(writer, request)
	}()
	t.Cleanup(func() {
		serverConn.Close()
		clientConn.Close()
		<-done
	})

	reader := bufio.NewReader(clientConn)
	if err := clientConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v (body %s)", err, writer.body.String())
	}
	if !strings.HasPrefix(line, "HTTP/1.1 101") {
		t.Fatalf("status line = %q (body %s)", line, writer.body.String())
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		if strings.TrimRight(line, "\r\n") == "" {
			break
		}
	}
	if err := clientConn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("deadline: %v", err)
	}

	return &socketPeer{conn: clientConn, reader: reader}
}

// waitForConnections blocks until the hub holds want subscribers, so a test
// does not publish before the handler has finished subscribing.
func waitForConnections(t *testing.T, hub *SocketHub, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for hub.Count() != want {
		if time.Now().After(deadline) {
			t.Fatalf("connections = %d, want %d", hub.Count(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func socketServer(hub *SocketHub, latest LatestPositionQuerier, principal *auth.Principal) http.Handler {
	return NewServer(nil, WithSocket(hub, latest), withTestToken(principal)).Handler()
}

func TestSocketRouteNeedsConfiguration(t *testing.T) {
	handler := NewServer(nil, withTestToken(auth.NewPrincipal(1, true, []auth.Scope{auth.ScopeAll}, nil))).Handler()

	recorder := withToken(t, handler, http.MethodGet, "/api/socket?token="+testToken, "")
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestSocketRequiresValidTokenAndScope(t *testing.T) {
	hub := NewSocketHub()
	latest := &fakeLatestPositions{}
	handler := socketServer(hub, latest, auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadPositions}, []int64{7}))

	recorder := withToken(t, handler, http.MethodGet, "/api/socket", "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, want 401 (body %s)", recorder.Code, recorder.Body)
	}
	recorder = withToken(t, handler, http.MethodGet, "/api/socket?token=trc_unknown", "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token status = %d, want 401 (body %s)", recorder.Code, recorder.Body)
	}

	noScope := socketServer(hub, latest, auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadMetrics}, nil))
	recorder = withToken(t, noScope, http.MethodGet, "/api/socket?token="+testToken, "")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("scope status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestSocketSeedsLatestPositions(t *testing.T) {
	hub := NewSocketHub()
	latest := &fakeLatestPositions{results: []model.Position{{
		ID:         11,
		DeviceID:   7,
		Protocol:   "gt06",
		Time:       time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
		ServerTime: time.Date(2026, 11, 1, 12, 0, 1, 0, time.UTC),
		Valid:      true,
		Latitude:   51.5,
		Longitude:  0.25,
		Attributes: map[string]any{"sat": 9},
	}}}

	handler := socketServer(hub, latest, auth.NewPrincipal(1, true, []auth.Scope{auth.ScopeAll}, nil))
	peer := openSocket(t, handler, "/api/socket?token="+testToken)

	var message struct {
		Positions []positionResponse `json:"positions"`
	}
	if err := json.Unmarshal(peer.receive(t), &message); err != nil {
		t.Fatalf("decode seed: %v", err)
	}
	if len(message.Positions) != 1 || message.Positions[0].ID != 11 {
		t.Fatalf("seed = %+v", message.Positions)
	}
	if message.Positions[0].Attributes["sat"] != float64(9) {
		t.Errorf("attributes = %v", message.Positions[0].Attributes)
	}
	if ids := latest.lastDeviceIDs(); ids != nil {
		t.Errorf("device ids = %v, want nil for an admin", ids)
	}
	if hub.Count() != 1 {
		t.Errorf("connections = %d, want 1", hub.Count())
	}
}

func TestSocketBroadcastsWithinScope(t *testing.T) {
	hub := NewSocketHub()
	handler := socketServer(hub, &fakeLatestPositions{}, auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadPositions}, []int64{7}))
	peer := openSocket(t, handler, "/api/socket?token="+testToken)

	// Wait out the seed so ordering is deterministic: the seed is empty here,
	// but it still arrives first.
	peer.receive(t)

	hub.PublishPosition(&model.Position{ID: 1, DeviceID: 8})
	peer.expectSilence(t)

	hub.PublishPosition(&model.Position{ID: 2, DeviceID: 7, Protocol: "gt06"})
	var message struct {
		Positions []positionResponse `json:"positions"`
	}
	if err := json.Unmarshal(peer.receive(t), &message); err != nil {
		t.Fatalf("decode position: %v", err)
	}
	if len(message.Positions) != 1 || message.Positions[0].ID != 2 {
		t.Fatalf("positions = %+v", message.Positions)
	}

	// The token has no read:alerts, so an event for the same device is not sent.
	hub.PublishEvent(&model.Event{ID: 5, DeviceID: 7, Type: model.EventPowerCut})
	peer.expectSilence(t)
}

func TestSocketSendsEventsAndDevicesWithinTheirScopes(t *testing.T) {
	hub := NewSocketHub()
	handler := socketServer(hub, &fakeLatestPositions{},
		auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadAlerts, auth.ScopeReadDevices}, []int64{7}))
	peer := openSocket(t, handler, "/api/socket?token="+testToken)
	waitForConnections(t, hub, 1)

	// No read:positions, so the connection is not seeded.
	hub.PublishEvent(&model.Event{ID: 5, DeviceID: 7, Type: model.EventPowerCut})
	var events struct {
		Events []eventResponse `json:"events"`
	}
	if err := json.Unmarshal(peer.receive(t), &events); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if len(events.Events) != 1 || events.Events[0].ID != 5 {
		t.Fatalf("events = %+v", events.Events)
	}

	hub.PublishDevice(&model.Device{ID: 7, UniqueID: "862000000000007", Name: "van"})
	var devices struct {
		Devices []deviceResponse `json:"devices"`
	}
	if err := json.Unmarshal(peer.receive(t), &devices); err != nil {
		t.Fatalf("decode device: %v", err)
	}
	if len(devices.Devices) != 1 || devices.Devices[0].ID != 7 || devices.Devices[0].Name != "van" {
		t.Fatalf("devices = %+v", devices.Devices)
	}
}

func TestSocketIgnoresClientMessagesAndRemovesClosedConnections(t *testing.T) {
	hub := NewSocketHub()
	handler := socketServer(hub, &fakeLatestPositions{}, auth.NewPrincipal(1, true, []auth.Scope{auth.ScopeAll}, nil))
	peer := openSocket(t, handler, "/api/socket?token="+testToken)
	peer.receive(t)

	// traccar-web asks for logs; there is no log stream, but the message must
	// not break the connection.
	sendErr := make(chan error, 1)
	go func() { sendErr <- peer.send(`{"logs":true}`) }()
	if err := <-sendErr; err != nil {
		t.Fatalf("send: %v", err)
	}

	hub.PublishPosition(&model.Position{ID: 3, DeviceID: 7})
	if _, err := interpretPosition(t, peer.receive(t)); err != nil {
		t.Fatalf("position after a client message: %v", err)
	}

	// Closing the browser side must unregister the connection.
	peer.conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for hub.Count() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hub.Count() != 0 {
		t.Errorf("connections = %d after close, want 0", hub.Count())
	}
}

func interpretPosition(t *testing.T, payload []byte) (int64, error) {
	t.Helper()
	var message struct {
		Positions []positionResponse `json:"positions"`
	}
	if err := json.Unmarshal(payload, &message); err != nil {
		return 0, err
	}
	if len(message.Positions) != 1 {
		return 0, errors.New("expected exactly one position")
	}
	return message.Positions[0].ID, nil
}
