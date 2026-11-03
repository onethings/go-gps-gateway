package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"traccar-server_go/go_server/internal/model"
)

// notificationColumns is the SELECT list shared by the notification queries.
// notificators is a JSON column, so every dialect returns the same text.
const (
	notificationColumns = `id, type, notificators, description,
		always, command_id, calendar_id, attributes, created_at`
	// notificationJoinedColumns is the same list qualified for joins.
	notificationJoinedColumns = `n.id, n.type, n.notificators, n.description,
		n.always, n.command_id, n.calendar_id, n.attributes, n.created_at`
)

// Notifications lists every subscription preset, oldest first.
func (s *Store) Notifications(ctx context.Context) ([]model.Notification, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+notificationColumns+" FROM notifications ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("storage: query notifications: %w", err)
	}
	defer rows.Close()

	var notifications []model.Notification
	for rows.Next() {
		notification, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		notifications = append(notifications, *notification)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query notifications: %w", err)
	}
	return notifications, nil
}

// Notification returns one subscription preset by id.
func (s *Store) Notification(ctx context.Context, notificationID int64) (*model.Notification, error) {
	notification, err := scanNotification(s.db.QueryRowContext(ctx,
		"SELECT "+notificationColumns+" FROM notifications WHERE id = $1",
		notificationID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotificationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("storage: query notification %d: %w", notificationID, err)
	}
	return notification, nil
}

// CreateNotification inserts a subscription preset.
func (s *Store) CreateNotification(ctx context.Context, create model.NotificationCreate) (*model.Notification, error) {
	notificators, err := encodeNotificators(create.Notificators)
	if err != nil {
		return nil, err
	}

	attributes, err := encodeAttributes(create.Attributes)
	if err != nil {
		return nil, err
	}

	id, err := insertReturningID(ctx, s.db,
		`INSERT INTO notifications (type, notificators, description, always, command_id, calendar_id, attributes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		create.Type, notificators, create.Description, create.Always, create.CommandID,
		nullableID(create.CalendarID), attributes)
	if err != nil {
		return nil, fmt.Errorf("storage: create notification: %w", err)
	}
	return s.Notification(ctx, id)
}

// UpdateNotification replaces the non-nil fields of a preset.
func (s *Store) UpdateNotification(ctx context.Context, notificationID int64, update model.NotificationUpdate) (*model.Notification, error) {
	var encoded *string
	if update.Notificators != nil {
		value, err := encodeNotificators(*update.Notificators)
		if err != nil {
			return nil, err
		}
		encoded = &value
	}
	var attributes *string
	if update.Attributes != nil {
		value, err := encodeAttributes(*update.Attributes)
		if err != nil {
			return nil, err
		}
		attributes = &value
	}

	assignments := make([]string, 0, 8)
	args := []any{notificationID}
	add := func(column string, value any) {
		args = append(args, value)
		assignments = append(assignments, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	if update.Type != nil {
		add("type", *update.Type)
	}
	if encoded != nil {
		add("notificators", *encoded)
	}
	if update.Description != nil {
		add("description", *update.Description)
	}
	if update.Always != nil {
		add("always", *update.Always)
	}
	if update.CommandID != nil {
		add("command_id", *update.CommandID)
	}
	if update.CalendarID != nil {
		add("calendar_id", nullableID(*update.CalendarID))
	}
	if attributes != nil {
		add("attributes", *attributes)
	}
	if len(assignments) == 0 {
		return s.Notification(ctx, notificationID)
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE notifications SET `+strings.Join(assignments, ", ")+` WHERE id = $1`, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: update notification %d: %w", notificationID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("storage: update notification %d: %w", notificationID, err)
	}
	if affected == 0 {
		return nil, model.ErrNotificationNotFound
	}
	return s.Notification(ctx, notificationID)
}

// DeleteNotification removes a preset. Subscriptions cascade.
func (s *Store) DeleteNotification(ctx context.Context, notificationID int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM notifications WHERE id = $1`, notificationID)
	if err != nil {
		return fmt.Errorf("storage: delete notification %d: %w", notificationID, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: delete notification %d: %w", notificationID, err)
	}
	if affected == 0 {
		return model.ErrNotificationNotFound
	}
	return nil
}

// UserNotifications lists the presets a user subscribed to, oldest first.
func (s *Store) UserNotifications(ctx context.Context, userID int64) ([]model.Notification, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+notificationJoinedColumns+`
		 FROM notifications n
		 JOIN user_notifications un ON un.notification_id = n.id
		 WHERE un.user_id = $1
		 ORDER BY n.id`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("storage: query user %d notifications: %w", userID, err)
	}
	defer rows.Close()

	var notifications []model.Notification
	for rows.Next() {
		notification, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		notifications = append(notifications, *notification)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query user %d notifications: %w", userID, err)
	}
	return notifications, nil
}

// AssignNotification subscribes a user to a preset. Repeating the call is a
// no-op.
func (s *Store) AssignNotification(ctx context.Context, userID, notificationID int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_notifications (user_id, notification_id) VALUES ($1, $2)`,
		userID, notificationID)
	if err != nil && !s.db.dialect.isDuplicate(err) {
		return fmt.Errorf("storage: assign notification %d to user %d: %w", notificationID, userID, err)
	}
	return nil
}

