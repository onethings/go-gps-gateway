package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

const (
	defaultDeviceLimit = 100
	maxDeviceLimit     = 1000

	// deviceColumns keeps the SELECT list and the scan order in sync.
	deviceColumns = `id, unique_id, name, model, protocol, group_id, attributes,
		status, last_update, created_at, phone, contact, category, disabled,
		expiration_time, calendar_id, position_id`
)

// Devices returns the devices matching the filter, most recently updated first.
// Limit is clamped to the range 1..1000 and Offset to a non-negative value.
func (s *Store) Devices(ctx context.Context, filter model.DeviceFilter) ([]model.Device, error) {
	query, args := deviceQuery(filter)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: query devices: %w", err)
	}
	defer rows.Close()

	var devices []model.Device
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, *device)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: query devices: %w", err)
	}
	return devices, nil
}

// deviceQuery builds the statement and its arguments.
func deviceQuery(filter model.DeviceFilter) (string, []any) {
	args := make([]any, 0, 4)
	clauses := make([]string, 0, 2)

	placeholder := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}

	if filter.Protocol != "" {
		clauses = append(clauses, "protocol = "+placeholder(filter.Protocol))
	}

	// uniqueId and id are exact lookups that the Java resource answers without
	// the keyword and paging parameters. The two lists are ORed together.
	lookups := make([]string, 0, 2)
	if len(filter.UniqueIDs) > 0 {
		lookups = append(lookups, textIDsClause(&args, "unique_id", filter.UniqueIDs))
	}
	if len(filter.IDs) > 0 {
		lookups = append(lookups, deviceIDsClause(&args, "id", filter.IDs))
	}
	if len(lookups) > 0 {
		clauses = append(clauses, "("+strings.Join(lookups, " OR ")+")")
	} else if keyword := strings.TrimSpace(filter.Keyword); keyword != "" {
		pattern := placeholder("%" + keyword + "%")
		clauses = append(clauses, fmt.Sprintf(
			"(name ILIKE %s OR unique_id ILIKE %s OR COALESCE(model, '') ILIKE %s)",
			pattern, pattern, pattern))
	}

	if filter.DeviceIDs != nil {
		clauses = append(clauses, deviceIDsClause(&args, "id", filter.DeviceIDs))
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultDeviceLimit
	}
	if limit > maxDeviceLimit {
		limit = maxDeviceLimit
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	var builder strings.Builder
	builder.WriteString("SELECT " + deviceColumns + " FROM devices")
	if len(clauses) > 0 {
		builder.WriteString(" WHERE ")
		builder.WriteString(strings.Join(clauses, " AND "))
	}

	args = append(args, limit, offset)
	fmt.Fprintf(&builder,
		" ORDER BY last_update DESC NULLS LAST, id DESC LIMIT $%d OFFSET $%d",
		len(args)-1, len(args))
	return builder.String(), args
}

// scanDevice reads one row in deviceColumns order.
func scanDevice(row interface{ Scan(dest ...any) error }) (*model.Device, error) {
	var (
		device         model.Device
		deviceModel    sql.NullString
		protocol       sql.NullString
		groupID        sql.NullInt64
		lastUpdate     sql.NullTime
		attributes     []byte
		phone          sql.NullString
		contact        sql.NullString
		category       sql.NullString
		expirationTime sql.NullTime
	)
	if err := row.Scan(
		&device.ID, &device.UniqueID, &device.Name, &deviceModel, &protocol,
		&groupID, &attributes, &device.Status, &lastUpdate, &device.CreatedAt,
		&phone, &contact, &category, &device.Disabled, &expirationTime,
		&device.CalendarID, &device.PositionID,
	); err != nil {
		return nil, fmt.Errorf("postgres: scan device: %w", err)
	}

	device.Model = deviceModel.String
	device.Protocol = protocol.String
	device.GroupID = groupID.Int64
	device.Phone = phone.String
	device.Contact = contact.String
	device.Category = category.String
	if lastUpdate.Valid {
		device.LastUpdate = lastUpdate.Time
	}
	if expirationTime.Valid {
		device.ExpirationTime = expirationTime.Time
	}
	device.Attributes = map[string]any{}
	if len(attributes) > 0 {
		if err := json.Unmarshal(attributes, &device.Attributes); err != nil {
			return nil, fmt.Errorf("postgres: decode device attributes: %w", err)
		}
	}
	return &device, nil
}

