package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// TestSessionStoreIntegration runs only when TRACCAR_POSTGRES_DSN is set.
func TestSessionStoreIntegration(t *testing.T) {
	dsn := os.Getenv("TRACCAR_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TRACCAR_POSTGRES_DSN is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	uniqueID := "session-test-" + time.Now().Format("150405.000000")
	device, err := store.CreateDevice(ctx, model.DeviceCreate{UniqueID: uniqueID})
	if err != nil {
		t.Fatalf("create device: %v", err)
	}
	t.Cleanup(func() {
		if err := store.DeleteDevice(context.Background(), device.ID); err != nil {
			t.Logf("clean up device: %v", err)
		}
	})

	if device.Status != "unknown" {
		t.Errorf("new device status = %q, want unknown", device.Status)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.SetDeviceStatus(ctx, device.ID, "online", now); err != nil {
		t.Fatalf("set status: %v", err)
	}
	reloaded, err := store.Device(ctx, device.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Status != "online" {
		t.Errorf("status = %q, want online", reloaded.Status)
	}

	events := []*model.Event{
		model.NewEvent(model.EventDeviceOnline, device.ID, now),
		model.NewEvent(model.EventDeviceOffline, device.ID, now),
	}
	if err := store.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	for index, event := range events {
		if event.ID == 0 {
			t.Errorf("event %d got no id back", index)
		}
	}
}