// UnassignNotification removes a subscription. Removing one that does not
// exist is not an error, so a repeated DELETE stays idempotent.
func (s *Store) UnassignNotification(ctx context.Context, userID, notificationID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM user_notifications WHERE user_id = $1 AND notification_id = $2`,
		userID, notificationID)
	if err != nil {
		return fmt.Errorf("storage: unassign notification %d from user %d: %w", notificationID, userID, err)
	}
	return nil
}

// DeviceSubscriptions resolves the active subscriptions that apply to one
// alert on a device. A preset applies to the device when it is marked always or
// linked to the device through a device -> notification permission pair, and it
// fires when its type matches the alert or is the "all"/"*" wildcard. One row
// comes back per preset, so the dispatcher can apply the per-preset filters
// (alarms, geofenceIds) and still send each recipient once.
func (s *Store) DeviceSubscriptions(ctx context.Context, deviceID int64, alertType string) ([]model.NotificationSubscription, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT u.id, u.email, n.notificators, u.attributes,
		        n.id, n.type, n.always, n.attributes
		 FROM notifications n
		 JOIN user_notifications un ON un.notification_id = n.id
		 JOIN users u ON u.id = un.user_id
		 JOIN permissions p ON p.owner_type = 'user' AND p.owner_id = u.id
		                   AND p.property_type = 'device'
		 WHERE p.property_id = $1
		   AND NOT u.disabled
		   AND (n.type = $2 OR n.type = 'all' OR n.type = '*')
		   AND (n.always OR EXISTS (
		           SELECT 1 FROM permissions pn
		           WHERE pn.owner_type = 'device' AND pn.owner_id = $1
		             AND pn.property_type = 'notification' AND pn.property_id = n.id))
		 ORDER BY u.id, n.id`,
		deviceID, alertType)
	if err != nil {
		return nil, fmt.Errorf("storage: query device %d subscriptions: %w", deviceID, err)
	}
	defer rows.Close()

	var subscriptions []model.NotificationSubscription
	for rows.Next() {
		var (
			userID           int64
			email            string
			notificatorsJSON []byte
			attributesJSON   []byte
			notificationJSON []byte
		)
		var (
			subscription model.NotificationSubscription
			always       bool
		)
		if err := rows.Scan(&userID, &email, &notificatorsJSON, &attributesJSON,
			&subscription.NotificationID, &subscription.NotificationType, &always,
			&notificationJSON); err != nil {
			return nil, fmt.Errorf("storage: scan device %d subscription: %w", deviceID, err)
		}

		var notificators []string
		if err := json.Unmarshal(notificatorsJSON, &notificators); err != nil {
			return nil, fmt.Errorf("storage: decode notification channels: %w", err)
		}
		attributes, err := decodeAttributes(attributesJSON)
		if err != nil {
			return nil, fmt.Errorf("storage: user %d subscription: %w", userID, err)
		}
		notificationAttributes, err := decodeAttributes(notificationJSON)
		if err != nil {
			return nil, fmt.Errorf("storage: notification %d: %w", subscription.NotificationID, err)
		}

		subscription.UserID = userID
		subscription.Email = email
		subscription.Notificators = notificators
		subscription.Attributes = attributes
		subscription.Always = always
		subscription.NotificationAttributes = notificationAttributes
		subscriptions = append(subscriptions, subscription)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query device %d subscriptions: %w", deviceID, err)
	}
	return subscriptions, nil
}

// scanNotification reads one row in notificationColumns order.
func scanNotification(row interface{ Scan(dest ...any) error }) (*model.Notification, error) {
	var (
		notification     model.Notification
		notificatorsJSON []byte
		calendarID       sql.NullInt64
		attributesJSON   []byte
	)
	if err := row.Scan(&notification.ID, &notification.Type, &notificatorsJSON,
		&notification.Description, &notification.Always, &notification.CommandID,
		&calendarID, &attributesJSON, &notification.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("storage: scan notification: %w", err)
	}
	if calendarID.Valid {
		notification.CalendarID = calendarID.Int64
	}

	if err := json.Unmarshal(notificatorsJSON, &notification.Notificators); err != nil {
		return nil, fmt.Errorf("storage: decode notification channels: %w", err)
	}
	if notification.Notificators == nil {
		notification.Notificators = []string{}
	}
	attributes, err := decodeAttributes(attributesJSON)
	if err != nil {
		return nil, fmt.Errorf("storage: notification %d: %w", notification.ID, err)
	}
	notification.Attributes = attributes
	return &notification, nil
}

// encodeNotificators renders a channel list as a JSON array. An empty slice is
// encoded as [], which is what the array constructor expects.
func encodeNotificators(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("storage: encode notification channels: %w", err)
	}
	return string(encoded), nil
}

// appendUniqueStrings appends values that are not already present.
func appendUniqueStrings(existing []string, values ...string) []string {
	seen := make(map[string]struct{}, len(existing)+len(values))
	for _, value := range existing {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		existing = append(existing, value)
	}
	return existing
}
