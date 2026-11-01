package postgres

import (
	"context"
	"fmt"

	"traccar-server_go/go_server/internal/model"
)

var maintenanceSpec = resourceSpec[model.Maintenance]{
	table:   "maintenances",
	columns: []string{"name", "type", "start", "period", "attributes"},
	keyword: []string{"name", "type"},
	values: func(maintenance *model.Maintenance) ([]any, error) {
		attributes, err := encodeAttributes(maintenance.Attributes)
		return []any{
			maintenance.Name, maintenance.Type, maintenance.Start, maintenance.Period, attributes,
		}, err
	},
	scan: func(row rowScanner) (*model.Maintenance, error) {
		var (
			maintenance model.Maintenance
			attributes  []byte
		)
		if err := row.Scan(&maintenance.ID, &maintenance.Name, &maintenance.Type,
			&maintenance.Start, &maintenance.Period, &attributes); err != nil {
			return nil, notFoundAs(fmt.Errorf("postgres: scan maintenance: %w", err))
		}
		decoded, err := decodeAttributes(attributes)
		if err != nil {
			return nil, fmt.Errorf("postgres: maintenance %d: %w", maintenance.ID, err)
		}
		maintenance.Attributes = decoded
		return &maintenance, nil
	},
	id:    func(maintenance *model.Maintenance) int64 { return maintenance.ID },
	setID: func(maintenance *model.Maintenance, id int64) { maintenance.ID = id },
}

// CreateMaintenance inserts a service interval and fills its ID.
func (s *Store) CreateMaintenance(ctx context.Context, maintenance *model.Maintenance) error {
	return createResource(ctx, s.db, maintenanceSpec, maintenance)
}

// UpdateMaintenance replaces the mutable columns of a service interval.
func (s *Store) UpdateMaintenance(ctx context.Context, maintenance *model.Maintenance) error {
	return updateResource(ctx, s.db, maintenanceSpec, maintenance)
}

// DeleteMaintenance removes a service interval.
func (s *Store) DeleteMaintenance(ctx context.Context, maintenanceID int64) error {
	return deleteResource(ctx, s.db, maintenanceSpec.table, maintenanceID)
}

// Maintenances returns the service intervals matching the filter.
func (s *Store) Maintenances(ctx context.Context, filter model.ResourceFilter) ([]*model.Maintenance, error) {
	return listResources(ctx, s.db, maintenanceSpec, filter)
}
