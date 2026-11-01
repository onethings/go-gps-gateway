package gateway_test

import (
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/model"
)

// trackerRecorder collects what the server reports to a session tracker.
type trackerRecorder struct {
	mu      sync.Mutex
	opened  []string
	data    int
	closed  []int64
	devices []int64
}

func (r *trackerRecorder) SessionOpened(protocol string, deviceID int64, remote string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.opened = append(r.opened, protocol)
	r.devices = append(r.devices, deviceID)
}

func (r *trackerRecorder) SessionData(deviceID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data++
}

func (r *trackerRecorder) SessionClosed(deviceID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = append(r.closed, deviceID)
}

func (r *trackerRecorder) counts() (opened, data int, closed []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.opened), r.data, append([]int64(nil), r.closed...)
}

// TestServerReportsSessions covers the contract the session manager relies on:
// the login binds the connection, every frame counts as data and the close is
// reported so the device can be declared offline.
func TestServerReportsSessions(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("864717003283581", "gt06"); err != nil {
		t.Fatalf("register: %v", err)
	}

	tracker := &trackerRecorder{}
	positions := make(chan *model.Position, 4)

	server, err := gateway.NewServer(
		gateway.DefaultRegistry(), "gt06", directory,
		gateway.SinkFunc(func(position *model.Position) error {
			positions <- position
			return nil
		}),
		gateway.WithLogger(newTestLogger(t)),
		gateway.WithSessionTracker(tracker),
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	conn := startConn(t, server)

	write(t, conn, mustHex(t, "78780d01086471700328358100093f040d0a"))
	_ = readN(t, conn, 10)

	write(t, conn, mustHex(t, "78781f120f0a140e150bc505e51e780293a9e800540000f601006e0055da00035f240d0a"))
	_ = readN(t, conn, 10)
	nextPosition(t, positions)

	opened, data, closed := tracker.counts()
	if opened != 1 {
		t.Fatalf("opened %d sessions, want 1", opened)
	}
	if data < 2 {
		t.Errorf("reported %d frames, want at least the login and the report", data)
	}
	if len(closed) != 0 {
		t.Errorf("reported %v closed before the connection ended", closed)
	}

	_ = conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, closed = tracker.counts(); len(closed) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(closed) != 1 || closed[0] != 1 {
		t.Fatalf("closed %v, want the device's session once", closed)
	}
}
