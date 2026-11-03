// Package postgres persists devices and positions. It satisfies the
// gateway.DeviceDirectory and gateway.Sink interfaces structurally, so the
// gateway never imports this package.
package postgres

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
var ErrUnknownDevice = errors.New("postgres: unknown device")

// ErrMissingDevice is returned when a position has no device id.
var ErrMissingDevice = errors.New("postgres: position without device id")

// positionColumns is the number of bound values per position row.
const positionColumns = 12

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the PostgreSQL persistence layer.
type Store struct {
	db *sql.DB
	// tokens caches LookupToken behind a generation version, so the high
	// frequency token-authorisation path does not hit the database on every
	// request.
	tokens *tokenCache
}

// tokenCacheCapacity bounds the token cache. At this many live entries the next
// miss drops the whole cache; reads refill it, so this is a memory cap, not a
// correctness boundary.
const tokenCacheCapacity = 4096

// New opens the database, verifies connectivity and applies pending
// migrations.
func New(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}

	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)
	db.SetConnMaxLifetime(time.Hour)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}

	store := &Store{db: db}
	store.tokens = newTokenCache(tokenCacheCapacity)
	if err := store.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the connection pool.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the pool for code that needs ad hoc queries (REST handlers).
func (s *Store) DB() *sql.DB { return s.db }

// Migrate applies every embedded migration that has not been applied yet.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("postgres: create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("postgres: read migrations: %w", err)
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
			return fmt.Errorf("postgres: check migration %s: %w", name, err)
		}
		if exists {
			continue
		}

		content, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("postgres: read %s: %w", name, err)
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("postgres: begin %s: %w", name, err)
		}
		for _, statement := range splitStatements(string(content)) {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("postgres: apply %s: %w", name, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("postgres: record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("postgres: commit %s: %w", name, err)
		}
	}

	return nil
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
		return 0, fmt.Errorf("postgres: migration %q has no version prefix", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("postgres: migration %q: %w", name, err)
	}
	return version, nil
}

// Resolve implements codec.DeviceResolver. An unknown identifier yields a nil
// session and a nil error, matching the interface contract.
func (s *Store) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	var (
		deviceID int64
		model    *string
	)
	err := s.db.QueryRowContext(context.Background(),
		`SELECT id, model FROM devices WHERE unique_id = $1`, uniqueID,
	).Scan(&deviceID, &model)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: resolve %q: %w", uniqueID, err)
	}

	session := &codec.DeviceSession{DeviceID: deviceID, UniqueID: uniqueID}
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
		return "", fmt.Errorf("postgres: unique id %d: %w", deviceID, err)
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
		return "", false, fmt.Errorf("postgres: device protocol %d: %w", deviceID, err)
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

	var deviceID int64
	err := s.db.QueryRowContext(context.Background(),
		`INSERT INTO devices (unique_id, protocol) VALUES ($1, NULLIF($2, ''))
		 ON CONFLICT (unique_id) DO UPDATE
		 SET protocol = COALESCE(devices.protocol, EXCLUDED.protocol)
		 RETURNING id`, uniqueID, protocol,
	).Scan(&deviceID)
	if err != nil {
		return 0, fmt.Errorf("postgres: register %q: %w", uniqueID, err)
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

	values := make([]string, 0, len(positions))
	args := make([]any, 0, len(positions)*positionColumns)
	// Newest device time per device, used to advance devices.last_update.
	touched := make(map[int64]time.Time, 4)

	for _, position := range positions {
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
		placeholders[positionColumns-2] += "::jsonb"
		placeholders[positionColumns-1] += "::jsonb"

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
		return fmt.Errorf("postgres: insert positions: %w", err)
	}
	return s.updateLastUpdate(ctx, touched)
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

	type row struct {
		ID int64     `json:"id"`
		At time.Time `json:"at"`
	}
	rows := make([]row, 0, len(touched))
	for deviceID, at := range touched {
		rows = append(rows, row{ID: deviceID, At: at.UTC()})
	}

	encoded, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("postgres: encode last_update: %w", err)
	}

	if _, err := db.ExecContext(ctx,
		`UPDATE devices AS d SET last_update = v.at,
			position_id = COALESCE((
				SELECT p.id FROM positions AS p
				WHERE p.device_id = d.id
				ORDER BY p.device_time DESC, p.id DESC
				LIMIT 1), d.position_id)
		 FROM jsonb_to_recordset($1::jsonb) AS v(id bigint, at timestamptz)
		 WHERE d.id = v.id AND (d.last_update IS NULL OR d.last_update < v.at)`,
		string(encoded),
	); err != nil {
		return fmt.Errorf("postgres: update device last_update: %w", err)
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
		return nil, fmt.Errorf("postgres: encode attributes: %w", err)
	}

	geofenceIDs, err := json.Marshal(position.GeofenceIDs)
	if err != nil {
		return nil, fmt.Errorf("postgres: encode geofences: %w", err)
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
