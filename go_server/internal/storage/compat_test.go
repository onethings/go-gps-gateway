package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// referenceSchema is the SQLite equivalent of a dump from the Java server: the
// two tables with the columns and constraints the reference changelogs create,
// plus rows written the way the Java server writes them.
const referenceSchema = `
CREATE TABLE tc_devices (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       VARCHAR(128) NOT NULL,
    uniqueid   VARCHAR(128) NOT NULL UNIQUE,
    lastupdate TIMESTAMP,
    positionid INT,
    groupid    INT,
    attributes VARCHAR(4000),
    phone      VARCHAR(128),
    model      VARCHAR(128),
    contact    VARCHAR(512),
    category   VARCHAR(128),
    disabled   BOOLEAN DEFAULT false,
    status     CHAR(8),
    expirationtime TIMESTAMP,
    calendarid INT
);

CREATE TABLE tc_positions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    protocol    VARCHAR(128),
    deviceid    INT NOT NULL REFERENCES tc_devices (id) ON DELETE CASCADE,
    servertime  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    devicetime  TIMESTAMP NOT NULL,
    fixtime     TIMESTAMP NOT NULL,
    valid       BOOLEAN NOT NULL,
    latitude    DOUBLE NOT NULL,
    longitude   DOUBLE NOT NULL,
    altitude    FLOAT NOT NULL,
    speed       FLOAT NOT NULL,
    course      FLOAT NOT NULL,
    address     VARCHAR(512),
    attributes  VARCHAR(4000),
    accuracy    DOUBLE NOT NULL DEFAULT 0,
    network     VARCHAR(4000),
    geofenceids VARCHAR(128)
);

INSERT INTO tc_devices (id, name, uniqueid, lastupdate, attributes, phone, model, disabled)
VALUES (1, 'Truck', '862170041234567', '2026-10-31 12:00:00', '{"fuel":"diesel"}', '+37060000001', 'GT06N', false);
INSERT INTO tc_devices (id, name, uniqueid, lastupdate, attributes, model, disabled)
VALUES (2, 'Spare', '862170041234568', '2026-10-30 09:00:00', NULL, 'GT06N', true);

INSERT INTO tc_positions (id, protocol, deviceid, servertime, devicetime, fixtime,
                          valid, latitude, longitude, altitude, speed, course,
                          attributes, accuracy, geofenceids)
VALUES (10, 'gt06', 1, '2026-10-31 12:00:05', '2026-10-31 12:00:00', '2026-10-31 12:00:00',
        true, 54.94535, 24.01762, 100, 27, 180, '{"sat":9}', 5, '3,4');
INSERT INTO tc_positions (id, protocol, deviceid, servertime, devicetime, fixtime,
                          valid, latitude, longitude, altitude, speed, course,
                          attributes, accuracy, geofenceids)
VALUES (11, 'gt06', 1, '2026-10-31 12:00:35', '2026-10-31 12:00:30', '2026-10-31 12:00:30',
        true, 54.94600, 24.01800, 101, 28, 181, NULL, 6, NULL);
`

func newReferenceDatabase(t *testing.T) string {
	t.Helper()

	dsn := "sqlite://" + filepath.Join(t.TempDir(), "reference.db")
	raw, err := sql.Open("sqlite", sqliteDSN(dsn))
	if err != nil {
		t.Fatalf("open reference database: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	for _, statement := range splitStatements(referenceSchema) {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("seed reference database: %v", err)
		}
	}
	return dsn
}

