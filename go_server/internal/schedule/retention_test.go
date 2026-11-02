package schedule

import (
	"context"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

type inactiveFake struct {
	devices   []model.Device
	positions []model.Position
	inserted  []*model.Event
}

func (f *inactiveFake) Devices(context.Context, model.DeviceFilter) ([]model.Device, error) {
	return f.devices, nil
}

func (f *inactiveFake) LatestPositions(context.Context, []int64) ([]model.Position, error) {
	return f.positions, nil
}

func (f *inactiveFake) InsertEvents(_ context.Context, events []*model.Event) error {
	f.inserted = append(f.inserted, events...)
	return nil
}

func TestDeviceInactiveRaisesOneEventPerSilence(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	store := &inactiveFake{
		devices: []model.Device{{ID: 1, LastUpdate: now.Add(-3 * time.Hour)}},
	}
	task := &DeviceInactive{Store: store, Timeout: time.Hour, Now: func() time.Time { return now }}

	for pass := 0; pass < 2; pass++ {
		if err := task.Run(context.Background()); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}
	if len(store.inserted) != 1 {
		t.Fatalf("inserted %d events, want 1", len(store.inserted))
	}
	if store.inserted[0].Type != model.EventDeviceInactive {
		t.Fatalf("type = %q", store.inserted[0].Type)
	}
}

func TestDeviceInactiveSkipsDevicesThatReport(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	store := &inactiveFake{
		devices:   []model.Device{{ID: 1, LastUpdate: now.Add(-3 * time.Hour)}},
		positions: []model.Position{{DeviceID: 1, Time: now.Add(-5 * time.Minute)}},
	}
	task := &DeviceInactive{Store: store, Timeout: time.Hour, Now: func() time.Time { return now }}
	if err := task.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.inserted) != 0 {
		t.Fatalf("inserted %d events, want 0", len(store.inserted))
	}
}

func TestDeviceInactiveIgnoresNeverSeenDevice(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	store := &inactiveFake{devices: []model.Device{{ID: 1}}}
	task := &DeviceInactive{Store: store, Timeout: time.Hour, Now: func() time.Time { return now }}
	if err := task.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.inserted) != 0 {
		t.Fatalf("inserted %d events, want 0", len(store.inserted))
	}
}

func TestRetentionDeletesBeforeTheCutoff(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	var cutoff time.Time
	task := &Retention{
		Label: "events",
		Keep:  24 * time.Hour,
		Now:   func() time.Time { return now },
		Delete: func(_ context.Context, before time.Time) (int64, error) {
			cutoff = before
			return 7, nil
		},
	}
	if err := task.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !cutoff.Equal(now.Add(-24 * time.Hour)) {
		t.Fatalf("cutoff = %v", cutoff)
	}
	if task.Name() != "events" {
		t.Fatalf("name = %q", task.Name())
	}
}

func TestRetentionDoesNothingWithoutKeep(t *testing.T) {
	called := false
	task := &Retention{Label: "positions", Delete: func(context.Context, time.Time) (int64, error) {
		called = true
		return 0, nil
	}}
	if err := task.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("a retention of zero must not delete anything")
	}
}
