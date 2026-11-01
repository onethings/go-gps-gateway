package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"traccar-server_go/go_server/internal/model"
)

var reportSpec = resourceSpec[model.Report]{
	table:   "reports",
	columns: []string{"calendar_id", "type", "description", "attributes"},
	keyword: []string{"type", "description"},
	values: func(report *model.Report) ([]any, error) {
		attributes, err := encodeAttributes(report.Attributes)
		return []any{nullableID(report.CalendarID), report.Type, report.Description, attributes}, err
	},
	scan: func(row rowScanner) (*model.Report, error) {
		var (
			report     model.Report
			calendarID sql.NullInt64
			attributes []byte
		)
		if err := row.Scan(&report.ID, &calendarID, &report.Type, &report.Description, &attributes); err != nil {
			return nil, notFoundAs(fmt.Errorf("postgres: scan report: %w", err))
		}
		report.CalendarID = calendarID.Int64
		decoded, err := decodeAttributes(attributes)
		if err != nil {
			return nil, fmt.Errorf("postgres: report %d: %w", report.ID, err)
		}
		report.Attributes = decoded
		return &report, nil
	},
	id:    func(report *model.Report) int64 { return report.ID },
	setID: func(report *model.Report, id int64) { report.ID = id },
}

// CreateReport inserts a scheduled report definition and fills its ID.
func (s *Store) CreateReport(ctx context.Context, report *model.Report) error {
	return createResource(ctx, s.db, reportSpec, report)
}

// UpdateReport replaces the mutable columns of a report definition.
func (s *Store) UpdateReport(ctx context.Context, report *model.Report) error {
	return updateResource(ctx, s.db, reportSpec, report)
}

// DeleteReport removes a report definition.
func (s *Store) DeleteReport(ctx context.Context, reportID int64) error {
	return deleteResource(ctx, s.db, reportSpec.table, reportID)
}

// Reports returns the report definitions matching the filter, ordered by id.
func (s *Store) Reports(ctx context.Context, filter model.ResourceFilter) ([]*model.Report, error) {
	return listResources(ctx, s.db, reportSpec, filter)
}
