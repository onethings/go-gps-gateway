package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"traccar-server_go/go_server/internal/model"
)

const (
	defaultDeviceLimit = 100
	maxDeviceLimit     = 1000

	// deviceColumns keeps the SELECT list and the scan order in sync.
	deviceColumns = `id, unique_id, name, model, protocol, attributes,
		last_update, created_at`
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
	args := make([]any, 0, 3)
	clauses := make([]string, 0, 1)

	if filter.Protocol != "" {
		args = append(args, filter.Protocol)
		clauses = append(clauses, fmt.Sprintf("protocol = $%d", len(args)))
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
		device      model.Device
		deviceModel sql.NullString
		protocol    sql.NullString
		lastUpdate  sql.NullTime
		attributes  []byte
	)
	if err := row.Scan(
		&device.ID, &device.UniqueID, &device.Name, &deviceModel, &protocol,
		&attributes, &lastUpdate, &device.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("postgres: scan device: %w", err)
	}

	device.Model = deviceModel.String
	device.Protocol = protocol.String
	if lastUpdate.Valid {
		device.LastUpdate = lastUpdate.Time
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
		`INSERT INTO devices (unique_id, name, model, protocol)
		 VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''))
		 ON CONFLICT (unique_id) DO NOTHING
		 RETURNING `+deviceColumns,
		create.UniqueID, create.Name, create.Model, create.Protocol))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrDuplicateDevice
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: create device %q: %w", create.UniqueID, err)
	}
	return device, nil
}

// UpdateDevice replaces the mutable fields of a device.
func (s *Store) UpdateDevice(ctx context.Context, deviceID int64, update model.DeviceUpdate) (*model.Device, error) {
	device, err := scanDevice(s.db.QueryRowContext(ctx,
		`UPDATE devices SET name = $2, model = NULLIF($3, '') WHERE id = $1
		 RETURNING `+deviceColumns,
		deviceID, update.Name, update.Model))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrDeviceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: update device %d: %w", deviceID, err)
	}
	return device, nil
}

// DeleteDevice removes a device. Positions and alerts cascade.
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
	return nil
}
