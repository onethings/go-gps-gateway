package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"traccar-server_go/go_server/internal/model"
)

const (
	defaultDeviceLimit = 100
	maxDeviceLimit     = 1000
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
		var (
			device      model.Device
			deviceModel sql.NullString
			protocol    sql.NullString
			lastUpdate  sql.NullTime
			attributes  []byte
		)
		if err := rows.Scan(
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
		devices = append(devices, device)
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
	builder.WriteString(`SELECT id, unique_id, name, model, protocol, attributes,
		last_update, created_at
		FROM devices`)
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
