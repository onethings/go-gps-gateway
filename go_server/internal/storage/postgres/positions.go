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
	defaultPositionLimit = 100
	maxPositionLimit     = 1000
)

// QueryPositions returns the positions matching the filter, newest first.
// Limit is clamped to the range 1..1000 and Offset to a non-negative value.
func (s *Store) QueryPositions(ctx context.Context, filter model.PositionFilter) ([]model.Position, error) {
	query, args := positionQuery(filter)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: query positions: %w", err)
	}
	defer rows.Close()

	var positions []model.Position
	for rows.Next() {
		var (
			position   model.Position
			latitude   sql.NullFloat64
			longitude  sql.NullFloat64
			altitude   sql.NullFloat64
			speed      sql.NullFloat64
			course     sql.NullFloat64
			accuracy   sql.NullFloat64
			attributes []byte
		)
		if err := rows.Scan(
			&position.ID, &position.DeviceID, &position.Protocol, &position.Time,
			&position.ServerTime, &position.Valid, &latitude, &longitude,
			&altitude, &speed, &course, &accuracy, &attributes,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan position: %w", err)
		}

		position.Latitude = latitude.Float64
		position.Longitude = longitude.Float64
		position.Altitude = altitude.Float64
		position.Speed = speed.Float64
		position.Course = course.Float64
		position.Accuracy = accuracy.Float64

		// A nil map would serialise as null; an empty object is friendlier.
		position.Attributes = map[string]any{}
		if len(attributes) > 0 {
			if err := json.Unmarshal(attributes, &position.Attributes); err != nil {
				return nil, fmt.Errorf("postgres: decode position attributes: %w", err)
			}
		}
		positions = append(positions, position)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: query positions: %w", err)
	}
	return positions, nil
}

// positionQuery builds the statement and its arguments. The time bounds are
// compared against device_time, which is what the reports use.
func positionQuery(filter model.PositionFilter) (string, []any) {
	args := make([]any, 0, 5)
	clauses := make([]string, 0, 3)

	add := func(column, operator string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("%s %s $%d", column, operator, len(args)))
	}

	if filter.DeviceID != 0 {
		add("device_id", "=", filter.DeviceID)
	}
	if filter.DeviceIDs != nil {
		clauses = append(clauses, deviceIDsClause(&args, "device_id", filter.DeviceIDs))
	}
	if !filter.From.IsZero() {
		add("device_time", ">=", filter.From.UTC())
	}
	if !filter.To.IsZero() {
		add("device_time", "<=", filter.To.UTC())
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultPositionLimit
	}
	if limit > maxPositionLimit {
		limit = maxPositionLimit
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	var builder strings.Builder
	builder.WriteString(`SELECT id, device_id, protocol, device_time, server_time, valid,
		latitude, longitude, altitude, speed, course, accuracy, attributes
		FROM positions`)
	if len(clauses) > 0 {
		builder.WriteString(" WHERE ")
		builder.WriteString(strings.Join(clauses, " AND "))
	}

	args = append(args, limit, offset)
	fmt.Fprintf(&builder, " ORDER BY device_time DESC, id DESC LIMIT $%d OFFSET $%d",
		len(args)-1, len(args))
	return builder.String(), args
}
