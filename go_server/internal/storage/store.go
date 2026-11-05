// Package storage persists devices and positions. It satisfies the
// gateway.DeviceDirectory and gateway.Sink interfaces structurally, so the
// gateway never imports this package.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnknownDevice is returned when a unique id or device id has no record.
var ErrUnknownDevice = errors.New("storage: unknown device")

// ErrMissingDevice is returned when a position has no device id.
var ErrMissingDevice = errors.New("storage: position without device id")

// positionColumns is the number of bound values per position row.
const positionColumns = 12

//go:embed migrations/*/*.sql
var migrationsFS embed.FS

// Store is the SQL persistence layer. The dialect is chosen by the DSN scheme:
// postgres://, mysql:// or sqlite://.
type Store struct {
	db *DB
	// tokens caches LookupToken behind a generation version, so the high
	// frequency token-authorisation path does not hit the database on every
	// request.
	tokens *tokenCache
	// referenceSchema adopts the Traccar Java tables instead of creating the
	// Go ones. See WithReferenceSchema.
	referenceSchema bool
}

// Option customises a Store.
type Option func(*Store)

// WithReferenceSchema attaches to a database written by the Java server. On the
// first start the reference tc_devices and tc_positions tables are adopted in
// place, so the devices and their history are visible without an export. A
// database that already has the Go tables is left untouched, and the migration
// that follows records the schema, so the option is harmless once adopted.
func WithReferenceSchema() Option {
	return func(s *Store) { s.referenceSchema = true }
}

// tokenCacheCapacity bounds the token cache. At this many live entries the next
// miss drops the whole cache; reads refill it, so this is a memory cap, not a
// correctness boundary.
const tokenCacheCapacity = 4096

// New opens the database, verifies connectivity and applies pending
// migrations.
func New(ctx context.Context, dsn string, options ...Option) (*Store, error) {
	selected, driverDSN, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(selected.driver, driverDSN)
	if err != nil {
		return nil, fmt.Errorf("storage: open: %w", err)
	}

	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)
	db.SetConnMaxLifetime(time.Hour)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: ping: %w", err)
	}

	store := &Store{db: &DB{DB: db, dialect: selected}}
	for _, option := range options {
		option(store)
	}
	store.tokens = newTokenCache(tokenCacheCapacity)
	if err := store.adoptReferenceSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the connection pool.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the pool for code that needs ad hoc queries (REST handlers).
func (s *Store) DB() *DB { return s.db }

// Dialect reports the database dialect selected by the DSN.
func (s *Store) Dialect() string { return s.db.dialect.name }