func TestAdoptReferenceSchema(t *testing.T) {
	ctx := context.Background()
	dsn := newReferenceDatabase(t)

	store, err := New(ctx, dsn, WithReferenceSchema())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	devices, err := store.Devices(ctx, model.DeviceFilter{})
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %d, want 2", len(devices))
	}
	byUniqueID := map[string]model.Device{}
	for _, device := range devices {
		byUniqueID[device.UniqueID] = device
	}

	truck, ok := byUniqueID["862170041234567"]
	if !ok {
		t.Fatalf("device 862170041234567 missing from %v", devices)
	}
	if truck.Name != "Truck" || truck.Model != "GT06N" || truck.Phone != "+37060000001" {
		t.Errorf("device = %+v, want the reference columns carried over", truck)
	}
	if truck.Disabled {
		t.Error("device is disabled, want enabled")
	}
	if truck.Attributes["fuel"] != "diesel" {
		t.Errorf("attributes = %v, want the reference JSON", truck.Attributes)
	}
	if spare := byUniqueID["862170041234568"]; !spare.Disabled {
		t.Error("spare device is enabled, want disabled")
	}

	positions, err := store.QueryPositions(ctx, model.PositionFilter{DeviceID: 1, Ascending: true})
	if err != nil {
		t.Fatalf("query positions: %v", err)
	}
	if len(positions) != 2 {
		t.Fatalf("positions = %d, want 2", len(positions))
	}
	first := positions[0]
	if first.ServerTime.IsZero() || first.Time.Year() != 2026 || first.Time.Month() != time.October {
		t.Errorf("times = %v / %v, want the reference columns", first.Time, first.ServerTime)
	}
	if len(first.GeofenceIDs) != 2 || first.GeofenceIDs[0] != 3 || first.GeofenceIDs[1] != 4 {
		t.Errorf("geofence ids = %v, want [3 4] from the comma separated column", first.GeofenceIDs)
	}
	if first.Attributes["sat"] != float64(9) {
		t.Errorf("attributes = %v, want sat 9", first.Attributes)
	}
	if positions[1].Accuracy != 6 {
		t.Errorf("accuracy = %v, want 6", positions[1].Accuracy)
	}

	written := model.NewPosition("gt06")
	written.DeviceID = 1
	written.Time = time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	written.Valid = true
	written.Latitude = 54.95
	written.Longitude = 24.02
	written.GeofenceIDs = []int64{7}
	if err := store.InsertPositions(ctx, []*model.Position{written}); err != nil {
		t.Fatalf("insert position: %v", err)
	}

	positions, err = store.QueryPositions(ctx, model.PositionFilter{DeviceID: 1})
	if err != nil {
		t.Fatalf("query positions after insert: %v", err)
	}
	if len(positions) != 3 {
		t.Fatalf("positions = %d, want 3", len(positions))
	}
	if positions[0].Time != written.Time || len(positions[0].GeofenceIDs) != 1 {
		t.Errorf("newest position = %+v, want the one just written", positions[0])
	}

	lastUpdate, err := store.Device(ctx, 1)
	if err != nil {
		t.Fatalf("device after insert: %v", err)
	}
	if lastUpdate.LastUpdate.IsZero() {
		t.Error("last_update was not advanced on the adopted table")
	}

	session, err := store.Resolve("862170041234567")
	if err != nil || session == nil {
		t.Fatalf("resolve = %v, %v, want a session", session, err)
	}
	if session.DeviceID != 1 {
		t.Errorf("resolved device id = %d, want 1", session.DeviceID)
	}
}

func TestAdoptReferenceSchemaIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dsn := newReferenceDatabase(t)

	first, err := New(ctx, dsn, WithReferenceSchema())
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	_ = first.Close()

	// A later start has no tc_devices table left, so the adoption is a no-op
	// whether or not the operator keeps the option.
	second, err := New(ctx, dsn, WithReferenceSchema())
	if err != nil {
		t.Fatalf("second start with the option: %v", err)
	}
	_ = second.Close()

	third, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("start without the option: %v", err)
	}
	_ = third.Close()
}

func TestAdoptReferenceSchemaRefusesAmbiguousDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := newReferenceDatabase(t)

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	_ = store.Close()

	if _, err := New(ctx, dsn, WithReferenceSchema()); err == nil {
		t.Fatal("start with both schemas succeeded, want a refusal")
	}
}

func TestAdoptReferenceSchemaOffByDefault(t *testing.T) {
	ctx := context.Background()
	dsn := newReferenceDatabase(t)

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Without the option the Go schema is created next to the reference one and
	// the reference rows are not adopted.
	devices, err := store.Devices(ctx, model.DeviceFilter{})
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	if len(devices) != 0 {
		t.Errorf("devices = %d, want the empty Go table", len(devices))
	}
}

// TestCompatSchemaCarriesEveryColumnTheQueriesSelect pins the contract between
// the attach steps and the column lists the store reads. A column added to
// deviceColumns or positionSelectColumns without a matching column in the
// adopted table only fails at runtime, on a real Java database.
func TestCompatSchemaCarriesEveryColumnTheQueriesSelect(t *testing.T) {
	sqlite, err := compatFS.ReadFile("compat/sqlite.sql")
	if err != nil {
		t.Fatalf("read compat/sqlite.sql: %v", err)
	}

	checks := []struct {
		table   string
		columns string
	}{
		{"devices", deviceColumns},
		{"positions", positionSelectColumns},
	}

	for _, check := range checks {
		body := createTableBody(t, string(sqlite), check.table)
		for _, column := range columnNames(check.columns) {
			if !regexp.MustCompile(`\b` + regexp.QuoteMeta(column) + `\b`).MatchString(body) {
				t.Errorf("%s: adopted table has no %q column", check.table, column)
			}
		}
	}
}

// createTableBody returns the CREATE TABLE statement of a table in a SQL file.
func createTableBody(t *testing.T, s, table string) string {
	t.Helper()

	start := strings.Index(s, "CREATE TABLE "+table+" (")
	if start < 0 {
		t.Fatalf("no CREATE TABLE %s in the attach steps", table)
	}
	end := strings.Index(s[start:], ");")
	if end < 0 {
		t.Fatalf("unterminated CREATE TABLE %s", table)
	}
	return s[start : start+end]
}

// columnNames splits a SQL column list into its names.
func columnNames(list string) []string {
	parts := strings.Split(list, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		if name := strings.TrimSpace(part); name != "" {
			names = append(names, name)
		}
	}
	return names
}