// Device returns one device by id.
func (s *Store) Device(ctx context.Context, deviceID int64) (*model.Device, error) {
	device, err := scanDevice(s.db.QueryRowContext(ctx,
		"SELECT "+deviceColumns+" FROM devices WHERE id = $1", deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrDeviceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: device %d: %w", deviceID, err)
	}
	return device, nil
}

// DeviceAttributes returns the attributes of every device, keyed by device id.
// The alert engine keeps this snapshot in memory so evaluating a position
// never queries the database. Rows without attributes are omitted.
func (s *Store) DeviceAttributes(ctx context.Context) (map[int64]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, attributes FROM devices`)
	if err != nil {
		return nil, fmt.Errorf("postgres: query device attributes: %w", err)
	}
	defer rows.Close()

	attributes := make(map[int64]map[string]any)
	for rows.Next() {
		var (
			deviceID int64
			encoded  []byte
		)
		if err := rows.Scan(&deviceID, &encoded); err != nil {
			return nil, fmt.Errorf("postgres: scan device attributes: %w", err)
		}
		if len(encoded) == 0 {
			continue
		}
		values := map[string]any{}
		if err := json.Unmarshal(encoded, &values); err != nil {
			return nil, fmt.Errorf("postgres: decode device attributes: %w", err)
		}
		if len(values) > 0 {
			attributes[deviceID] = values
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: query device attributes: %w", err)
	}
	return attributes, nil
}

// CreateDevice inserts a device and returns the stored row. A unique id that
// already exists returns model.ErrDuplicateDevice instead of touching the row.
func (s *Store) CreateDevice(ctx context.Context, create model.DeviceCreate) (*model.Device, error) {
	device, err := scanDevice(s.db.QueryRowContext(ctx,
		`INSERT INTO devices (unique_id, name, model, protocol, phone, contact,
			category, disabled, expiration_time, calendar_id)
		 VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''),
			NULLIF($6, ''), NULLIF($7, ''), $8, $9, $10)
		 ON CONFLICT (unique_id) DO NOTHING
		 RETURNING `+deviceColumns,
		create.UniqueID, create.Name, create.Model, create.Protocol,
		create.Phone, create.Contact, create.Category, create.Disabled,
		nullTime(create.ExpirationTime), create.CalendarID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrDuplicateDevice
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: create device %q: %w", create.UniqueID, err)
	}
	s.invalidateTokenCache()
	return device, nil
}

// UpdateDevice replaces the mutable fields of a device. A nil Attributes
// pointer keeps the stored map, so a rename does not wipe the thresholds the
// alert engine reads.
func (s *Store) UpdateDevice(ctx context.Context, deviceID int64, update model.DeviceUpdate) (*model.Device, error) {
	var encodedAttributes *string
	if update.Attributes != nil {
		encoded, err := encodeAttributes(*update.Attributes)
		if err != nil {
			return nil, err
		}
		encodedAttributes = &encoded
	}

	device, err := scanDevice(s.db.QueryRowContext(ctx,
		`UPDATE devices SET name = $2, model = NULLIF($3, ''),
			phone = NULLIF($4, ''), contact = NULLIF($5, ''),
			category = NULLIF($6, ''), disabled = $7, expiration_time = $8,
			calendar_id = $9, attributes = COALESCE($10::jsonb, attributes)
		 WHERE id = $1
		 RETURNING `+deviceColumns,
		deviceID, update.Name, update.Model, update.Phone, update.Contact,
		update.Category, update.Disabled, nullTime(update.ExpirationTime),
		update.CalendarID, encodedAttributes))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrDeviceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: update device %d: %w", deviceID, err)
	}
	s.invalidateTokenCache()
	return device, nil
}

// DeleteDevice removes a device. Positions and events cascade; the grants that
// name the device are dropped explicitly because the generic permission table
// has no foreign key.
func (s *Store) DeleteDevice(ctx context.Context, deviceID int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = $1`, deviceID)
	if err != nil {
		return fmt.Errorf("postgres: delete device %d: %w", deviceID, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres: delete device %d: %w", deviceID, err)
	}
	if affected == 0 {
		return model.ErrDeviceNotFound
	}

	if err := s.ClearPermissions(ctx, permissions.ObjectDevice, deviceID); err != nil {
		return err
	}
	return nil
}

// nullTime binds a zero time as NULL, which is how an absent expiration date is
// stored.
func nullTime(value time.Time) sql.NullTime {
	if value.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: value.UTC(), Valid: true}
}
