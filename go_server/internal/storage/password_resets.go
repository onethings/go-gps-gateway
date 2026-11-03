package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// passwordResetResendInterval suppresses a second reset email for the same
// account within this window. The API answers the same way either way, so the
// throttle is invisible to the caller.
const passwordResetResendInterval = time.Minute

// CreatePasswordReset stores a new reset code for an account and invalidates
// every unused code it already had. model.ErrResetTooSoon reports that the
// previous code is younger than passwordResetResendInterval.
func (s *Store) CreatePasswordReset(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: create password reset: %w", err)
	}
	defer tx.Rollback()

	var recent bool
	err = tx.QueryRowContext(ctx,
		`SELECT EXISTS (
		     SELECT 1 FROM password_resets
		     WHERE user_id = $1 AND used_at IS NULL
		       AND created_at > now() - $2::double precision * interval '1 second'
		 )`,
		userID, passwordResetResendInterval.Seconds(),
	).Scan(&recent)
	if err != nil {
		return fmt.Errorf("postgres: check recent password reset of user %d: %w", userID, err)
	}
	if recent {
		return model.ErrResetTooSoon
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM password_resets WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("postgres: drop password resets of user %d: %w", userID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO password_resets (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt); err != nil {
		return fmt.Errorf("postgres: create password reset of user %d: %w", userID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("postgres: create password reset of user %d: %w", userID, err)
	}
	return nil
}

// ConsumePasswordReset marks an unused, unexpired code as used and returns the
// account it belongs to. A repeated call with the same code reports
// model.ErrResetTokenInvalid, which is what makes a code single use.
func (s *Store) ConsumePasswordReset(ctx context.Context, tokenHash string) (int64, error) {
	var userID int64
	err := s.db.QueryRowContext(ctx,
		`UPDATE password_resets SET used_at = now()
		 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		 RETURNING user_id`,
		tokenHash,
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrResetTokenInvalid
	}
	if err != nil {
		return 0, fmt.Errorf("postgres: consume password reset: %w", err)
	}
	return userID, nil
}

// DeletePasswordResets removes every reset code of an account. A password
// change calls it so an outstanding email stops working.
func (s *Store) DeletePasswordResets(ctx context.Context, userID int64) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM password_resets WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("postgres: delete password resets of user %d: %w", userID, err)
	}
	return nil
}

// DeletePasswordReset removes one code by hash, which is what a failed delivery
// needs: only that code is known to be undelivered.
func (s *Store) DeletePasswordReset(ctx context.Context, tokenHash string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM password_resets WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("postgres: delete password reset: %w", err)
	}
	return nil
}