// Migrate applies every embedded migration that has not been applied yet.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, s.schemaMigrationsDDL()); err != nil {
		return fmt.Errorf("storage: create schema_migrations: %w", err)
	}

	dir := "migrations/" + s.db.dialect.migrations
	entries, err := fs.ReadDir(migrationsFS, dir)
	if err != nil {
		return fmt.Errorf("storage: read migrations: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}

		var exists bool
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version,
		).Scan(&exists); err != nil {
			return fmt.Errorf("storage: check migration %s: %w", name, err)
		}
		if exists {
			continue
		}

		content, err := migrationsFS.ReadFile(dir + "/" + name)
		if err != nil {
			return fmt.Errorf("storage: read %s: %w", name, err)
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("storage: begin %s: %w", name, err)
		}
		for _, statement := range splitStatements(string(content)) {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("storage: apply %s: %w", name, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("storage: record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("storage: commit %s: %w", name, err)
		}
	}

	return nil
}

// schemaMigrationsDDL is the one statement the migration runner needs before it
// can read the applied version list. The timestamp type differs per dialect.
func (s *Store) schemaMigrationsDDL() string {
	switch s.db.dialect.name {
	case "mysql":
		return `CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
		)`
	case "sqlite":
		return `CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`
	default:
		return `CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`
	}
}

// splitStatements splits a migration file on the semicolons that end a
// statement. A semicolon inside a '...' literal or a comment belongs to the
// text: 0009 explains the new columns with a comment that contains one, and
// splitting there produced a fragment that started mid-sentence.
//
// Dollar-quoted strings are not handled: the migrations are plain DDL.
func splitStatements(content string) []string {
	var (
		statements []string
		builder    strings.Builder
		inLine     bool
		inBlock    bool
		inString   bool
	)

	flush := func() {
		if statement := strings.TrimSpace(builder.String()); statement != "" && hasSQL(statement) {
			statements = append(statements, statement)
		}
		builder.Reset()
	}

	for i := 0; i < len(content); i++ {
		switch c := content[i]; {
		case inLine:
			builder.WriteByte(c)
			if c == '\n' {
				inLine = false
			}
		case inBlock:
			builder.WriteByte(c)
			if c == '*' && i+1 < len(content) && content[i+1] == '/' {
				builder.WriteByte('/')
				i++
				inBlock = false
			}
		case inString:
			builder.WriteByte(c)
			if c == '\'' {
				if i+1 < len(content) && content[i+1] == '\'' {
					builder.WriteByte(content[i+1])
					i++
				} else {
					inString = false
				}
			}
		case c == '\'':
			inString = true
			builder.WriteByte(c)
		case c == '-' && i+1 < len(content) && content[i+1] == '-':
			inLine = true
			builder.WriteString("--")
			i++
		case c == '/' && i+1 < len(content) && content[i+1] == '*':
			inBlock = true
			builder.WriteString("/*")
			i++
		case c == ';':
			flush()
		default:
			builder.WriteByte(c)
		}
	}
	flush()
	return statements
}

// hasSQL reports whether the text carries anything besides whitespace and
// comments, so a file that ends in a comment does not yield an empty query.
func hasSQL(statement string) bool {
	for i := 0; i < len(statement); i++ {
		switch c := statement[i]; {
		case c == '-' && i+1 < len(statement) && statement[i+1] == '-':
			for i < len(statement) && statement[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(statement) && statement[i+1] == '*':
			i += 2
			for i+1 < len(statement) && !(statement[i] == '*' && statement[i+1] == '/') {
				i++
			}
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
		default:
			return true
		}
	}
	return false
}

func migrationVersion(name string) (int, error) {
	prefix, _, found := strings.Cut(name, "_")
	if !found {
		return 0, fmt.Errorf("storage: migration %q has no version prefix", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("storage: migration %q: %w", name, err)
	}
	return version, nil
}

// Resolve implements codec.DeviceResolver. An unknown identifier yields a nil
// session and a nil error, matching the interface contract.
func (s *Store) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	var (
		deviceID int64
		model    *string
		disabled bool
	)
	err := s.db.QueryRowContext(context.Background(),
		`SELECT id, model, disabled FROM devices WHERE unique_id = $1`, uniqueID,
	).Scan(&deviceID, &model, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("storage: resolve %q: %w", uniqueID, err)
	}

	session := &codec.DeviceSession{
		DeviceID: deviceID,
		UniqueID: uniqueID,
		Disabled: disabled,
	}
	if model != nil {
		session.Model = *model
	}
	return session, nil
}

// UniqueID implements codec.UniqueIDProvider.
func (s *Store) UniqueID(deviceID int64) (string, error) {
	var uniqueID string
	err := s.db.QueryRowContext(context.Background(),
		`SELECT unique_id FROM devices WHERE id = $1`, deviceID,
	).Scan(&uniqueID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnknownDevice
	}
	if err != nil {
		return "", fmt.Errorf("storage: unique id %d: %w", deviceID, err)
	}
	return uniqueID, nil
}

// DeviceProtocol returns the protocol recorded for the device. found is false
// when the device does not exist; protocol is empty when the column is still
// NULL, which happens for devices registered before the gateway recorded it.
func (s *Store) DeviceProtocol(ctx context.Context, deviceID int64) (string, bool, error) {
	var protocol *string
	err := s.db.QueryRowContext(ctx,
		`SELECT protocol FROM devices WHERE id = $1`, deviceID,
	).Scan(&protocol)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("storage: device protocol %d: %w", deviceID, err)
	}
	if protocol == nil {
		return "", true, nil
	}
	return *protocol, true, nil
}

// Register creates the device if needed and returns its id. The protocol is
// recorded on first sight and never overwritten, so a device that is moved to
// another protocol has to be corrected in SQL.
func (s *Store) Register(uniqueID, protocol string) (int64, error) {
	if uniqueID == "" {
		return 0, ErrUnknownDevice
	}

	ctx := context.Background()
	var deviceID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM devices WHERE unique_id = $1`, uniqueID).Scan(&deviceID)
	switch {
	case err == nil:
		// The protocol is recorded on first sight and never overwritten.
		if _, err := s.db.ExecContext(ctx,
			`UPDATE devices SET protocol = COALESCE(protocol, NULLIF($2, '')) WHERE id = $1`,
			deviceID, protocol); err != nil {
			return 0, fmt.Errorf("storage: register %q: %w", uniqueID, err)
		}
		return deviceID, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("storage: register %q: %w", uniqueID, err)
	}

	deviceID, err = insertReturningID(ctx, s.db,
		`INSERT INTO devices (unique_id, protocol, attributes)
		 VALUES ($1, NULLIF($2, ''), '{}') RETURNING id`, uniqueID, protocol)
	if err != nil {
		if s.db.dialect.isDuplicate(err) {
			// Another connection inserted the same unique id between the lookup
			// and the insert; its row is the one both callers should use.
			if lookupErr := s.db.QueryRowContext(ctx,
				`SELECT id FROM devices WHERE unique_id = $1`, uniqueID).Scan(&deviceID); lookupErr == nil {
				return deviceID, nil
			}
		}
		return 0, fmt.Errorf("storage: register %q: %w", uniqueID, err)
	}
	return deviceID, nil
}

// Handle implements gateway.Sink.
func (s *Store) Handle(position *model.Position) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.InsertPositions(ctx, []*model.Position{position})
}

// InsertPositions writes a batch of positions in a single statement.
func (s *Store) InsertPositions(ctx context.Context, positions []*model.Position) error {
	if len(positions) == 0 {
		return nil
	}

	disabled, err := disabledDevices(ctx, s.db, positions)
	if err != nil {
		return err
	}

	values := make([]string, 0, len(positions))
	args := make([]any, 0, len(positions)*positionColumns)
	// Newest device time per device, used to advance devices.last_update.
	touched := make(map[int64]time.Time, 4)

	for _, position := range positions {
		if disabled[position.DeviceID] {
			continue
		}
		row, err := positionArgs(position)
		if err != nil {
			if errors.Is(err, ErrMissingDevice) {
				continue
			}
			return err
		}

		deviceTime := deviceTimeOf(position)
		if previous, ok := touched[position.DeviceID]; !ok || deviceTime.After(previous) {
			touched[position.DeviceID] = deviceTime
		}

		placeholders := make([]string, positionColumns)
		for column := 0; column < positionColumns; column++ {
			placeholders[column] = fmt.Sprintf("$%d", len(args)+column+1)
		}
		// The last two values are the JSON encoded geofence ids and attributes.
		values = append(values, "("+strings.Join(placeholders, ", ")+")")
		args = append(args, row...)
	}

	if len(values) == 0 {
		return nil
	}

	query := `INSERT INTO positions (
			device_id, protocol, device_time, valid, latitude, longitude,
			altitude, speed, course, accuracy, geofenceids, attributes
		) VALUES ` + strings.Join(values, ", ")

	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("storage: insert positions: %w", err)
	}
	return s.updateLastUpdate(ctx, touched)
}

// disabledDevices reports which of the batch's devices an operator has switched
// off. The gateway drops those positions itself, but a protocol that identifies
// the device inside the frame binds a session without knowing the flag, so the
// write boundary checks too.
func disabledDevices(
	ctx context.Context, db execer, positions []*model.Position,
) (map[int64]bool, error) {
	ids := make([]int64, 0, len(positions))
	seen := make(map[int64]bool, len(positions))
	for _, position := range positions {
		if position.DeviceID == 0 || seen[position.DeviceID] {
			continue
		}
		seen[position.DeviceID] = true
		ids = append(ids, position.DeviceID)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	args := make([]any, 0, len(ids))
	rows, err := db.QueryContext(ctx,
		`SELECT id FROM devices WHERE disabled AND `+deviceIDsClause(&args, "id", ids), args...)
	if err != nil {
		return nil, fmt.Errorf("storage: query disabled devices: %w", err)
	}
	defer rows.Close()

	disabled := make(map[int64]bool, len(ids))
	for rows.Next() {
		var deviceID int64
		if err := rows.Scan(&deviceID); err != nil {
			return nil, fmt.Errorf("storage: scan disabled device: %w", err)
		}
		disabled[deviceID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query disabled devices: %w", err)
	}
	return disabled, nil
}

// updateLastUpdate advances devices.last_update for the devices in the batch in
// a single statement. It never moves the column backwards, so a retransmitted
// or out-of-order position cannot rewind it.
func (s *Store) updateLastUpdate(ctx context.Context, touched map[int64]time.Time) error {
	return updateLastUpdateWith(ctx, s.db, touched)
}

// updateLastUpdateWith is updateLastUpdate on a given connection, so the report
// transaction can advance it in the same commit as the positions.
func updateLastUpdateWith(ctx context.Context, db execer, touched map[int64]time.Time) error {
	if len(touched) == 0 {
		return nil
	}

	for deviceID, at := range touched {
		if _, err := db.ExecContext(ctx,
			`UPDATE devices SET last_update = $2,
				position_id = COALESCE((
					SELECT p.id FROM positions AS p
					WHERE p.device_id = $1
					ORDER BY p.device_time DESC, p.id DESC
					LIMIT 1), position_id)
			 WHERE id = $1 AND (last_update IS NULL OR last_update < $2)`,
			deviceID, at.UTC(),
		); err != nil {
			return fmt.Errorf("storage: update device last_update: %w", err)
		}
	}
	return nil
}

// deviceTimeOf returns the device time of a position, defaulting to now like
// the insert does for positions that carry no time.
func deviceTimeOf(position *model.Position) time.Time {
	if position.Time.IsZero() {
		return time.Now().UTC()
	}
	return position.Time.UTC()
}

// positionArgs flattens a position into the column order of the insert.
func positionArgs(position *model.Position) ([]any, error) {
	if position.DeviceID == 0 {
		return nil, ErrMissingDevice
	}

	deviceTime := deviceTimeOf(position)

	attributes, err := json.Marshal(position.Attributes)
	if err != nil {
		return nil, fmt.Errorf("storage: encode attributes: %w", err)
	}

	geofenceIDs, err := json.Marshal(position.GeofenceIDs)
	if err != nil {
		return nil, fmt.Errorf("storage: encode geofences: %w", err)
	}

	return []any{
		position.DeviceID,
		position.Protocol,
		deviceTime.UTC(),
		position.Valid,
		position.Latitude,
		position.Longitude,
		position.Altitude,
		position.Speed,
		position.Course,
		position.Accuracy,
		string(geofenceIDs),
		string(attributes),
	}, nil
}
