package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// statusRecorder is a Store that remembers the transitions it was given.
type statusRecorder struct {
	mu       sync.Mutex
	statuses []statusChange
	fail     error
}

type statusChange struct {
	deviceID int64
	status   string
	at       time.Time
}

func (r *statusRecorder) SetDeviceStatus(
	_ context.Context, deviceID int64, status string, at time.Time,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.statuses = append(r.statuses, statusChange{deviceID: deviceID, status: status, at: at})
	return r.fail
}

func (r *statusRecorder) recorded() []statusChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]statusChange(nil), r.statuses...)
}

// eventRecorder is an Events sink that remembers the events it was given.
type eventRecorder struct {
	mu     sync.Mutex
	events []*model.Event
	fail   error
}

func (r *eventRecorder) InsertEvents(_ context.Context, events []*model.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, events...)
	return r.fail
}

func (r *eventRecorder) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	types := make([]string, 0, len(r.events))
	for _, event := range r.events {
		types = append(types, event.Type)
	}
	return types
}

// waitFor polls until the condition holds or the test times out. The manager
// writes on its own goroutine, so every assertion about the database has to
// wait rather than assume.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// runManager starts the manager and stops it at the end of the test.
func runManager(t *testing.T, manager *Manager) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestConnectRecordsOnlineStatusAndEvent(t *testing.T) {
	statuses := &statusRecorder{}
	events := &eventRecorder{}

	manager := New(Config{Store: statuses, Events: events})
	runManager(t, manager)

	manager.Connect("gt06", 7, "10.0.0.1:5000")

	if !manager.Online(7) {
		t.Error("device is not online after Connect")
	}

	waitFor(t, "the online transition", func() bool { return len(statuses.recorded()) == 1 })
	change := statuses.recorded()[0]
	if change.status != StatusOnline || change.deviceID != 7 {
		t.Errorf("recorded %+v, want 7 online", change)
	}

	waitFor(t, "the deviceOnline event", func() bool { return len(events.types()) == 1 })
	if got := events.types()[0]; got != model.EventDeviceOnline {
		t.Errorf("event %q, want %q", got, model.EventDeviceOnline)
	}
}

func TestDisconnectRecordsOfflineStatusAndEvent(t *testing.T) {
	statuses := &statusRecorder{}
	events := &eventRecorder{}

	manager := New(Config{Store: statuses, Events: events})
	runManager(t, manager)

	manager.Connect("gt06", 7, "")
	manager.Disconnect(7)

	if manager.Online(7) {
		t.Error("device is still online after Disconnect")
	}

	waitFor(t, "both transitions", func() bool { return len(statuses.recorded()) == 2 })
	if got := statuses.recorded()[1].status; got != StatusOffline {
		t.Errorf("last status %q, want %q", got, StatusOffline)
	}

	waitFor(t, "both events", func() bool { return len(events.types()) == 2 })
	if got := events.types()[1]; got != model.EventDeviceOffline {
		t.Errorf("last event %q, want %q", got, model.EventDeviceOffline)
	}
}

// TestReconnectKeepsOneSession covers a device that comes back before its
// disconnect was seen: one session, no extra online row.
func TestReconnectKeepsOneSession(t *testing.T) {
	statuses := &statusRecorder{}

	manager := New(Config{Store: statuses})
	runManager(t, manager)

	manager.Connect("gt06", 7, "10.0.0.1:1")
	manager.Connect("gt06", 7, "10.0.0.1:2")

	if count := manager.OnlineCount(); count != 1 {
		t.Errorf("online count %d, want 1", count)
	}
	waitFor(t, "the online transition", func() bool { return len(statuses.recorded()) == 1 })
	time.Sleep(20 * time.Millisecond)
	if got := len(statuses.recorded()); got != 1 {
		t.Errorf("wrote %d statuses, want 1 for a reconnect", got)
	}
}

