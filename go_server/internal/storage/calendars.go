package postgres

import (
	"context"
	"fmt"

	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

var calendarSpec = resourceSpec[model.Calendar]{
	table:   "calendars",
	columns: []string{"name", "data", "attributes"},
	keyword: []string{"name"},
	values: func(calendar *model.Calendar) ([]any, error) {
		attributes, err := encodeAttributes(calendar.Attributes)
		return []any{calendar.Name, calendar.Data, attributes}, err
	},
	scan: func(row rowScanner) (*model.Calendar, error) {
		var (
			calendar   model.Calendar
			attributes []byte
		)
		if err := row.Scan(&calendar.ID, &calendar.Name, &calendar.Data, &attributes); err != nil {
			return nil, notFoundAs(fmt.Errorf("postgres: scan calendar: %w", err))
		}
		decoded, err := decodeAttributes(attributes)
		if err != nil {
			return nil, fmt.Errorf("postgres: calendar %d: %w", calendar.ID, err)
		}
		calendar.Attributes = decoded
		return &calendar, nil
	},
	id:    func(calendar *model.Calendar) int64 { return calendar.ID },
	setID: func(calendar *model.Calendar, id int64) { calendar.ID = id },
}

// CreateCalendar inserts a calendar and fills its ID.
func (s *Store) CreateCalendar(ctx context.Context, calendar *model.Calendar) error {
	return createResource(ctx, s.db, calendarSpec, calendar)
}

// UpdateCalendar replaces the mutable columns of a calendar.
func (s *Store) UpdateCalendar(ctx context.Context, calendar *model.Calendar) error {
	return updateResource(ctx, s.db, calendarSpec, calendar)
}

// DeleteCalendar removes a calendar. Geofences and reports that referenced it
// keep their rows and lose the reference; its grants go with it.
func (s *Store) DeleteCalendar(ctx context.Context, calendarID int64) error {
	if err := deleteResource(ctx, s.db, calendarSpec.table, calendarID); err != nil {
		return err
	}
	return s.ClearPermissions(ctx, permissions.ObjectCalendar, calendarID)
}

// Calendars returns the calendars matching the filter, ordered by id.
func (s *Store) Calendars(ctx context.Context, filter model.ResourceFilter) ([]*model.Calendar, error) {
	return listResources(ctx, s.db, calendarSpec, filter)
}
