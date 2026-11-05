package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

// ExpiredUsers returns the enabled users whose expirationTime attribute is in
// the past. The attribute is the Java server's spelling: an ISO 8601 timestamp.
func (s *Store) ExpiredUsers(ctx context.Context, before time.Time) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userSelectColumns+`
		 FROM users u
		 WHERE disabled = false
		 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("storage: expired users: %w", err)
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		if expires, ok := expirationTimeOf(user.Attributes); ok && !expires.After(before) {
			users = append(users, *user)
		}
	}
	return users, rows.Err()
}

// ExpiredTemporaryUsers returns the temporary accounts whose expirationTime
// attribute is in the past. A temporary account is the password-less one a share
// link created, so it owns nothing but its grant and token and the scheduled
// cleanup can delete it outright.
func (s *Store) ExpiredTemporaryUsers(ctx context.Context, before time.Time) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userSelectColumns+`
		 FROM users u
		 WHERE temporary = true
		 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("storage: expired temporary users: %w", err)
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		if expires, ok := expirationTimeOf(user.Attributes); ok && !expires.After(before) {
			users = append(users, *user)
		}
	}
	return users, rows.Err()
}

// SetUserDisabled flips users.disabled. It goes through the same update path the
// API uses, so an attribute rewrite cannot clobber the row.
func (s *Store) SetUserDisabled(ctx context.Context, userID int64, disabled bool) error {
	_, err := s.UpdateUser(ctx, userID, model.UserUpdate{Disabled: &disabled})
	return err
}

// DeleteExpiredTokens removes the API tokens whose expiry has passed and returns
// how many rows went away.
func (s *Store) DeleteExpiredTokens(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM api_tokens WHERE expires_at IS NOT NULL AND expires_at <= $1`, before)
	if err != nil {
		return 0, fmt.Errorf("storage: delete expired tokens: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("storage: delete expired tokens: %w", err)
	}
	if deleted > 0 {
		s.invalidateTokenCache()
	}
	return deleted, nil
}

// DeleteExpiredPasswordResets removes the reset codes that expired or were
// already used.
func (s *Store) DeleteExpiredPasswordResets(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM password_resets
		 WHERE expires_at <= $1 OR used_at IS NOT NULL`, before)
	if err != nil {
		return 0, fmt.Errorf("storage: delete password resets: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("storage: delete password resets: %w", err)
	}
	return deleted, nil
}

// DeleteOldActions trims the audit trail.
func (s *Store) DeleteOldActions(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM actions WHERE action_time < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("storage: delete actions: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("storage: delete actions: %w", err)
	}
	return deleted, nil
}

// expirationTimeOf reads the Java style expirationTime account attribute. It is
// stored as an ISO 8601 string; an attribute written by a test may already be a
// time.Time.
func expirationTimeOf(attributes model.Attributes) (time.Time, bool) {
	value, ok := attributes["expirationTime"]
	if !ok {
		return time.Time{}, false
	}
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), true
	case string:
		parsed, err := time.Parse(time.RFC3339, typed)
		if err != nil {
			return time.Time{}, false
		}
		return parsed.UTC(), true
	default:
		return time.Time{}, false
	}
}

// UsersExpiringBetween returns the accounts whose expirationTime attribute
// falls in (from, to]. The window is how the expiration notices avoid sending
// the same message twice, so a run that is late loses a notice instead of
// repeating one.
//
// Disabled accounts are not filtered out: the task that disables an expired
// account also runs hourly, and whichever of the two runs first must not decide
// whether the notice goes out.
func (s *Store) UsersExpiringBetween(ctx context.Context, from, to time.Time) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userSelectColumns+`
		 FROM users u
		 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("storage: users expiring: %w", err)
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		if expires, ok := expirationTimeOf(user.Attributes); ok &&
			expires.After(from) && !expires.After(to) {
			users = append(users, *user)
		}
	}
	return users, rows.Err()
}

// DevicesExpiringBetween returns the devices whose expiration_time falls in
// (from, to].
func (s *Store) DevicesExpiringBetween(ctx context.Context, from, to time.Time) ([]model.Device, error) {
	// The window is applied in Go because each dialect stores the column
	// differently; only devices that expire at all are read, which keeps the
	// scan small.
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+deviceColumns+` FROM devices
		 WHERE expiration_time IS NOT NULL
		 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("storage: devices expiring: %w", err)
	}
	defer rows.Close()

	var devices []model.Device
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		if !device.ExpirationTime.IsZero() &&
			device.ExpirationTime.After(from) && !device.ExpirationTime.After(to) {
			devices = append(devices, *device)
		}
	}
	return devices, rows.Err()
}

// DeviceUsers returns the accounts that hold a grant on the device, either
// directly or through the group tree, which is what an expiration notice needs:
// every account that would lose the device.
func (s *Store) DeviceUsers(ctx context.Context, deviceID int64) ([]model.User, error) {
	groupID, err := s.DeviceGroup(ctx, deviceID)
	if err != nil {
		return nil, err
	}

	groupIDs := []int64(nil)
	if groupID != 0 {
		parents, err := s.GroupParents(ctx)
		if err != nil {
			return nil, err
		}
		groupIDs = permissions.Ancestors(groupID, parents)
	}

	args := []any{deviceID}
	clauses := []string{"(p.property_type = 'device' AND p.property_id = $1)"}
	if len(groupIDs) > 0 {
		clauses = append(clauses,
			"(p.property_type = 'group' AND "+deviceIDsClause(&args, "p.property_id", groupIDs)+")")
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userSelectColumns+`
		 FROM users u
		 JOIN permissions p ON p.owner_type = 'user' AND p.owner_id = u.id
		 WHERE `+strings.Join(clauses, " OR ")+`
		 ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: users of device %d: %w", deviceID, err)
	}
	defer rows.Close()

	var users []model.User
	seen := make(map[int64]struct{})
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[user.ID]; duplicate {
			continue
		}
		seen[user.ID] = struct{}{}
		users = append(users, *user)
	}
	return users, rows.Err()
}
