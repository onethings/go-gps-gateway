package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

func samplePosition() *model.Position {
	position := model.NewPosition("gt06")
	position.DeviceID = 7
	position.Time = time.Date(2026, 10, 31, 13, 2, 10, 0, time.UTC)
	position.Valid = true
	position.Latitude = 54.94535
	position.Longitude = 24.01762
	position.Altitude = 100
	position.Speed = 27
	position.Course = 180
	position.Accuracy = 5
	position.Attributes["sat"] = 9
	position.Attributes["ignition"] = true
	return position
}

func TestPositionArgs(t *testing.T) {
	args, err := positionArgs(samplePosition())
	if err != nil {
		t.Fatalf("positionArgs: %v", err)
	}
	if len(args) != positionColumns {
		t.Fatalf("args = %d, want %d", len(args), positionColumns)
	}
	if args[0] != int64(7) || args[1] != "gt06" || args[3] != true {
		t.Errorf("unexpected leading args: %#v", args[:4])
	}
	if _, ok := args[2].(time.Time); !ok {
		t.Errorf("device time is %T, want time.Time", args[2])
	}

	var attributes map[string]any
	if err := json.Unmarshal([]byte(args[10].(string)), &attributes); err != nil {
		t.Fatalf("attributes are not valid JSON: %v", err)
	}
	if attributes["sat"] != float64(9) || attributes["ignition"] != true {
		t.Errorf("attributes = %#v", attributes)
	}
}

func TestPositionArgsRequiresDevice(t *testing.T) {
	position := samplePosition()
	position.DeviceID = 0
	if _, err := positionArgs(position); err != ErrMissingDevice {
		t.Fatalf("err = %v, want %v", err, ErrMissingDevice)
	}
}

func TestMigrationVersion(t *testing.T) {
	version, err := migrationVersion("0001_init.sql")
	if err != nil || version != 1 {
		t.Fatalf("version = %d, err = %v", version, err)
	}
	if _, err := migrationVersion("init.sql"); err == nil {
		t.Fatal("expected an error for a file without a version prefix")
	}
}

func TestSplitStatements(t *testing.T) {
	statements := splitStatements("CREATE TABLE a (id INT);\n\nCREATE INDEX b ON a (id);\n")
	if len(statements) != 2 {
		t.Fatalf("statements = %d, want 2", len(statements))
	}
	if statements[1] != "CREATE INDEX b ON a (id)" {
		t.Errorf("second statement = %q", statements[1])
	}
}

// TestSplitStatementsSemicolonsInText covers the shapes 0009 hit: a semicolon
// in a leading comment, one in a literal, and a file that ends in a comment.
func TestSplitStatementsSemicolonsInText(t *testing.T) {
	content := "-- lead; comment\nALTER TABLE a ADD COLUMN b INT;\n" +
		"INSERT INTO a VALUES ('x;y');\n/* block; comment */ CREATE INDEX c ON a (b);\n" +
		"-- trailer; only\n"
	statements := splitStatements(content)
	want := []string{
		"-- lead; comment\nALTER TABLE a ADD COLUMN b INT",
		"INSERT INTO a VALUES ('x;y')",
		"/* block; comment */ CREATE INDEX c ON a (b)",
	}
	if len(statements) != len(want) {
		t.Fatalf("statements = %q, want %d", statements, len(want))
	}
	for i := range want {
		if statements[i] != want[i] {
			t.Errorf("statement %d = %q, want %q", i, statements[i], want[i])
		}
	}
}

func TestSplitStatementsCommentOnly(t *testing.T) {
	if statements := splitStatements("-- nothing here;\n"); len(statements) != 0 {
		t.Fatalf("statements = %q, want none", statements)
	}
}

