package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// TestDisabledDevicesAreNotWritten pins the write boundary: a position for a
// device an operator switched off never reaches the table, even when the caller
// already holds a session for it. The gateway drops those positions too; this
// covers the transports and protocols that bypass the gateway session.
func TestDisabledDevicesAreNotWritten(t *testing.T) {
	ctx := context.Background()
	store, err := New(ctx, "sqlite://"+filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	enabledID, err := store.Register("861234567890123", "gt06")
	if err != nil {
		t.Fatalf("register enabled: %v", err)
	}
	disabledID, err := store.Register("861234567890124", "gt06")
	if err != nil {
		t.Fatalf("register disabled: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE devices SET disabled = true WHERE id = $1`, disabledID); err != nil {
		t.Fatalf("disable device: %v", err)
	}

	enabledSession, err := store.Resolve("861234567890123")
	if err != nil || enabledSession == nil {
		t.Fatalf("resolve enabled = %v, %v", enabledSession, err)
	}
	if enabledSession.Disabled {
		t.Error("enabled device resolved as disabled")
	}
	disabledSession, err := store.Resolve("861234567890124")
	if err != nil || disabledSession == nil {
		t.Fatalf("resolve disabled = %v, %v", disabledSession, err)
	}
	if !disabledSession.Disabled {
		t.Error("disabled device resolved as enabled")
	}

	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	batch := make([]*model.Position, 0, 2)
	for _, deviceID := range []int64{enabledID, disabledID} {
		position := model.NewPosition("gt06")
		position.DeviceID = deviceID
		position.Time = now
		position.Valid = true
		position.Latitude = 54.9
		position.Longitude = 24.0
		batch = append(batch, position)
	}
	if err := store.InsertPositions(ctx, batch); err != nil {
		t.Fatalf("insert positions: %v", err)
	}

	enabled, err := store.QueryPositions(ctx, model.PositionFilter{DeviceID: enabledID})
	if err != nil {
		t.Fatalf("query enabled positions: %v", err)
	}
	if len(enabled) != 1 {
		t.Errorf("enabled positions = %d, want 1", len(enabled))
	}
	disabled, err := store.QueryPositions(ctx, model.PositionFilter{DeviceID: disabledID})
	if err != nil {
		t.Fatalf("query disabled positions: %v", err)
	}
	if len(disabled) != 0 {
		t.Errorf("disabled positions = %d, want 0", len(disabled))
	}

	// The report path writes the position and its events in one transaction,
	// so it needs the same guard.
	withEvent := model.NewPosition("gt06")
	withEvent.DeviceID = disabledID
	withEvent.Time = now.Add(time.Minute)
	withEvent.Valid = true
	if err := store.InsertReports(ctx, []*model.PositionReport{{
		Position: withEvent,
		Events: []*model.Event{{
			DeviceID: disabledID,
			Type:     "deviceMoving",
			Time:     now.Add(time.Minute),
		}},
	}}); err != nil {
		t.Fatalf("insert report: %v", err)
	}

	disabled, err = store.QueryPositions(ctx, model.PositionFilter{DeviceID: disabledID})
	if err != nil {
		t.Fatalf("query disabled positions after report: %v", err)
	}
	if len(disabled) != 0 {
		t.Errorf("disabled positions after report = %d, want 0", len(disabled))
	}
	events, err := store.QueryEvents(ctx, model.EventFilter{DeviceID: disabledID})
	if err != nil {
		t.Fatalf("query disabled events: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("disabled events = %d, want 0", len(events))
	}
}
