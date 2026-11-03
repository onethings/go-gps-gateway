package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

// The store satisfies the service that resolves group inheritance.
var _ permissions.Store = (*Store)(nil)

const permissionColumns = "owner_type, owner_id, property_type, property_id"

// UserGrants returns the direct device and group grants of a user, in id order.
// Group grants are what device access is inherited through; see
// permissions.Service.DeviceIDs.
func (s *Store) UserGrants(ctx context.Context, userID int64) ([]int64, []int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT property_type, property_id FROM permissions
		 WHERE owner_type = 'user' AND owner_id = $1
		   AND property_type IN ('device', 'group')
		 ORDER BY property_type, property_id`,
		userID)
	if err != nil {
		return nil, nil, fmt.Errorf("storage: query grants of user %d: %w", userID, err)
	}
	defer rows.Close()

	devices := make([]int64, 0, 4)
	groups := make([]int64, 0, 2)
	for rows.Next() {
		var (
			propertyType string
			propertyID   int64
		)
		if err := rows.Scan(&propertyType, &propertyID); err != nil {
			return nil, nil, fmt.Errorf("storage: scan grant of user %d: %w", userID, err)
		}
		if propertyType == string(permissions.ObjectDevice) {
			devices = append(devices, propertyID)
		} else {
			groups = append(groups, propertyID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("storage: query grants of user %d: %w", userID, err)
	}
	return devices, groups, nil
}

// inheritedDeviceIDs resolves the devices reachable through the granted groups:
// every group inside permissions.MaxGroupDepth is collected first, then the
// devices sitting in them.
func (s *Store) inheritedDeviceIDs(ctx context.Context, groupIDs []int64) ([]int64, error) {
	parents, err := s.GroupParents(ctx)
	if err != nil {
		return nil, err
	}
	return s.DevicesInGroups(ctx, permissions.Reach(groupIDs, parents))
}

// GroupParents returns every group id with its parent, zero for a root. The
// group tree is small and read once per request that needs it, which is cheaper
// than a recursive query that would have to be repeated per device.
func (s *Store) GroupParents(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, COALESCE(group_id, 0) FROM device_groups ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("storage: query group tree: %w", err)
	}
	defer rows.Close()

	parents := make(map[int64]int64)
	for rows.Next() {
		var id, parentID int64
		if err := rows.Scan(&id, &parentID); err != nil {
			return nil, fmt.Errorf("storage: scan group tree: %w", err)
		}
		parents[id] = parentID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query group tree: %w", err)
	}
	return parents, nil
}

// DevicesInGroups returns the ids of the devices sitting in any of the groups.
func (s *Store) DevicesInGroups(ctx context.Context, groupIDs []int64) ([]int64, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}

	args := make([]any, 0, len(groupIDs))
	clause := deviceIDsClause(&args, "group_id", groupIDs)
	rows, err := s.db.QueryContext(ctx,
		"SELECT id FROM devices WHERE "+clause+" ORDER BY id", args...)
	if err != nil {
		return nil, fmt.Errorf("storage: query devices in groups: %w", err)
	}
	defer rows.Close()

	deviceIDs := make([]int64, 0, len(groupIDs))
	for rows.Next() {
		var deviceID int64
		if err := rows.Scan(&deviceID); err != nil {
			return nil, fmt.Errorf("storage: scan device in group: %w", err)
		}
		deviceIDs = append(deviceIDs, deviceID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query devices in groups: %w", err)
	}
	return deviceIDs, nil
}

// DeviceGroup returns the group a device sits in, zero when it has none.
func (s *Store) DeviceGroup(ctx context.Context, deviceID int64) (int64, error) {
	var groupID sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT group_id FROM devices WHERE id = $1`, deviceID).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrDeviceNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("storage: device %d group: %w", deviceID, err)
	}
	return groupID.Int64, nil
}

// Permissions lists the stored pairs matching the arguments. A zero id means
// "no restriction"; a zero object type means "any type", which is how the
// service asks for one user's grants.
func (s *Store) Permissions(ctx context.Context, owner permissions.Object, ownerID int64, property permissions.Object, propertyID int64) ([]permissions.Permission, error) {
	args := make([]any, 0, 4)
	clauses := make([]string, 0, 4)

	addClause := func(column, value string) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	addID := func(column string, value int64) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	if owner != "" {
		addClause("owner_type", string(owner))
	}
	if ownerID != 0 {
		addID("owner_id", ownerID)
	}
	if property != "" {
		addClause("property_type", string(property))
	}
	if propertyID != 0 {
		addID("property_id", propertyID)
	}

	query := "SELECT " + permissionColumns + " FROM permissions"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY owner_type, owner_id, property_type, property_id"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: query permissions: %w", err)
	}
	defer rows.Close()

	var pairs []permissions.Permission
	for rows.Next() {
		var (
			ownerType, propertyType   string
			ownerRowID, propertyRowID int64
		)
		if err := rows.Scan(&ownerType, &ownerRowID, &propertyType, &propertyRowID); err != nil {
			return nil, fmt.Errorf("storage: scan permission: %w", err)
		}
		pair, err := permissions.New(
			permissions.Object(ownerType), ownerRowID,
			permissions.Object(propertyType), propertyRowID,
		)
		if err != nil {
			// A row that cannot be expressed as a permission was written by
			// something other than this store; surface it instead of hiding it.
			return nil, fmt.Errorf("storage: stored permission %s %d -> %s %d: %w",
				ownerType, ownerRowID, propertyType, propertyRowID, err)
		}
		pairs = append(pairs, pair)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query permissions: %w", err)
	}
	return pairs, nil
}

// AddPermission stores a pair. Repeating the call is a no-op, which keeps the
// bulk POST idempotent.
func (s *Store) AddPermission(ctx context.Context, permission permissions.Permission) error {
	if err := permission.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO permissions (owner_type, owner_id, property_type, property_id)
		 VALUES ($1, $2, $3, $4)`,
		string(permission.Owner), permission.OwnerID,
		string(permission.Property), permission.PropertyID)
	if err != nil && !s.db.dialect.isDuplicate(err) {
		return fmt.Errorf("storage: add permission %s: %w", permission, err)
	}
	s.invalidateTokenCache()
	return nil
}

// RemovePermission drops a pair. Removing a pair that does not exist is not an
// error, so a repeated DELETE stays idempotent.
func (s *Store) RemovePermission(ctx context.Context, permission permissions.Permission) error {
	if err := permission.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM permissions
		 WHERE owner_type = $1 AND owner_id = $2 AND property_type = $3 AND property_id = $4`,
		string(permission.Owner), permission.OwnerID,
		string(permission.Property), permission.PropertyID)
	if err != nil {
		return fmt.Errorf("storage: remove permission %s: %w", permission, err)
	}
	s.invalidateTokenCache()
	return nil
}

// ClearPermissions drops every pair that names the object, on either side. The
// generic table cannot use a foreign key, so the deletion hooks call this to
// keep orphan grants from outliving their object.
func (s *Store) ClearPermissions(ctx context.Context, object permissions.Object, objectID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM permissions
		 WHERE (owner_type = $1 AND owner_id = $2)
		    OR (property_type = $1 AND property_id = $2)`,
		string(object), objectID)
	if err != nil {
		return fmt.Errorf("storage: clear permissions of %s %d: %w", object, objectID, err)
	}
	s.invalidateTokenCache()
	return nil
}
