package gateway_test

import (
	"testing"
	"time"

	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/metrics"
)

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not met in time")
}

func TestServerMetrics(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("864717003283581", "gt06"); err != nil {
		t.Fatalf("register: %v", err)
	}

	meters := metrics.New()
	server, err := gateway.NewServer(gateway.DefaultRegistry(), "gt06", directory, nil,
		gateway.WithLogger(newTestLogger(t)),
		gateway.WithMetrics(meters),
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	conn := startConn(t, server)

	write(t, conn, mustHex(t, gt06Login))
	if ack := readN(t, conn, 10); ack[3] != 0x01 {
		t.Fatalf("login ack = %x", ack)
	}
	write(t, conn, mustHex(t, "78781f120f0a140e150bc505e51e780293a9e800540000f601006e0055da00035f240d0a"))
	if ack := readN(t, conn, 10); ack[3] != 0x12 {
		t.Fatalf("report ack = %x", ack)
	}

	if got := meters.ConnectionsActive.With("gt06").Value(); got != 1 {
		t.Errorf("connections = %d, want 1", got)
	}
	if got := meters.Frames.With("gt06").Value(); got < 2 {
		t.Errorf("frames = %d, want at least 2", got)
	}
	if got := meters.Positions.With("gt06").Value(); got < 1 {
		t.Errorf("positions = %d, want at least 1", got)
	}

	_ = conn.Close()
	waitFor(t, func() bool {
		return meters.ConnectionsActive.With("gt06").Value() == 0
	})
}
