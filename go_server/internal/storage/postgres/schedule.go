package postgres

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
		   AND attributes->>'expirationTime' IS NOT NULL
		   AND (attributes->>'expirationTime')::timestamptz <= $1
		 ORDER BY id`, before)
	if err != nil {
		return nil, fmt.Errorf("postgres: expired users: %w", err)
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *user)
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
		return 0, fmt.Errorf("postgres: delete expired tokens: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("postgres: delete expired tokens: %w", err)
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
		return 0, fmt.Errorf("postgres: delete password resets: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("postgres: delete password resets: %w", err)
	}
	return deleted, nil
}

// DeleteOldActions trims the audit trail.
func (s *Store) DeleteOldActions(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM actions WHERE action_time < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("postgres: delete actions: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("postgres: delete actions: %w", err)
	}
	return deleted, nil
}
