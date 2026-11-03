package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
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

// geofenceSpec exposes the geofences table to the CRUD helpers. The area stays
// WKT text in the column and is parsed on the way in and out, so a row that the
// engine could not read is a row the API cannot write either.
var geofenceSpec = resourceSpec[geofence.Geofence]{
	table:   "geofences",
	columns: []string{"calendar_id", "name", "description", "area", "attributes"},
	keyword: []string{"name", "description"},
	values: func(fence *geofence.Geofence) ([]any, error) {
		attributes, err := encodeAttributes(fence.Attributes)
		if err != nil {
			return nil, err
		}
		area := fence.WKT()
		if area == "" {
			return nil, fmt.Errorf("postgres: geofence %d has no area", fence.ID)
		}
		return []any{nullableID(fence.CalendarID), fence.Name, fence.Description, area, attributes}, nil
	},
	scan: func(row rowScanner) (*geofence.Geofence, error) {
		var (
			fence      geofence.Geofence
			calendarID sql.NullInt64
			area       string
			attributes []byte
		)
		if err := row.Scan(&fence.ID, &calendarID, &fence.Name, &fence.Description,
			&area, &attributes); err != nil {
			return nil, notFoundAs(fmt.Errorf("postgres: scan geofence: %w", err))
		}
		fence.CalendarID = calendarID.Int64
		parsed, err := geofence.Parse(area)
		if err != nil {
			return nil, fmt.Errorf("postgres: geofence %d: %w", fence.ID, err)
		}
		fence.Area = parsed
		decoded, err := decodeAttributes(attributes)
		if err != nil {
			return nil, fmt.Errorf("postgres: geofence %d: %w", fence.ID, err)
		}
		fence.Attributes = decoded
		return &fence, nil
	},
	id:    func(fence *geofence.Geofence) int64 { return fence.ID },
	setID: func(fence *geofence.Geofence, id int64) { fence.ID = id },
}

// CreateGeofence inserts a geofence and fills its ID.
func (s *Store) CreateGeofence(ctx context.Context, fence *geofence.Geofence) error {
	return createResource(ctx, s.db, geofenceSpec, fence)
}

// UpdateGeofence replaces the mutable columns of a geofence.
func (s *Store) UpdateGeofence(ctx context.Context, fence *geofence.Geofence) error {
	return updateResource(ctx, s.db, geofenceSpec, fence)
}

// DeleteGeofence removes a geofence. Events that referred to it keep their
// rows, because the geofence id there is a plain number and not a reference.
// The grants naming the geofence are dropped with it.
func (s *Store) DeleteGeofence(ctx context.Context, geofenceID int64) error {
	if err := deleteResource(ctx, s.db, geofenceSpec.table, geofenceID); err != nil {
		return err
	}
	return s.ClearPermissions(ctx, permissions.ObjectGeofence, geofenceID)
}

// QueryGeofences returns the geofences matching the filter. Unlike Geofences it
// reports an unreadable area instead of skipping the row, which is what a CRUD
// caller wants to see.
func (s *Store) QueryGeofences(ctx context.Context, filter model.ResourceFilter) ([]*geofence.Geofence, error) {
	return listResources(ctx, s.db, geofenceSpec, filter)
}
