package postgres

import (
	"context"
	"fmt"

	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

var attributeSpec = resourceSpec[model.Attribute]{
	table:   "attributes",
	columns: []string{"description", "attribute", "expression", "type", "priority"},
	keyword: []string{"description", "attribute", "expression", "type"},
	values: func(attribute *model.Attribute) ([]any, error) {
		return []any{
			attribute.Description, attribute.Attribute, attribute.Expression,
			attribute.Type, attribute.Priority,
		}, nil
	},
	scan: func(row rowScanner) (*model.Attribute, error) {
		var attribute model.Attribute
		if err := row.Scan(&attribute.ID, &attribute.Description, &attribute.Attribute,
			&attribute.Expression, &attribute.Type, &attribute.Priority); err != nil {
			return nil, notFoundAs(fmt.Errorf("postgres: scan attribute: %w", err))
		}
		return &attribute, nil
	},
	id:    func(attribute *model.Attribute) int64 { return attribute.ID },
	setID: func(attribute *model.Attribute, id int64) { attribute.ID = id },
}

// CreateAttribute inserts a computed attribute and fills its ID.
func (s *Store) CreateAttribute(ctx context.Context, attribute *model.Attribute) error {
	return createResource(ctx, s.db, attributeSpec, attribute)
}

// UpdateAttribute replaces the columns of a computed attribute.
func (s *Store) UpdateAttribute(ctx context.Context, attribute *model.Attribute) error {
	return updateResource(ctx, s.db, attributeSpec, attribute)
}

// DeleteAttribute removes a computed attribute and the grants that name it.
func (s *Store) DeleteAttribute(ctx context.Context, attributeID int64) error {
	if err := deleteResource(ctx, s.db, attributeSpec.table, attributeID); err != nil {
		return err
	}
	return s.ClearPermissions(ctx, permissions.ObjectAttribute, attributeID)
}

// Attributes returns the computed attributes matching the filter, ordered by
// id.
func (s *Store) Attributes(ctx context.Context, filter model.ResourceFilter) ([]*model.Attribute, error) {
	return listResources(ctx, s.db, attributeSpec, filter)
}
