package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"traccar-server_go/go_server/internal/model"
)

// EnsureProtocolSettings inserts a settings row for every protocol the
// gateway ships, keeping what an administrator already stored. It is called on
// startup so the web interface has one row per protocol to edit.
func (s *Store) EnsureProtocolSettings(ctx context.Context, defaults []model.ProtocolSettings) error {
	for _, settings := range defaults {
		if settings.Protocol == "" {
			continue
		}
		transport := settings.Transport
		if transport == "" {
			transport = model.TransportTCP
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO protocol_settings (protocol, port, transport)
			 VALUES ($1, $2, $3)`,
			settings.Protocol, settings.Port, transport,
		); err != nil && !s.db.dialect.isDuplicate(err) {
			return fmt.Errorf("storage: ensure protocol %q: %w", settings.Protocol, err)
		}
	}
	return nil
}

// ProtocolSettings returns every protocol row ordered by name.
func (s *Store) ProtocolSettings(ctx context.Context) ([]model.ProtocolSettings, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, protocol, port, transport FROM protocol_settings ORDER BY protocol`)
	if err != nil {
		return nil, fmt.Errorf("storage: query protocol settings: %w", err)
	}
	defer rows.Close()

	var settings []model.ProtocolSettings
	for rows.Next() {
		var row model.ProtocolSettings
		if err := rows.Scan(&row.ID, &row.Protocol, &row.Port, &row.Transport); err != nil {
			return nil, fmt.Errorf("storage: scan protocol settings: %w", err)
		}
		settings = append(settings, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query protocol settings: %w", err)
	}
	return settings, nil
}

// UpdateProtocolSettings stores a new port and transport for one row. A
// protocol_name change is deliberately impossible: the name is the identity
// the registry and the device rows use.
func (s *Store) UpdateProtocolSettings(
	ctx context.Context, id int64, port int, transport string,
) (*model.ProtocolSettings, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE protocol_settings SET port = $2, transport = $3 WHERE id = $1`,
		id, port, transport)
	if err != nil {
		return nil, fmt.Errorf("storage: update protocol settings %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("storage: update protocol settings %d: %w", id, err)
	}
	if affected == 0 {
		return nil, model.ErrProtocolNotFound
	}

	var row model.ProtocolSettings
	if err := s.db.QueryRowContext(ctx,
		`SELECT id, protocol, port, transport FROM protocol_settings WHERE id = $1`,
		id).Scan(&row.ID, &row.Protocol, &row.Port, &row.Transport); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrProtocolNotFound
		}
		return nil, fmt.Errorf("storage: update protocol settings %d: %w", id, err)
	}
	return &row, nil
}
