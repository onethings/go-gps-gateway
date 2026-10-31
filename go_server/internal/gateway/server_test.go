package gateway_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/model"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	data, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("invalid hex %q: %v", s, err)
	}
	return data
}

// newTestLogger buffers server logs instead of forwarding them to t.Logf:
// ServeConn runs in a background goroutine that may log after the test has
// completed, and t.Logf panics in that case.
func newTestLogger(t *testing.T) func(format string, args ...any) {
	var (
		mu   sync.Mutex
		logs strings.Builder
	)
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if logs.Len() > 0 && t.Failed() {
			t.Logf("gateway logs:\n%s", logs.String())
		}
	})
	return func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(&logs, format, args...)
		logs.WriteString("\n")
	}
}

func newServer(t *testing.T, protocolName string, directory gateway.DeviceDirectory, sink gateway.Sink) *gateway.Server {
	t.Helper()
	server, err := gateway.NewServer(
		gateway.DefaultRegistry(), protocolName, directory, sink,
		gateway.WithLogger(newTestLogger(t)),
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return server
}

func startConn(t *testing.T, server *gateway.Server) net.Conn {
	t.Helper()
	client, peer := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ServeConn(ctx, peer)
	}()

	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		_ = peer.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("ServeConn did not stop")
		}
	})
	return client
}

func readN(t *testing.T, conn net.Conn, size int) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, size)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read %d bytes: %v", size, err)
	}
	return buf
}

func readChunk(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf[:n]
}

func write(t *testing.T, conn net.Conn, data []byte) {
	t.Helper()
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func nextPosition(t *testing.T, positions <-chan *model.Position) *model.Position {
	t.Helper()
	select {
	case position := <-positions:
		return position
	case <-time.After(2 * time.Second):
		t.Fatal("no position received")
		return nil
	}
}

// TestGT06 establishes a session with the login frame and then decodes a
// location report on the same connection.
func TestGT06(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("864717003283581"); err != nil {
		t.Fatalf("register: %v", err)
	}

	positions := make(chan *model.Position, 4)
	server := newServer(t, "gt06", directory, gateway.SinkFunc(func(position *model.Position) error {
		positions <- position
		return nil
	}))

	conn := startConn(t, server)

	write(t, conn, mustHex(t, "78780d01086471700328358100093f040d0a"))
	ack := readN(t, conn, 10)
	if ack[3] != 0x01 {
		t.Fatalf("login ack = %x", ack)
	}

	write(t, conn, mustHex(t, "78781f120f0a140e150bc505e51e780293a9e800540000f601006e0055da00035f240d0a"))
	ack = readN(t, conn, 10)
	if ack[3] != 0x12 {
		t.Fatalf("report ack = %x", ack)
	}

	position := nextPosition(t, positions)
	if position.DeviceID != 1 {
		t.Errorf("device id = %d, want 1", position.DeviceID)
	}
	if !position.Valid {
		t.Error("valid = false, want true")
	}
	if position.Latitude < 54.94 || position.Latitude > 54.95 {
		t.Errorf("latitude = %.5f", position.Latitude)
	}
}

// TestJT808 decodes a real 0x0200 report and checks the 0x8001 response.
func TestJT808(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("071604684030"); err != nil {
		t.Fatalf("register: %v", err)
	}

	positions := make(chan *model.Position, 4)
	server := newServer(t, "jt808", directory, gateway.SinkFunc(func(position *model.Position) error {
		positions <- position
		return nil
	}))

	conn := startConn(t, server)

	write(t, conn, mustHex(t, "7e0200004b071604684030004d00000000000c0006005e56a5065c573400330000005226082717271030011b31010de11c01fe0059007e96033ce517c5007e96033ce55dc7007e96033ce549c8e4020064f002034af50101a47e"))

	response := readChunk(t, conn)
	if response[0] != 0x7e || response[1] != 0x80 || response[2] != 0x01 {
		t.Fatalf("response = %x, want 8001", response)
	}

	position := nextPosition(t, positions)
	if !position.Valid {
		t.Error("valid = false, want true")
	}
	if position.Longitude < 106.7 || position.Longitude > 106.8 {
		t.Errorf("longitude = %.5f", position.Longitude)
	}
}

// TestTeltonikaHandshake checks that the IMEI packet is acknowledged with 0x01
// and that the heartbeat byte produces neither a position nor a reply.
func TestTeltonikaHandshake(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("123456789012345"); err != nil {
		t.Fatalf("register: %v", err)
	}

	positions := make(chan *model.Position, 4)
	server := newServer(t, "teltonika", directory, gateway.SinkFunc(func(position *model.Position) error {
		positions <- position
		return nil
	}))

	conn := startConn(t, server)

	write(t, conn, mustHex(t, "000F313233343536373839303132333435"))
	if response := readN(t, conn, 1); response[0] != 0x01 {
		t.Fatalf("handshake response = %x, want 01", response)
	}

	write(t, conn, []byte{0xff})

	select {
	case position := <-positions:
		t.Fatalf("unexpected position: %v", position)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestH02 checks the text protocol, which resolves the device from the frame
// and needs no session binding.
func TestH02(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("135790246811220"); err != nil {
		t.Fatalf("register: %v", err)
	}

	positions := make(chan *model.Position, 4)
	server := newServer(t, "h02", directory, gateway.SinkFunc(func(position *model.Position) error {
		positions <- position
		return nil
	}))

	conn := startConn(t, server)

	write(t, conn, []byte("*HQ,135790246811220,HTBT,100#"))

	position := nextPosition(t, positions)
	if value, _ := position.Get(model.KeyBatteryLevel); value != 100 {
		t.Errorf("battery level = %v, want 100", value)
	}
}

// TestGarbageKeepsConnectionAlive makes sure a resynchronising frame decoder
// does not tear the connection down.
func TestGarbageKeepsConnectionAlive(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("864717003283581"); err != nil {
		t.Fatalf("register: %v", err)
	}

	server := newServer(t, "gt06", directory, nil)
	conn := startConn(t, server)

	write(t, conn, []byte{0x00, 0x01, 0x02})
	write(t, conn, mustHex(t, "78780d01086471700328358100093f040d0a"))
	if ack := readN(t, conn, 10); ack[3] != 0x01 {
		t.Fatalf("login ack = %x", ack)
	}
}

// TestAutoRegister checks that an unknown identifier can be created on the fly.
func TestAutoRegister(t *testing.T) {
	directory := gateway.NewMemoryDirectory()

	positions := make(chan *model.Position, 4)
	server, err := gateway.NewServer(gateway.DefaultRegistry(), "gt06", directory,
		gateway.SinkFunc(func(position *model.Position) error {
			positions <- position
			return nil
		}),
		gateway.WithLogger(newTestLogger(t)),
		gateway.WithAutoRegister(),
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	conn := startConn(t, server)

	write(t, conn, mustHex(t, "78780d01086471700328358100093f040d0a"))
	readN(t, conn, 10)
	write(t, conn, mustHex(t, "78781f120f0a140e150bc505e51e780293a9e800540000f601006e0055da00035f240d0a"))
	readN(t, conn, 10)

	position := nextPosition(t, positions)
	if position.DeviceID != 1 {
		t.Errorf("device id = %d, want 1", position.DeviceID)
	}
	if _, err := directory.UniqueID(position.DeviceID); err != nil {
		t.Errorf("registered device missing: %v", err)
	}
}
