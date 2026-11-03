package storage

import (
	"context"
	"fmt"
	"time"

	"traccar-server_go/go_server/internal/model"
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