func TestIdleSessionExpiresAndClosesTheConnection(t *testing.T) {
	statuses := &statusRecorder{}
	events := &eventRecorder{}

	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	type closedConn struct {
		protocol string
		deviceID int64
	}
	var (
		closedMu sync.Mutex
		closedOn []closedConn
	)

	manager := New(Config{
		Store:   statuses,
		Events:  events,
		Timeout: 2 * time.Minute,
		Now:     func() time.Time { return now },
		Close: func(protocol string, deviceID int64) {
			closedMu.Lock()
			defer closedMu.Unlock()
			closedOn = append(closedOn, closedConn{protocol: protocol, deviceID: deviceID})
		},
	})
	runManager(t, manager)

	manager.Connect("gt06", 7, "")

	// A frame keeps the session alive.
	now = now.Add(90 * time.Second)
	manager.Touch(7)
	manager.expire()
	if !manager.Online(7) {
		t.Fatal("a device that just reported was expired")
	}

	// Silence for longer than the timeout expires it.
	now = now.Add(121 * time.Second)
	manager.expire()
	if manager.Online(7) {
		t.Error("a silent device is still online")
	}

	closedMu.Lock()
	count := len(closedOn)
	var first closedConn
	if count > 0 {
		first = closedOn[0]
	}
	closedMu.Unlock()

	if count != 1 {
		t.Fatalf("closed %d connections, want 1", count)
	}
	if first.deviceID != 7 || first.protocol != "gt06" {
		t.Errorf("closed %+v, want device 7 on gt06", first)
	}

	waitFor(t, "the offline event", func() bool {
		for _, eventType := range events.types() {
			if eventType == model.EventDeviceOffline {
				return true
			}
		}
		return false
	})
}

func TestSweepLoopExpiresWithoutExternalCalls(t *testing.T) {
	statuses := &statusRecorder{}

	manager := New(Config{
		Store:   statuses,
		Timeout: 10 * time.Millisecond,
		Sweep:   time.Millisecond,
	})
	runManager(t, manager)

	manager.Connect("h02", 3, "")
	waitFor(t, "the timeout sweep", func() bool { return !manager.Online(3) })
}

func TestShutdownWritesWhatIsStillQueued(t *testing.T) {
	statuses := &statusRecorder{}

	manager := New(Config{Store: statuses})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Run(ctx)
	}()

	manager.Connect("gt06", 11, "")
	cancel()
	<-done

	if got := len(statuses.recorded()); got != 1 {
		t.Errorf("wrote %d statuses on shutdown, want 1", got)
	}
}

func TestFullQueueDropsInsteadOfBlocking(t *testing.T) {
	manager := New(Config{Store: &statusRecorder{}})

	// No Run: nothing drains the queue, so it has to fill up and drop.
	for index := 0; index < transitionQueue+10; index++ {
		manager.Connect("gt06", int64(index+1), "")
	}

	if manager.Dropped() != 10 {
		t.Errorf("dropped %d transitions, want 10", manager.Dropped())
	}
	if manager.OnlineCount() != transitionQueue+10 {
		t.Errorf("online count %d, want every connected device", manager.OnlineCount())
	}
}

func TestFailedWritesAreReportedNotFatal(t *testing.T) {
	// The logger runs on the writer goroutine, so the counter is atomic.
	var logs atomic.Int64
	statuses := &statusRecorder{fail: errors.New("database down")}
	events := &eventRecorder{fail: errors.New("database down")}

	manager := New(Config{
		Store:  statuses,
		Events: events,
		Logger: func(string, ...any) { logs.Add(1) },
	})
	runManager(t, manager)

	manager.Connect("gt06", 1, "")
	waitFor(t, "the failure log", func() bool { return logs.Load() > 0 })
}

func TestSessionsReturnsCopies(t *testing.T) {
	manager := New(Config{})
	manager.Connect("gt06", 1, "10.0.0.1:1")

	sessions := manager.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("%d sessions, want 1", len(sessions))
	}
	sessions[0].Status = StatusOffline
	if !manager.Online(1) {
		t.Error("mutating the returned slice changed the manager")
	}
}

// TestStatusHookSeesEveryTransition pins the socket feed: online and offline are
// both reported, so a connected client can refresh the device row.
func TestStatusHookSeesEveryTransition(t *testing.T) {
	var mu sync.Mutex
	var seen []string

	manager := New(Config{
		Store:  &statusRecorder{},
		Events: &eventRecorder{},
		OnStatus: func(_ context.Context, deviceID int64, status string) {
			if deviceID != 7 {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, status)
		},
	})
	runManager(t, manager)

	manager.Connect("gt06", 7, "")
	manager.Disconnect(7)

	waitFor(t, "both transitions", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 2
	})

	mu.Lock()
	defer mu.Unlock()
	if seen[0] != StatusOnline || seen[1] != StatusOffline {
		t.Errorf("hook saw %v, want online then offline", seen)
	}
}
