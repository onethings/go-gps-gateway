package stats

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
)

type fakeStore struct {
	devices int
	users   int
	err     error

	mu   sync.Mutex
	rows []*model.Statistics
}

func (f *fakeStore) InsertStatistics(_ context.Context, row *model.Statistics) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, row)
	return nil
}

// row returns one captured row, safe against the writer goroutine.
func (f *fakeStore) row(index int) *model.Statistics {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index >= len(f.rows) {
		return nil
	}
	return f.rows[index]
}

// count reports how many rows were written so far.
func (f *fakeStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func (f *fakeStore) OnlineCounts(context.Context) (int, int, error) {
	return f.devices, f.users, f.err
}

func TestCaptureWritesTheIncrementSinceTheLastRow(t *testing.T) {
	meters := metrics.New()
	store := &fakeStore{devices: 2, users: 1}
	clock := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	writer := &Writer{Store: store, Metrics: meters, Now: func() time.Time { return clock }}

	// The first capture counts everything since the process started, which is
	// what the counters hold after a restart with traffic.
	meters.Frames.With("gt06").Add(3)
	meters.Frames.With("h02").Add(2)
	meters.Positions.With("gt06").Add(3)
	meters.Notifications.With("mail").Add(1)
	meters.Requests.Add(10)
	meters.GeocoderRequests.Add(4)
	meters.GeolocationRequests.Add(2)

	if err := writer.Capture(context.Background()); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if store.count() != 1 {
		t.Fatalf("rows = %d", store.count())
	}
	row := store.row(0)
	if row.ActiveDevices != 2 || row.ActiveUsers != 1 {
		t.Errorf("active = %d/%d", row.ActiveDevices, row.ActiveUsers)
	}
	if row.MessagesReceived != 5 || row.MessagesStored != 3 {
		t.Errorf("messages = %d/%d", row.MessagesReceived, row.MessagesStored)
	}
	if row.Requests != 10 || row.MailSent != 1 || row.SmsSent != 0 {
		t.Errorf("requests/mail/sms = %d/%d/%d", row.Requests, row.MailSent, row.SmsSent)
	}
	if row.GeocoderRequests != 4 || row.GeolocationRequests != 2 {
		t.Errorf("geo = %d/%d", row.GeocoderRequests, row.GeolocationRequests)
	}
	if row.Protocols["gt06"] != 3 || row.Protocols["h02"] != 2 {
		t.Errorf("protocols = %v", row.Protocols)
	}
	if !row.CaptureTime.Equal(clock) {
		t.Errorf("capture time = %v", row.CaptureTime)
	}

	// The next row only carries what happened in between.
	meters.Frames.With("gt06").Add(1)
	meters.Requests.Add(2)
	if err := writer.Capture(context.Background()); err != nil {
		t.Fatalf("capture: %v", err)
	}
	second := store.row(1)
	if second.MessagesReceived != 1 || second.Requests != 2 {
		t.Errorf("second row = %d/%d", second.MessagesReceived, second.Requests)
	}
	if second.Protocols["h02"] != 0 {
		t.Errorf("h02 increment = %d", second.Protocols["h02"])
	}
}

func TestCaptureSurvivesACounterReset(t *testing.T) {
	meters := metrics.New()
	store := &fakeStore{}
	writer := &Writer{Store: store, Metrics: meters}

	meters.Requests.Add(5)
	if err := writer.Capture(context.Background()); err != nil {
		t.Fatalf("capture: %v", err)
	}

	// A restart resets the counters, which must never produce a negative row.
	restarted := metrics.New()
	writer.Metrics = restarted
	restarted.Requests.Add(1)
	if err := writer.Capture(context.Background()); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if second := store.row(1); second.Requests != 1 {
		t.Fatalf("requests = %d", second.Requests)
	}
}

func TestCaptureReportsAStoreFailure(t *testing.T) {
	writer := &Writer{Store: &fakeStore{err: errors.New("down")}, Metrics: metrics.New()}
	if err := writer.Capture(context.Background()); err == nil {
		t.Fatal("the store failure was swallowed")
	}
}

func TestRunWritesRowsUntilCancelled(t *testing.T) {
	store := &fakeStore{}
	writer := &Writer{Store: store, Metrics: metrics.New(), Interval: 5 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		writer.Run(ctx)
	}()

	deadline := time.Now().Add(time.Second)
	for store.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
	if store.count() == 0 {
		t.Fatal("no row was written")
	}
}
