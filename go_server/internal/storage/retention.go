package storage

import (
	"context"
	"fmt"
	"time"
)

// DeleteEventsBefore removes the events older than before and reports how many
// rows it removed. The event retention task calls it.
func (s *Store) DeleteEventsBefore(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE event_time < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("storage: delete old events: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("storage: delete old events: %w", err)
	}
	return deleted, nil
}

// DeletePositionsBefore removes the positions older than before. It is the
// history retention task, which an operator has to enable explicitly.
func (s *Store) DeletePositionsBefore(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM positions WHERE device_time < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("storage: delete old positions: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("storage: delete old positions: %w", err)
	}
	return deleted, nil
}

// DisableExpiredDevices suspends the devices whose expiration time has passed.
// A device that never had an expiration time is left alone.
func (s *Store) DisableExpiredDevices(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE devices SET disabled = true
		  WHERE expiration_time IS NOT NULL AND expiration_time < $1 AND NOT disabled`,
		before)
	if err != nil {
		return 0, fmt.Errorf("storage: disable expired devices: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("storage: disable expired devices: %w", err)
	}
	return changed, nil
}
