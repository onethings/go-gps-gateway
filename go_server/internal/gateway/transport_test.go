package gateway_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/model"
)

// fakePacketConn is an in-memory net.PacketConn: packets are queued by the test
// and the writes the server makes are captured. Binding a real UDP socket is not
// always allowed, and a fake keeps the test deterministic anyway.
type fakePacketConn struct {
	packets chan fakePacket
	written chan fakePacket
	closed  chan struct{}
	once    sync.Once
}

type fakePacket struct {
	data []byte
	addr net.Addr
}

func newFakePacketConn() *fakePacketConn {
	return &fakePacketConn{
		packets: make(chan fakePacket, 8),
		written: make(chan fakePacket, 8),
		closed:  make(chan struct{}),
	}
}

func (c *fakePacketConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	select {
	case packet := <-c.packets:
		return copy(buffer, packet.data), packet.addr, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *fakePacketConn) WriteTo(payload []byte, addr net.Addr) (int, error) {
	select {
	case c.written <- fakePacket{data: append([]byte(nil), payload...), addr: addr}:
		return len(payload), nil
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *fakePacketConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *fakePacketConn) LocalAddr() net.Addr              { return fakeAddr("127.0.0.1:15023") }
func (c *fakePacketConn) SetDeadline(time.Time) error      { return nil }
func (c *fakePacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakePacketConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddr string

func (a fakeAddr) Network() string { return "udp" }
func (a fakeAddr) String() string  { return string(a) }

// TestUDPProtocolDecodesDatagrams sends the h02 heartbeat over a datagram
// listener: the protocol identifies the device in every packet, so no session is
// needed for the first position.
func TestUDPProtocolDecodesDatagrams(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("135790246811220", "h02"); err != nil {
		t.Fatalf("register: %v", err)
	}

	written := make(chan *model.Position, 4)
	server := newServer(t, "h02", directory, gateway.SinkFunc(func(position *model.Position) error {
		written <- position
		return nil
	}))

	conn := newFakePacketConn()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ServePacket(ctx, conn)
	}()

	conn.packets <- fakePacket{data: []byte("*HQ,135790246811220,HTBT,88#"), addr: fakeAddr("10.0.0.9:5000")}

	select {
	case position := <-written:
		if value, _ := position.Get(model.KeyBatteryLevel); value != 88 {
			t.Errorf("battery level = %v, want 88", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no position decoded from the datagram")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServePacket did not stop")
	}
}

// TestHTTPSProtocolDecodesRequest drives the OsmAnd handler without a socket,
// which is what the gateway mounts on its HTTP listener.
func TestHTTPProtocolDecodesRequest(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("123456789012345", "osmand"); err != nil {
		t.Fatalf("register: %v", err)
	}

	positions := make(chan *model.Position, 4)
	server := newServer(t, "osmand", directory, gateway.SinkFunc(func(position *model.Position) error {
		positions <- position
		return nil
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet,
		"/?id=123456789012345&lat=54.94535&lon=24.01762&timestamp=1700000000&speed=12.5&bearing=90&altitude=120",
		nil)
	server.HTTPHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if body, _ := io.ReadAll(recorder.Result().Body); string(body) != "{}" {
		t.Errorf("body = %q", body)
	}

	select {
	case position := <-positions:
		if position.Latitude != 54.94535 || position.Longitude != 24.01762 {
			t.Errorf("position = %v,%v", position.Latitude, position.Longitude)
		}
		if position.Speed != 12.5 {
			t.Errorf("speed = %v", position.Speed)
		}
		if position.Time.Unix() != 1700000000 {
			t.Errorf("time = %v", position.Time)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no position decoded from the request")
	}
}

// TestHTTPProtocolRejectsUnknownDevice makes sure an unknown identifier is the
// device's problem: the handler answers, but no position is stored.
func TestHTTPProtocolRejectsUnknownDevice(t *testing.T) {
	server := newServer(t, "osmand", gateway.NewMemoryDirectory(), gateway.SinkFunc(
		func(*model.Position) error {
			t.Error("position stored for an unknown device")
			return nil
		}))

	recorder := httptest.NewRecorder()
	server.HTTPHandler().ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/?id=unknown&lat=1&lon=2&timestamp=1700000000", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
}

// TestMQTTProtocolDecodesPublish walks the MQTT handshake the way a device does:
// CONNECT, SUBSCRIBE, then a publish that carries the position.
func TestMQTTProtocolDecodesPublish(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("123456789012345", "mqtt"); err != nil {
		t.Fatalf("register: %v", err)
	}

	positions := make(chan *model.Position, 4)
	server := newServer(t, "mqtt", directory, gateway.SinkFunc(func(position *model.Position) error {
		positions <- position
		return nil
	}))

	conn := startConn(t, server)

	connect := []byte{0x10, 12, 0x00, 0x04, 'M', 'Q', 'T', 'T', 0x04, 0x02, 0x00, 0x3c, 0x00, 0x00}
	write(t, conn, connect)
	if ack := readN(t, conn, 4); ack[0] != 0x20 || ack[3] != 0 {
		t.Fatalf("connack = %x", ack)
	}

	payload := "id=123456789012345&lat=54.94535&lon=24.01762&timestamp=1700000000"
	topic := "traccar"
	publish := []byte{0x30}
	publish = append(publish, byte(2+len(topic)+len(payload)))
	publish = append(publish, byte(len(topic)>>8), byte(len(topic)))
	publish = append(publish, topic...)
	publish = append(publish, payload...)
	write(t, conn, publish)

	select {
	case position := <-positions:
		if position.Latitude != 54.94535 {
			t.Errorf("latitude = %v", position.Latitude)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no position decoded from the publish")
	}
}
