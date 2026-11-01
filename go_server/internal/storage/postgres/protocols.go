package postgres

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
			 VALUES ($1, $2, $3)
			 ON CONFLICT (protocol) DO NOTHING`,
			settings.Protocol, settings.Port, transport,
		); err != nil {
			return fmt.Errorf("postgres: ensure protocol %q: %w", settings.Protocol, err)
		}
	}
	return nil
}

// ProtocolSettings returns every protocol row ordered by name.
func (s *Store) ProtocolSettings(ctx context.Context) ([]model.ProtocolSettings, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, protocol, port, transport FROM protocol_settings ORDER BY protocol`)
	if err != nil {
		return nil, fmt.Errorf("postgres: query protocol settings: %w", err)
	}
	defer rows.Close()

	var settings []model.ProtocolSettings
	for rows.Next() {
		var row model.ProtocolSettings
		if err := rows.Scan(&row.ID, &row.Protocol, &row.Port, &row.Transport); err != nil {
			return nil, fmt.Errorf("postgres: scan protocol settings: %w", err)
		}
		settings = append(settings, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: query protocol settings: %w", err)
	}
	return settings, nil
}

// UpdateProtocolSettings stores a new port and transport for one row. A
// protocol_name change is deliberately impossible: the name is the identity
// the registry and the device rows use.
func (s *Store) UpdateProtocolSettings(
	ctx context.Context, id int64, port int, transport string,
) (*model.ProtocolSettings, error) {
	var row model.ProtocolSettings
	err := s.db.QueryRowContext(ctx,
		`UPDATE protocol_settings SET port = $2, transport = $3 WHERE id = $1
		 RETURNING id, protocol, port, transport`,
		id, port, transport,
	).Scan(&row.ID, &row.Protocol, &row.Port, &row.Transport)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrProtocolNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: update protocol settings %d: %w", id, err)
	}
	return &row, nil
}
