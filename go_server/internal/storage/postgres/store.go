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
const positionColumns = 11

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the PostgreSQL persistence layer.
type Store struct {
	db *sql.DB
}

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

// splitStatements splits a migration file on semicolons. The migrations are
// plain DDL without semicolons inside literals, which keeps this simple.
func splitStatements(content string) []string {
	parts := strings.Split(content, ";")
	statements := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") && len(strings.Split(trimmed, "\n")) == 1 {
			continue
		}
		statements = append(statements, trimmed)
	}
	return statements
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
	var deviceID int64
	err := s.db.QueryRowContext(context.Background(),
		`SELECT id FROM devices WHERE unique_id = $1`, uniqueID,
	).Scan(&deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: resolve %q: %w", uniqueID, err)
	}
	return &codec.DeviceSession{DeviceID: deviceID, UniqueID: uniqueID}, nil
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

// Register creates the device if needed and returns its id.
func (s *Store) Register(uniqueID string) (int64, error) {
	if uniqueID == "" {
		return 0, ErrUnknownDevice
	}

	var deviceID int64
	err := s.db.QueryRowContext(context.Background(),
		`INSERT INTO devices (unique_id) VALUES ($1)
		 ON CONFLICT (unique_id) DO UPDATE SET unique_id = EXCLUDED.unique_id
		 RETURNING id`, uniqueID,
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

	for _, position := range positions {
		row, err := positionArgs(position)
		if err != nil {
			if errors.Is(err, ErrMissingDevice) {
				continue
			}
			return err
		}

		placeholders := make([]string, positionColumns)
		for column := 0; column < positionColumns; column++ {
			placeholders[column] = fmt.Sprintf("$%d", len(args)+column+1)
		}
		// The last value is the JSON encoded attribute map.
		placeholders[positionColumns-1] += "::jsonb"

		values = append(values, "("+strings.Join(placeholders, ", ")+")")
		args = append(args, row...)
	}

	if len(values) == 0 {
		return nil
	}

	query := `INSERT INTO positions (
			device_id, protocol, device_time, valid, latitude, longitude,
			altitude, speed, course, accuracy, attributes
		) VALUES ` + strings.Join(values, ", ")

	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("postgres: insert positions: %w", err)
	}
	return nil
}

// positionArgs flattens a position into the column order of the insert.
func positionArgs(position *model.Position) ([]any, error) {
	if position.DeviceID == 0 {
		return nil, ErrMissingDevice
	}

	deviceTime := position.Time
	if deviceTime.IsZero() {
		deviceTime = time.Now()
	}

	attributes, err := json.Marshal(position.Attributes)
	if err != nil {
		return nil, fmt.Errorf("postgres: encode attributes: %w", err)
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
		string(attributes),
	}, nil
}
