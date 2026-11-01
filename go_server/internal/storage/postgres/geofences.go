package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"traccar-server_go/go_server/internal/geofence"
)

// Geofences loads every geofence and parses its Traccar WKT area. A row with an
// unparseable area is skipped and reported in the returned error together with
// the geofences that did parse, so one bad row does not disable the engine.
func (s *Store) Geofences(ctx context.Context) ([]*geofence.Geofence, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, area, attributes FROM geofences ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: query geofences: %w", err)
	}
	defer rows.Close()

	var (
		geofences []*geofence.Geofence
		failures  []error
	)
	for rows.Next() {
		var (
			id         int64
			name       string
			area       string
			attributes []byte
		)
		if err := rows.Scan(&id, &name, &area, &attributes); err != nil {
			return nil, fmt.Errorf("postgres: scan geofence: %w", err)
		}

		parsed, err := geofence.Parse(area)
		if err != nil {
			failures = append(failures, fmt.Errorf("postgres: geofence %d: %w", id, err))
			continue
		}

		fence := &geofence.Geofence{ID: id, Name: name, Area: parsed}
		if len(attributes) > 0 {
			if err := json.Unmarshal(attributes, &fence.Attributes); err != nil {
				failures = append(failures, fmt.Errorf("postgres: geofence %d attributes: %w", id, err))
			}
		}
		geofences = append(geofences, fence)
	}
	if err := rows.Err(); err != nil {
		return geofences, fmt.Errorf("postgres: query geofences: %w", err)
	}
	return geofences, errors.Join(failures...)
}
