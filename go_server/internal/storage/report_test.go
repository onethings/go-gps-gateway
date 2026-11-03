package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// TestEventLinksPosition is the acceptance test of the events table: the event
// an overspeed raised must carry the id of the position that caused it, which
// the alerts table never managed because the alert was written first.
func TestEventLinksPosition(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	first := &model.Position{DeviceID: 7, Time: at, Speed: 40}
	second := &model.Position{DeviceID: 7, Time: at.Add(time.Minute), Speed: 41}

	firstEvent := model.NewEvent(model.EventDeviceOverspeed, 7, first.Time)
	secondEvent := model.NewEvent(model.EventDeviceOverspeed, 7, second.Time)
	eventsByPosition := map[*model.Position][]*model.Event{
		first:  {firstEvent},
		second: {secondEvent},
	}

	ids := positionIDs{
		positionKeyOf(first):  {101},
		positionKeyOf(second): {102},
	}

	linkReportIDs([]*model.Position{first, second}, eventsByPosition, ids)

	if first.ID != 101 || second.ID != 102 {
		t.Fatalf("position ids = %d, %d; want 101, 102", first.ID, second.ID)
	}
	if firstEvent.PositionID != 101 {
		t.Errorf("first event links position %d, want 101", firstEvent.PositionID)
	}
	if secondEvent.PositionID != 102 {
		t.Errorf("second event links position %d, want 102", secondEvent.PositionID)
	}

	// The linked id is what the insert writes into events.position_id.
	args, err := eventArgs(firstEvent)
	if err != nil {
		t.Fatalf("eventArgs: %v", err)
	}
	if args[1] != int64(101) {
		t.Errorf("position_id argument = %#v, want 101", args[1])
	}
}

// TestEventLinksPositionWithRepeatedKeys covers the same device reporting the
// same device time twice: each position takes a different id instead of both
// pointing at one row.
func TestEventLinksPositionWithRepeatedKeys(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	first := &model.Position{DeviceID: 7, Time: at}
	second := &model.Position{DeviceID: 7, Time: at}

	ids := positionIDs{positionKeyOf(first): {1, 2}}
	linkReportIDs([]*model.Position{first, second}, nil, ids)

	if first.ID == second.ID {
		t.Fatalf("both positions took id %d", first.ID)
	}
	if first.ID+second.ID != 3 {
		t.Errorf("ids = %d, %d; want the two returned ids", first.ID, second.ID)
	}
}

// TestInsertReportsIntegration runs only when TRACCAR_POSTGRES_DSN is set and
// proves the transaction end to end.
func TestInsertReportsIntegration(t *testing.T) {
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

	uniqueID := "report-test-" + time.Now().Format("150405.000000")
	device, err := store.CreateDevice(ctx, model.DeviceCreate{UniqueID: uniqueID})
	if err != nil {
		t.Fatalf("create device: %v", err)
	}
	t.Cleanup(func() {
		if err := store.DeleteDevice(context.Background(), device.ID); err != nil {
			t.Logf("clean up device: %v", err)
		}
	})

	at := time.Now().UTC().Truncate(time.Millisecond)
	position := &model.Position{
		DeviceID:  device.ID,
		Protocol:  "gt06",
		Time:      at,
		Valid:     true,
		Speed:     40,
		Latitude:  -33.87,
		Longitude: 151.21,
	}
	event := model.NewEvent(model.EventDeviceOverspeed, device.ID, at)

	if err := store.InsertReports(ctx, []*model.PositionReport{{Position: position, Events: []*model.Event{event}}}); err != nil {
		t.Fatalf("insert report: %v", err)
	}

	if position.ID == 0 {
		t.Fatal("the position got no id back")
	}
	if event.ID == 0 {
		t.Fatal("the event got no id back")
	}
	if event.PositionID != position.ID {
		t.Fatalf("event links position %d, want %d", event.PositionID, position.ID)
	}

	stored, err := store.QueryEvents(ctx, model.EventFilter{DeviceID: device.ID, Limit: 10})
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	if len(stored) != 1 || stored[0].PositionID != position.ID {
		t.Fatalf("stored events = %#v", stored)
	}
}
