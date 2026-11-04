package storage

import (
	"context"
	"path/filepath"
	"testing"

	"traccar-server_go/go_server/internal/model"
)

// TestDeviceAndUserLimitsAreStored pins that the limits the API used to report
// as zero are persisted and read back.
func TestDeviceAndUserLimitsAreStored(t *testing.T) {
	ctx := context.Background()
	store, err := New(ctx, "sqlite://"+filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	unlimited, err := store.CreateUser(ctx, model.UserCreate{Email: "a@example.test"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if unlimited.DeviceLimit != model.NoLimit || unlimited.UserLimit != model.NoLimit {
		t.Errorf("limits = %d/%d, want %d/%d",
			unlimited.DeviceLimit, unlimited.UserLimit, model.NoLimit, model.NoLimit)
	}

	deviceLimit, userLimit := 5, 2
	capped, err := store.CreateUser(ctx, model.UserCreate{
		Email:       "b@example.test",
		DeviceLimit: &deviceLimit,
		UserLimit:   &userLimit,
	})
	if err != nil {
		t.Fatalf("create capped user: %v", err)
	}
	if capped.DeviceLimit != 5 || capped.UserLimit != 2 {
		t.Errorf("limits = %d/%d, want 5/2", capped.DeviceLimit, capped.UserLimit)
	}

	raised := 9
	updated, err := store.UpdateUser(ctx, capped.ID, model.UserUpdate{DeviceLimit: &raised})
	if err != nil {
		t.Fatalf("update user: %v", err)
	}
	if updated.DeviceLimit != 9 || updated.UserLimit != 2 {
		t.Errorf("limits = %d/%d, want 9/2", updated.DeviceLimit, updated.UserLimit)
	}
}
