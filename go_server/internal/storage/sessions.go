package postgres

import (
	"context"
	"fmt"
	"time"
)

// SetDeviceStatus records the connection state of one device. last_update keeps
// its meaning (the newest position time) and is deliberately not touched: a
// device that connects and sends nothing must not look like it reported.
func (s *Store) SetDeviceStatus(
	ctx context.Context, deviceID int64, status string, at time.Time,
) error {
	if deviceID == 0 || status == "" {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET status = $2 WHERE id = $1`, deviceID, status)
	if err != nil {
		return fmt.Errorf("postgres: set status of device %d: %w", deviceID, err)
	}
	return nil
}
