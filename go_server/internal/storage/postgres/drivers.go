package postgres

import (
	"context"
	"fmt"

	"traccar-server_go/go_server/internal/model"
)

var driverSpec = resourceSpec[model.Driver]{
	table:   "drivers",
	columns: []string{"name", "unique_id", "attributes"},
	keyword: []string{"name", "unique_id"},
	values: func(driver *model.Driver) ([]any, error) {
		attributes, err := encodeAttributes(driver.Attributes)
		return []any{driver.Name, driver.UniqueID, attributes}, err
	},
	scan: func(row rowScanner) (*model.Driver, error) {
		var (
			driver     model.Driver
			attributes []byte
		)
		if err := row.Scan(&driver.ID, &driver.Name, &driver.UniqueID, &attributes); err != nil {
			return nil, notFoundAs(fmt.Errorf("postgres: scan driver: %w", err))
		}
		decoded, err := decodeAttributes(attributes)
		if err != nil {
			return nil, fmt.Errorf("postgres: driver %d: %w", driver.ID, err)
		}
		driver.Attributes = decoded
		return &driver, nil
	},
	id:    func(driver *model.Driver) int64 { return driver.ID },
	setID: func(driver *model.Driver, id int64) { driver.ID = id },
}

// CreateDriver inserts a driver and fills its ID. The unique id is trimmed
// first, the same way the Java setter does.
func (s *Store) CreateDriver(ctx context.Context, driver *model.Driver) error {
	driver.Normalize()
	return createResource(ctx, s.db, driverSpec, driver)
}

// UpdateDriver replaces the mutable columns of a driver.
func (s *Store) UpdateDriver(ctx context.Context, driver *model.Driver) error {
	driver.Normalize()
	return updateResource(ctx, s.db, driverSpec, driver)
}

// DeleteDriver removes a driver.
func (s *Store) DeleteDriver(ctx context.Context, driverID int64) error {
	return deleteResource(ctx, s.db, driverSpec.table, driverID)
}

// Drivers returns the drivers matching the filter, ordered by id.
func (s *Store) Drivers(ctx context.Context, filter model.ResourceFilter) ([]*model.Driver, error) {
	return listResources(ctx, s.db, driverSpec, filter)
}