// TestBatchSink uses a recording writer, so batching is verified without a
// database.
func TestBatchSink(t *testing.T) {
	writer := &recordingWriter{}
	sink := NewBatchSink(writer, 3, 20*time.Millisecond)

	for i := 0; i < 7; i++ {
		position := samplePosition()
		position.DeviceID = int64(i + 1)
		if err := sink.Handle(position); err != nil {
			t.Fatalf("handle: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sink.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := sink.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	batches, total := writer.snapshot()
	if total != 7 {
		t.Fatalf("written = %d, want 7", total)
	}
	for i, batch := range batches {
		if len(batch) > 3 {
			t.Errorf("batch %d has %d positions, want <= 3", i, len(batch))
		}
	}
	if sink.Dropped() != 0 {
		t.Errorf("dropped = %d, want 0", sink.Dropped())
	}
}

type recordingWriter struct {
	mu      sync.Mutex
	batches [][]*model.Position
}

func (w *recordingWriter) InsertPositions(_ context.Context, positions []*model.Position) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.batches = append(w.batches, append([]*model.Position(nil), positions...))
	return nil
}

func (w *recordingWriter) snapshot() ([][]*model.Position, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := 0
	for _, batch := range w.batches {
		total += len(batch)
	}
	return w.batches, total
}

// TestStoreIntegration runs only when TRACCAR_POSTGRES_DSN is set, e.g.
//
//	docker run --rm -d -p 5432:5432 -e POSTGRES_PASSWORD=traccar postgres:16
//	TRACCAR_POSTGRES_DSN='postgres://postgres:traccar@localhost:5432/postgres?sslmode=disable' \
//	  go test ./internal/storage/postgres -run TestStoreIntegration -v
func TestStoreIntegration(t *testing.T) {
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

	uniqueID := "TEST" + time.Now().Format("150405.000000")
	deviceID, err := store.Register(uniqueID, "gt06")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	session, err := store.Resolve(uniqueID)
	if err != nil || session == nil || session.DeviceID != deviceID {
		t.Fatalf("resolve = %+v, err = %v", session, err)
	}
	if session.Model != "" {
		t.Errorf("model of a new device = %q, want empty", session.Model)
	}

	if _, err := store.UpdateDevice(ctx, deviceID, model.DeviceUpdate{Model: "VL103"}); err != nil {
		t.Fatalf("update model: %v", err)
	}
	session, err = store.Resolve(uniqueID)
	if err != nil || session == nil || session.Model != "VL103" {
		t.Fatalf("resolve after model update = %+v, err = %v", session, err)
	}
	if resolvedID, err := store.UniqueID(deviceID); err != nil || resolvedID != uniqueID {
		t.Fatalf("unique id = %q, err = %v", resolvedID, err)
	}
	if protocol, found, err := store.DeviceProtocol(ctx, deviceID); err != nil || !found || protocol != "gt06" {
		t.Fatalf("device protocol = %q, found = %v, err = %v", protocol, found, err)
	}
	if _, found, err := store.DeviceProtocol(ctx, deviceID+1<<20); err != nil || found {
		t.Fatalf("unknown device protocol: found = %v, err = %v", found, err)
	}

	position := samplePosition()
	position.DeviceID = deviceID
	if err := store.Handle(position); err != nil {
		t.Fatalf("handle: %v", err)
	}

	var (
		latitude   float64
		attributes []byte
	)
	if err := store.DB().QueryRowContext(ctx,
		`SELECT latitude, attributes FROM positions WHERE device_id = $1 ORDER BY id DESC LIMIT 1`,
		deviceID,
	).Scan(&latitude, &attributes); err != nil {
		t.Fatalf("query position: %v", err)
	}
	if latitude != position.Latitude {
		t.Errorf("latitude = %.5f, want %.5f", latitude, position.Latitude)
	}

	var decoded map[string]any
	if err := json.Unmarshal(attributes, &decoded); err != nil || decoded["sat"] != float64(9) {
		t.Errorf("attributes = %s, err = %v", attributes, err)
	}

	positions, err := store.QueryPositions(ctx, model.PositionFilter{DeviceID: deviceID, Limit: 10})
	if err != nil {
		t.Fatalf("query positions: %v", err)
	}
	if len(positions) == 0 {
		t.Fatal("query positions returned nothing")
	}
	got := positions[0]
	if got.ID == 0 || got.DeviceID != deviceID || got.Protocol != position.Protocol {
		t.Fatalf("position = %+v", got)
	}
	if got.Latitude != position.Latitude || got.Attributes["sat"] != float64(9) {
		t.Fatalf("position = %+v", got)
	}
	if got.ServerTime.IsZero() || got.Time.Format(time.RFC3339) != position.Time.Format(time.RFC3339) {
		t.Fatalf("times = %s / %s", got.Time, got.ServerTime)
	}

	// A window that starts after the row returns nothing.
	future := time.Now().Add(time.Hour)
	if window, err := store.QueryPositions(ctx, model.PositionFilter{
		DeviceID: deviceID,
		From:     future,
	}); err != nil || len(window) != 0 {
		t.Fatalf("future window = %d positions, err = %v", len(window), err)
	}

	device := findDevice(t, store, ctx, deviceID)
	if device.UniqueID != uniqueID || device.Protocol != "gt06" {
		t.Fatalf("device = %+v", device)
	}
	if !device.LastUpdate.Equal(position.Time.UTC()) {
		t.Fatalf("last update = %s, want %s", device.LastUpdate, position.Time)
	}

	// An older position must not rewind last_update.
	older := samplePosition()
	older.DeviceID = deviceID
	older.Time = position.Time.Add(-time.Hour)
	if err := store.Handle(older); err != nil {
		t.Fatalf("handle older position: %v", err)
	}
	if rewound := findDevice(t, store, ctx, deviceID); !rewound.LastUpdate.Equal(position.Time.UTC()) {
		t.Fatalf("last update rewound to %s", rewound.LastUpdate)
	}

	if _, err := store.DB().ExecContext(ctx, `DELETE FROM devices WHERE id = $1`, deviceID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

// findDevice looks a device up through the filtered query so the test also
// covers the protocol filter.
func findDevice(t *testing.T, store *Store, ctx context.Context, deviceID int64) model.Device {
	t.Helper()

	devices, err := store.Devices(ctx, model.DeviceFilter{Protocol: "gt06", Limit: 1000})
	if err != nil {
		t.Fatalf("query devices: %v", err)
	}
	for _, device := range devices {
		if device.ID == deviceID {
			return device
		}
	}
	t.Fatalf("device %d is not in the %d gt06 results", deviceID, len(devices))
	return model.Device{}
}

// TestDeviceStoreIntegration covers the CRUD path against a real database.
func TestDeviceStoreIntegration(t *testing.T) {
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

	uniqueID := "CRUD" + time.Now().Format("150405.000000")
	created, err := store.CreateDevice(ctx, model.DeviceCreate{
		UniqueID: uniqueID,
		Name:     "van",
		Model:    "GT06N",
		Protocol: "gt06",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.DB().ExecContext(ctx, `DELETE FROM devices WHERE id = $1`, created.ID)
	})

	if created.UniqueID != uniqueID || created.Name != "van" || created.Protocol != "gt06" {
		t.Fatalf("created = %+v", created)
	}
	if _, err := store.CreateDevice(ctx, model.DeviceCreate{UniqueID: uniqueID}); !errors.Is(err, model.ErrDuplicateDevice) {
		t.Fatalf("duplicate err = %v, want ErrDuplicateDevice", err)
	}

	updated, err := store.UpdateDevice(ctx, created.ID, model.DeviceUpdate{Name: "van-2"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "van-2" || updated.Model != "" {
		t.Fatalf("updated = %+v", updated)
	}

	if _, err := store.UpdateDevice(ctx, created.ID+1<<20, model.DeviceUpdate{}); !errors.Is(err, model.ErrDeviceNotFound) {
		t.Fatalf("unknown id err = %v, want ErrDeviceNotFound", err)
	}

	fetched, err := store.Device(ctx, created.ID)
	if err != nil || fetched.UniqueID != uniqueID {
		t.Fatalf("device = %+v, err = %v", fetched, err)
	}

	if _, err := store.DB().ExecContext(ctx,
		`UPDATE devices SET attributes = '{"speedLimit": 45, "powerCutVolts": "8"}'::jsonb WHERE id = $1`,
		created.ID); err != nil {
		t.Fatalf("set attributes: %v", err)
	}
	attributes, err := store.DeviceAttributes(ctx)
	if err != nil {
		t.Fatalf("device attributes: %v", err)
	}
	if got := attributes[created.ID]["speedLimit"]; got != 45.0 {
		t.Errorf("speedLimit = %v, want 45", got)
	}
	if got := attributes[created.ID]["powerCutVolts"]; got != "8" {
		t.Errorf("powerCutVolts = %v, want the stored string", got)
	}

	if err := store.DeleteDevice(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.DeleteDevice(ctx, created.ID); !errors.Is(err, model.ErrDeviceNotFound) {
		t.Fatalf("second delete err = %v, want ErrDeviceNotFound", err)
	}
}

// TestUserPasswordIntegration covers the password login storage path. Every
// query in it selects users.password_hash, so it also proves migration 0006
// ran against an existing database.
func TestUserPasswordIntegration(t *testing.T) {
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

	email := "password-" + time.Now().Format("150405.000000") + "@example.test"
	user, err := store.CreateUser(ctx, model.UserCreate{Email: email, Name: "Password Test"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.DB().ExecContext(ctx, `DELETE FROM users WHERE id = $1`, user.ID)
	})

	// A new account has no password, so its hash is empty rather than a value a
	// blank login could satisfy.
	credential, err := store.CredentialByEmail(ctx, email)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if credential.UserID != user.ID || credential.PasswordHash != "" || credential.Disabled {
		t.Fatalf("credential = %+v", credential)
	}

	hash, err := auth.HashPassword("secret")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := store.SetPassword(ctx, user.ID, hash); err != nil {
		t.Fatalf("set password: %v", err)
	}
	credential, err = store.CredentialByEmail(ctx, email)
	if err != nil {
		t.Fatalf("credential after set: %v", err)
	}
	if credential.PasswordHash != hash {
		t.Errorf("stored hash does not match the hash that was set")
	}
	if !auth.VerifyPassword(credential.PasswordHash, "secret") {
		t.Error("stored hash does not verify the password")
	}

	// The disable flag is what turns a correct password into a 403.
	disabled := true
	if _, err := store.UpdateUser(ctx, user.ID, model.UserUpdate{Disabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	credential, err = store.CredentialByEmail(ctx, email)
	if err != nil || !credential.Disabled {
		t.Fatalf("credential after disable = %+v, err = %v", credential, err)
	}

	if _, err := store.CredentialByEmail(ctx, "nobody@example.test"); !errors.Is(err, model.ErrUserNotFound) {
		t.Errorf("unknown email err = %v, want ErrUserNotFound", err)
	}
	if err := store.SetPassword(ctx, user.ID+1<<20, hash); !errors.Is(err, model.ErrUserNotFound) {
		t.Errorf("unknown user err = %v, want ErrUserNotFound", err)
	}
}

// TestPasswordSelfServiceIntegration covers the lockout, reset code and token
// revocation queries. It needs migrations 0008 applied.
func TestPasswordSelfServiceIntegration(t *testing.T) {
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

	email := "selfservice-" + time.Now().Format("150405.000000") + "@example.test"
	user, err := store.CreateUser(ctx, model.UserCreate{Email: email, Name: "Self Service"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.DB().ExecContext(ctx, `DELETE FROM users WHERE id = $1`, user.ID)
	})

	credential, err := store.CredentialByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("credential by id: %v", err)
	}
	if credential.LockedUntil != nil {
		t.Errorf("a new account is locked until %v", credential.LockedUntil)
	}

	// Two wrong passwords lock the account for the requested window.
	if lock, err := store.RecordLoginFailure(ctx, user.ID, 2, 10*time.Minute); err != nil || lock != nil {
		t.Fatalf("first failure = %v, %v; want no lock", lock, err)
	}
	lock, err := store.RecordLoginFailure(ctx, user.ID, 2, 10*time.Minute)
	if err != nil {
		t.Fatalf("second failure: %v", err)
	}
	if lock == nil {
		t.Fatal("second failure did not lock the account")
	}
	if wait := time.Until(*lock); wait <= 0 || wait > 10*time.Minute {
		t.Errorf("lock lasts %v, want at most 10 minutes", wait)
	}
	credential, err = store.CredentialByEmail(ctx, email)
	if err != nil || credential.LockedUntil == nil {
		t.Fatalf("credential after lock = %+v, err = %v", credential, err)
	}
	if err := store.ResetLoginFailures(ctx, user.ID); err != nil {
		t.Fatalf("reset failures: %v", err)
	}
	credential, err = store.CredentialByEmail(ctx, email)
	if err != nil || credential.LockedUntil != nil {
		t.Fatalf("credential after reset = %+v, err = %v", credential, err)
	}

	// Revoking keeps the token the caller used and drops the rest.
	first, _, err := store.IssueTokenForUser(ctx, user.ID, []string{"*"}, nil)
	if err != nil {
		t.Fatalf("issue first token: %v", err)
	}
	if _, _, err := store.IssueTokenForUser(ctx, user.ID, []string{"*"}, nil); err != nil {
		t.Fatalf("issue second token: %v", err)
	}
	if err := store.RevokeTokensExcept(ctx, user.ID, first.ID); err != nil {
		t.Fatalf("revoke others: %v", err)
	}
	tokens, err := store.Tokens(ctx, user.ID)
	if err != nil {
		t.Fatalf("tokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0].ID != first.ID {
		t.Errorf("tokens after keep = %+v, want only %d", tokens, first.ID)
	}
	if err := store.RevokeTokensExcept(ctx, user.ID, 0); err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	if tokens, err := store.Tokens(ctx, user.ID); err != nil || len(tokens) != 0 {
		t.Errorf("tokens after revoke all = %+v, err = %v", tokens, err)
	}

	// A reset code is throttled, single use, and expires.
	if err := store.CreatePasswordReset(ctx, user.ID, "hash-a", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create reset: %v", err)
	}
	if err := store.CreatePasswordReset(ctx, user.ID, "hash-b", time.Now().Add(time.Hour)); !errors.Is(err, model.ErrResetTooSoon) {
		t.Fatalf("second create err = %v, want ErrResetTooSoon", err)
	}
	if err := store.DeletePasswordResets(ctx, user.ID); err != nil {
		t.Fatalf("delete resets: %v", err)
	}
	if err := store.CreatePasswordReset(ctx, user.ID, "hash-b", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create after delete: %v", err)
	}
	resetUser, err := store.ConsumePasswordReset(ctx, "hash-b")
	if err != nil || resetUser != user.ID {
		t.Fatalf("consume = %d, %v; want %d", resetUser, err, user.ID)
	}
	if _, err := store.ConsumePasswordReset(ctx, "hash-b"); !errors.Is(err, model.ErrResetTokenInvalid) {
		t.Errorf("second consume err = %v, want ErrResetTokenInvalid", err)
	}
	if err := store.DeletePasswordResets(ctx, user.ID); err != nil {
		t.Fatalf("delete resets before expiry test: %v", err)
	}
	if err := store.CreatePasswordReset(ctx, user.ID, "hash-c", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("create expired reset: %v", err)
	}
	if _, err := store.ConsumePasswordReset(ctx, "hash-c"); !errors.Is(err, model.ErrResetTokenInvalid) {
		t.Errorf("expired consume err = %v, want ErrResetTokenInvalid", err)
	}
	if err := store.DeletePasswordResets(ctx, user.ID); err != nil {
		t.Fatalf("delete resets: %v", err)
	}
	if err := store.CreatePasswordReset(ctx, user.ID, "hash-d", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create droppable reset: %v", err)
	}
	if err := store.DeletePasswordReset(ctx, "hash-d"); err != nil {
		t.Fatalf("delete one reset: %v", err)
	}
	if _, err := store.ConsumePasswordReset(ctx, "hash-d"); !errors.Is(err, model.ErrResetTokenInvalid) {
		t.Errorf("consume of a deleted code err = %v, want ErrResetTokenInvalid", err)
	}
}
