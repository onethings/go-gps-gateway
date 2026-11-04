package storage

import (
	"context"
	"embed"
	"fmt"
	"strings"
)

// compatFS holds the per dialect steps that turn a Traccar Java database into
// one the Go schema can use. They are not part of the migrations: they run
// once, before them, and only when the operator asks for the reference schema.
//
//go:embed compat/*.sql
var compatFS embed.FS

// SchemaOption maps the configured database.schema value onto the store
// options. An empty value is the Go schema, so a configuration written before
// the key existed keeps working.
func SchemaOption(name string) ([]Option, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "native":
		return nil, nil
	case "reference":
		return []Option{WithReferenceSchema()}, nil
	default:
		return nil, fmt.Errorf(
			"storage: unknown database schema %q (use native or reference)", name)
	}
}

// adoptReferenceSchema renames the reference tables to the Go names and gives
// them the columns the Go code reads and writes. It is a no-op unless the store
// was opened with WithReferenceSchema, and a no-op on a database that has no
// tc_devices table, which is what every later start looks like.
func (s *Store) adoptReferenceSchema(ctx context.Context) error {
	if !s.referenceSchema {
		return nil
	}
	if s.db.dialect.name == "mysql" {
		return fmt.Errorf(
			"storage: database.schema=reference is not implemented for MySQL; use PostgreSQL or SQLite")
	}

	hasReference, err := s.tableExists(ctx, "tc_devices")
	if err != nil {
		return fmt.Errorf("storage: look for tc_devices: %w", err)
	}
	if !hasReference {
		return nil
	}

	hasNative, err := s.tableExists(ctx, "devices")
	if err != nil {
		return fmt.Errorf("storage: look for devices: %w", err)
	}
	if hasNative {
		return fmt.Errorf(
			"storage: both tc_devices and devices exist; drop the empty devices table or point " +
				"database.url at the reference database")
	}

	content, err := compatFS.ReadFile("compat/" + s.db.dialect.name + ".sql")
	if err != nil {
		return fmt.Errorf("storage: read reference schema steps: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin reference schema adoption: %w", err)
	}
	for _, statement := range splitStatements(string(content)) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("storage: adopt reference schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit reference schema adoption: %w", err)
	}
	return nil
}

// tableExists reports whether a table of that name is visible in the current
// schema. It is a plain query so every dialect can answer it the same way.
func (s *Store) tableExists(ctx context.Context, name string) (bool, error) {
	query := `SELECT EXISTS (SELECT 1 FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = $1)`
	if s.db.dialect.name == "sqlite" {
		query = `SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = $1)`
	}

	var exists bool
	if err := s.db.QueryRowContext(ctx, query, name).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}
