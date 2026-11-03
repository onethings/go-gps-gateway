package storage

import (
	"context"
	"fmt"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// RecordAction appends one audit row. An action with a zero time is stamped by
// the server clock.
func (s *Store) RecordAction(ctx context.Context, action *model.Action) error {
	if action == nil || action.ActionType == "" {
		return nil
	}
	at := action.ActionTime
	if at.IsZero() {
		at = time.Now()
	}
	attributes, err := encodeAttributes(action.Attributes)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO actions (action_time, address, user_id, action_type, object_type, object_id, attributes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		at, action.Address, action.UserID, action.ActionType, action.ObjectType, action.ObjectID,
		attributes,
	); err != nil {
		return fmt.Errorf("storage: record action %s: %w", action.ActionType, err)
	}
	return nil
}

// QueryActions returns the actions between from and to, oldest first, with the
// account email resolved. A zero from or to leaves that side unbounded; both
// zero returns every row up to the limit.
func (s *Store) QueryActions(
	ctx context.Context, from, to time.Time, limit, offset int,
) ([]model.Action, error) {
	if limit <= 0 || limit > maxResourceLimit {
		limit = maxResourceLimit
	}
	if offset < 0 {
		offset = 0
	}

	query := `SELECT a.id, a.action_time, a.address, a.user_id, a.action_type,
	                 a.object_type, a.object_id, COALESCE(u.email, ''), a.attributes
	          FROM actions a
	          LEFT JOIN users u ON u.id = a.user_id`
	args := make([]any, 0, 4)
	switch {
	case !from.IsZero() && !to.IsZero():
		args = append(args, from, to)
		query += fmt.Sprintf(" WHERE a.action_time BETWEEN $%d AND $%d", len(args)-1, len(args))
	case !from.IsZero():
		args = append(args, from)
		query += fmt.Sprintf(" WHERE a.action_time >= $%d", len(args))
	case !to.IsZero():
		args = append(args, to)
		query += fmt.Sprintf(" WHERE a.action_time <= $%d", len(args))
	}
	args = append(args, limit, offset)
	query += fmt.Sprintf(" ORDER BY a.action_time, a.id LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: query actions: %w", err)
	}
	defer rows.Close()

	var actions []model.Action
	for rows.Next() {
		var (
			action     model.Action
			attributes []byte
		)
		if err := rows.Scan(&action.ID, &action.ActionTime, &action.Address, &action.UserID,
			&action.ActionType, &action.ObjectType, &action.ObjectID, &action.UserEmail,
			&attributes); err != nil {
			return nil, fmt.Errorf("storage: scan action: %w", err)
		}
		decoded, err := decodeAttributes(attributes)
		if err != nil {
			return nil, fmt.Errorf("storage: action %d: %w", action.ID, err)
		}
		action.Attributes = decoded
		actions = append(actions, action)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query actions: %w", err)
	}
	return actions, nil
}
