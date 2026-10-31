package postgres

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

func TestAlertArgs(t *testing.T) {
	alert := &model.Alert{
		DeviceID:   7,
		PositionID: 11,
		Type:       model.AlertOverspeed,
		Time:       time.Date(2026, 10, 31, 13, 2, 10, 0, time.UTC),
		Attributes: map[string]any{"speed": 31.5},
	}

	args, err := alertArgs(alert)
	if err != nil {
		t.Fatalf("alertArgs: %v", err)
	}
	if len(args) != alertColumns {
		t.Fatalf("args = %d, want %d", len(args), alertColumns)
	}
	if args[0] != int64(7) || args[1] != int64(11) || args[2] != model.AlertOverspeed {
		t.Errorf("unexpected leading args: %#v", args[:3])
	}
	if _, ok := args[3].(time.Time); !ok {
		t.Errorf("alert time is %T, want time.Time", args[3])
	}
	if args[4] != nil {
		t.Errorf("geofence id = %#v, want NULL", args[4])
	}

	var attributes map[string]any
	if err := json.Unmarshal([]byte(args[5].(string)), &attributes); err != nil {
		t.Fatalf("attributes are not valid JSON: %v", err)
	}
	if attributes["speed"] != 31.5 {
		t.Errorf("attributes = %#v", attributes)
	}
}

func TestAlertArgsDefaults(t *testing.T) {
	args, err := alertArgs(&model.Alert{DeviceID: 1, Type: model.AlertPowerCut})
	if err != nil {
		t.Fatalf("alertArgs: %v", err)
	}
	if args[3].(time.Time).IsZero() {
		t.Error("a missing time should be filled with the current time")
	}
	if args[5].(string) != "{}" {
		t.Errorf("nil attributes encoded as %s, want {}", args[5])
	}
}

func TestAlertArgsRejectsIncompleteAlerts(t *testing.T) {
	if _, err := alertArgs(&model.Alert{Type: model.AlertOverspeed}); err != ErrMissingDevice {
		t.Errorf("err = %v, want %v", err, ErrMissingDevice)
	}
	if _, err := alertArgs(&model.Alert{DeviceID: 1}); err != ErrMissingAlertType {
		t.Errorf("err = %v, want %v", err, ErrMissingAlertType)
	}
}

func TestAlertQuery(t *testing.T) {
	from := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)

	query, args := alertQuery(model.AlertFilter{
		DeviceID: 7,
		Types:    []string{model.AlertOverspeed, "", model.AlertGeofenceEnter},
		From:     from,
		To:       to,
		Limit:    50,
		Offset:   10,
	})

	for _, fragment := range []string{
		"WHERE device_id = $1 AND type IN ($2, $3) AND alert_time >= $4 AND alert_time <= $5",
		"ORDER BY alert_time DESC, id DESC LIMIT $6 OFFSET $7",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("query is missing %q:\n%s", fragment, query)
		}
	}
	if len(args) != 7 {
		t.Fatalf("args = %d, want 7: %#v", len(args), args)
	}
	if args[0] != int64(7) || args[1] != model.AlertOverspeed || args[2] != model.AlertGeofenceEnter {
		t.Errorf("args = %#v", args[:3])
	}
	if !args[3].(time.Time).Equal(from) || !args[4].(time.Time).Equal(to) {
		t.Errorf("time args = %#v", args[3:5])
	}
	if args[5] != 50 || args[6] != 10 {
		t.Errorf("limit/offset = %#v", args[5:])
	}
}

func TestAlertQueryDefaults(t *testing.T) {
	query, args := alertQuery(model.AlertFilter{})
	if strings.Contains(query, "WHERE") || strings.Contains(query, "OFFSET") {
		t.Errorf("empty filter produced conditions:\n%s", query)
	}
	if !strings.HasSuffix(query, "LIMIT $1") {
		t.Errorf("query = %s", query)
	}
	if len(args) != 1 || args[0] != defaultAlertLimit {
		t.Errorf("args = %#v", args)
	}

	_, args = alertQuery(model.AlertFilter{Limit: 5000})
	if args[0] != maxAlertLimit {
		t.Errorf("limit = %v, want %d", args[0], maxAlertLimit)
	}
}

func TestAlertsInsert(t *testing.T) {
	timestamp := time.Date(2026, 10, 31, 13, 2, 10, 0, time.UTC)
	query, args, err := alertsInsert([]*model.Alert{
		{DeviceID: 7, Type: model.AlertOverspeed, Time: timestamp},
		{DeviceID: 8, PositionID: 3, Type: model.AlertPowerCut, Time: timestamp, GeofenceID: 5},
		{DeviceID: 9}, // skipped: no type
	})
	if err != nil {
		t.Fatalf("alertsInsert: %v", err)
	}

	want := "VALUES ($1, $2, $3, $4, $5, $6::jsonb), ($7, $8, $9, $10, $11, $12::jsonb)"
	if !strings.Contains(query, want) {
		t.Errorf("query does not match %q:\n%s", want, query)
	}
	if len(args) != 2*alertColumns {
		t.Fatalf("args = %d, want %d", len(args), 2*alertColumns)
	}
	if args[0] != int64(7) || args[1] != nil {
		t.Errorf("first row = %#v", args[:2])
	}
	if args[6] != int64(8) || args[7] != int64(3) || args[10] != int64(5) {
		t.Errorf("second row = %#v", args[6:11])
	}
}

