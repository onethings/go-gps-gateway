package storage

import (
	"context"
	"database/sql"
	"fmt"

	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

var groupSpec = resourceSpec[model.Group]{
	table:   "device_groups",
	columns: []string{"name", "group_id", "attributes"},
	keyword: []string{"name"},
	values: func(group *model.Group) ([]any, error) {
		attributes, err := encodeAttributes(group.Attributes)
		return []any{group.Name, nullableID(group.GroupID), attributes}, err
	},
	scan: func(row rowScanner) (*model.Group, error) {
		var (
			group      model.Group
			parentID   sql.NullInt64
			attributes []byte
		)
		if err := row.Scan(&group.ID, &group.Name, &parentID, &attributes); err != nil {
			return nil, notFoundAs(fmt.Errorf("storage: scan group: %w", err))
		}
		group.GroupID = parentID.Int64
		decoded, err := decodeAttributes(attributes)
		if err != nil {
			return nil, fmt.Errorf("storage: group %d: %w", group.ID, err)
		}
		group.Attributes = decoded
		return &group, nil
	},
	id:    func(group *model.Group) int64 { return group.ID },
	setID: func(group *model.Group, id int64) { group.ID = id },
}

// CreateGroup inserts a group and fills its ID.
func (s *Store) CreateGroup(ctx context.Context, group *model.Group) error {
	if err := createResource(ctx, s.db, groupSpec, group); err != nil {
		return err
	}
	s.invalidateTokenCache()
	return nil
}

// UpdateGroup replaces the mutable columns of a group.
func (s *Store) UpdateGroup(ctx context.Context, group *model.Group) error {
	if err := updateResource(ctx, s.db, groupSpec, group); err != nil {
		return err
	}
	s.invalidateTokenCache()
	return nil
}

// DeleteGroup removes a group. Child groups keep their rows and lose the
// parent reference; devices keep pointing at nothing. The grants that name the
// group on either side are dropped with it, because the generic permission
// table has no foreign key to clean them up.
func (s *Store) DeleteGroup(ctx context.Context, groupID int64) error {
	if err := deleteResource(ctx, s.db, groupSpec.table, groupID); err != nil {
		return err
	}
	return s.ClearPermissions(ctx, permissions.ObjectGroup, groupID)
}

// Groups returns the groups matching the filter, ordered by id.
func (s *Store) Groups(ctx context.Context, filter model.ResourceFilter) ([]*model.Group, error) {
	return listResources(ctx, s.db, groupSpec, filter)
}
