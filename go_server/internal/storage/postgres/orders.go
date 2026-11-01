package postgres

import (
	"context"
	"fmt"

	"traccar-server_go/go_server/internal/model"
)

var orderSpec = resourceSpec[model.Order]{
	table:   "orders",
	columns: []string{"unique_id", "description", "from_address", "to_address", "attributes"},
	keyword: []string{"unique_id", "description", "from_address", "to_address"},
	values: func(order *model.Order) ([]any, error) {
		attributes, err := encodeAttributes(order.Attributes)
		return []any{
			order.UniqueID, order.Description, order.FromAddress, order.ToAddress, attributes,
		}, err
	},
	scan: func(row rowScanner) (*model.Order, error) {
		var (
			order      model.Order
			attributes []byte
		)
		if err := row.Scan(&order.ID, &order.UniqueID, &order.Description,
			&order.FromAddress, &order.ToAddress, &attributes); err != nil {
			return nil, notFoundAs(fmt.Errorf("postgres: scan order: %w", err))
		}
		decoded, err := decodeAttributes(attributes)
		if err != nil {
			return nil, fmt.Errorf("postgres: order %d: %w", order.ID, err)
		}
		order.Attributes = decoded
		return &order, nil
	},
	id:    func(order *model.Order) int64 { return order.ID },
	setID: func(order *model.Order, id int64) { order.ID = id },
}

// CreateOrder inserts an order and fills its ID.
func (s *Store) CreateOrder(ctx context.Context, order *model.Order) error {
	return createResource(ctx, s.db, orderSpec, order)
}

// UpdateOrder replaces the mutable columns of an order.
func (s *Store) UpdateOrder(ctx context.Context, order *model.Order) error {
	return updateResource(ctx, s.db, orderSpec, order)
}

// DeleteOrder removes an order.
func (s *Store) DeleteOrder(ctx context.Context, orderID int64) error {
	return deleteResource(ctx, s.db, orderSpec.table, orderID)
}

// Orders returns the orders matching the filter, ordered by id.
func (s *Store) Orders(ctx context.Context, filter model.ResourceFilter) ([]*model.Order, error) {
	return listResources(ctx, s.db, orderSpec, filter)
}