func TestAlertsInsertWithoutValidRows(t *testing.T) {
	query, args, err := alertsInsert([]*model.Alert{{}, nil})
	if err != nil {
		t.Fatalf("alertsInsert: %v", err)
	}
	if query != "" || args != nil {
		t.Errorf("query = %q, args = %#v", query, args)
	}

	if query, _, err := alertsInsert(nil); err != nil || query != "" {
		t.Errorf("query = %q, err = %v", query, err)
	}
}

// TestAlertsMigration checks the embedded DDL still splits cleanly: the
// migration runner only understands semicolon separated statements.
func TestAlertsMigration(t *testing.T) {
	content, err := migrationsFS.ReadFile("migrations/0002_alerts.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}

	statements := splitStatements(string(content))
	if len(statements) != 5 {
		t.Fatalf("statements = %d, want 5: %#v", len(statements), statements)
	}
	for _, statement := range statements {
		if !strings.HasPrefix(statement, "CREATE") {
			t.Errorf("unexpected statement: %q", statement)
		}
	}

	joined := strings.Join(statements, "\n")
	for _, fragment := range []string{
		"REFERENCES devices (id) ON DELETE CASCADE",
		"REFERENCES positions (id) ON DELETE SET NULL",
		"alerts (device_id, alert_time DESC)",
	} {
		if !strings.Contains(joined, fragment) {
			t.Errorf("migration is missing %q", fragment)
		}
	}
}

// TestAlertStoreIntegration runs only when TRACCAR_POSTGRES_DSN is set.
func TestAlertStoreIntegration(t *testing.T) {
	dsn := os.Getenv("TRACCAR_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TRACCAR_POSTGRES_DSN is not set")
	}

	ctx := context.Background()
	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	uniqueID := "ALERT" + time.Now().Format("150405.000000")
	deviceID, err := store.Register(uniqueID, "gt06")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.DB().ExecContext(ctx, `DELETE FROM devices WHERE id = $1`, deviceID)
	})

	const area = "POLYGON ((0 0, 0 1, 1 1, 1 0, 0 0))"
	var geofenceID int64
	if err := store.DB().QueryRowContext(ctx,
		`INSERT INTO geofences (name, area, attributes) VALUES ('depot', $1, '{"floor": 0}'::jsonb)
		 RETURNING id`, area,
	).Scan(&geofenceID); err != nil {
		t.Fatalf("insert geofence: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.DB().ExecContext(ctx, `DELETE FROM geofences WHERE id = $1`, geofenceID)
	})

	geofences, err := store.Geofences(ctx)
	if err != nil {
		t.Fatalf("geofences: %v", err)
	}
	if len(geofences) == 0 {
		t.Fatal("no geofences loaded")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.InsertAlerts(ctx, []*model.Alert{
		{DeviceID: deviceID, Type: model.AlertOverspeed, Time: now,
			Attributes: map[string]any{"speed": 31.5}},
		{DeviceID: deviceID, Type: model.AlertGeofenceEnter, Time: now.Add(time.Second),
			GeofenceID: geofenceID, Attributes: map[string]any{"geofence": "depot"}},
		{Type: model.AlertPowerCut},
	}); err != nil {
		t.Fatalf("insert alerts: %v", err)
	}

	entered, err := store.QueryAlerts(ctx, model.AlertFilter{
		DeviceID: deviceID,
		Types:    []string{model.AlertGeofenceEnter},
	})
	if err != nil {
		t.Fatalf("query alerts: %v", err)
	}
	if len(entered) != 1 {
		t.Fatalf("typed alerts = %d, want 1", len(entered))
	}
	if entered[0].GeofenceID != geofenceID || entered[0].Attributes["geofence"] != "depot" {
		t.Errorf("alert = %+v", entered[0])
	}
	if entered[0].PositionID != 0 {
		t.Errorf("position id = %d, want the NULL default", entered[0].PositionID)
	}

	all, err := store.QueryAlerts(ctx, model.AlertFilter{
		DeviceID: deviceID,
		From:     now.Add(-time.Minute),
		To:       now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("query alerts: %v", err)
	}
	// The incomplete third alert is skipped, so only two rows exist.
	if len(all) != 2 {
		t.Fatalf("alerts = %d, want 2", len(all))
	}
	if all[0].Type != model.AlertGeofenceEnter || all[1].Type != model.AlertOverspeed {
		t.Errorf("alerts are not newest first: %+v", all)
	}

	// A malformed area must not hide the healthy geofences.
	var brokenID int64
	if err := store.DB().QueryRowContext(ctx,
		`INSERT INTO geofences (name, area) VALUES ('broken', 'POINT (1 2)') RETURNING id`,
	).Scan(&brokenID); err != nil {
		t.Fatalf("insert broken geofence: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.DB().ExecContext(ctx, `DELETE FROM geofences WHERE id = $1`, brokenID)
	})

	geofences, err = store.Geofences(ctx)
	if err == nil {
		t.Error("expected the unparseable area to be reported")
	}
	found := false
	for _, fence := range geofences {
		if fence.ID == geofenceID {
			found = true
		}
		if fence.ID == brokenID {
			t.Error("the broken geofence was loaded")
		}
	}
	if !found {
		t.Error("the valid geofence is missing")
	}
}
