package api_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/api"
	"traccar-server_go/go_server/internal/gateway"
)

// deviceProtocols is the part of the postgres store the command handler needs.
type deviceProtocols map[int64]string

func (d deviceProtocols) DeviceProtocol(_ context.Context, deviceID int64) (string, bool, error) {
	protocol, ok := d[deviceID]
	return protocol, ok, nil
}

// TestCommandReachesDevice wires the real gateway, router and HTTP handler
// together over net.Pipe: a GT06 client logs in and receives the command that
// was posted to /api/commands. No sockets are opened, so it also runs in a
// sandbox.
func TestCommandReachesDevice(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("864717003283581", "gt06"); err != nil {
		t.Fatalf("register: %v", err)
	}

	server, err := gateway.NewServer(gateway.DefaultRegistry(), "gt06", directory, nil,
		gateway.WithLogger(func(string, ...any) {}))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	client, peer := net.Pipe()

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
			t.Error("ServeConn did not stop")
		}
	})

	router := gateway.NewRouter(gateway.DefaultRegistry(), directory)
	router.Register("gt06", server)
	handler := api.NewServer(nil,
		api.WithCommands(router, deviceProtocols{1: "gt06"}),
	).Handler()

	login, err := hex.DecodeString("78780d01086471700328358100093f040d0a")
	if err != nil {
		t.Fatalf("hex: %v", err)
	}
	write(t, client, login)

	ack := make([]byte, 10)
	read(t, client, ack)
	if ack[3] != 0x01 {
		t.Fatalf("login ack = %x", ack)
	}

	// net.Pipe has no buffer: the handler blocks in Send until the device
	// side reads, so the request has to run on its own goroutine.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/commands",
		strings.NewReader(`{"deviceId":1,"type":"engineStop"}`))
	request.Header.Set("Content-Type", "application/json")

	status := make(chan int, 1)
	go func() {
		handler.ServeHTTP(recorder, request)
		status <- recorder.Code
	}()

	// 7878 | length | 0x80 | content length | index(4) | "Relay,1#" |
	// message index | crc(2) | 0d0a
	frame := make([]byte, 23)
	read(t, client, frame)

	if code := <-status; code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", code, recorder.Body)
	}

	var response struct {
		DeviceID int64  `json:"deviceId"`
		Protocol string `json:"protocol"`
		Queued   bool   `json:"queued"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, recorder.Body)
	}
	if response.DeviceID != 1 || response.Protocol != "gt06" || response.Queued {
		t.Fatalf("response = %+v: an online device is delivered, not queued", response)
	}

	if !strings.Contains(string(frame), "Relay,1#") {
		t.Fatalf("command frame = %x", frame)
	}
	if frame[0] != 0x78 || frame[1] != 0x78 ||
		frame[len(frame)-2] != 0x0d || frame[len(frame)-1] != 0x0a {
		t.Fatalf("command frame delimiters = %x", frame)
	}

	if !router.Online("gt06", 1) {
		t.Error("online = false after login")
	}
}

func write(t *testing.T, conn net.Conn, data []byte) {
	t.Helper()
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func read(t *testing.T, conn net.Conn, buffer []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("read %d bytes: %v", len(buffer), err)
	}
}
