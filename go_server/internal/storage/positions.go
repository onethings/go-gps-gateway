package storage

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

	// positionSelectColumns keeps the SELECT list and the scan order in sync.
	positionSelectColumns = `id, device_id, protocol, device_time, server_time, valid,
		latitude, longitude, altitude, speed, course, accuracy, attributes, geofenceids`
)

// QueryPositions returns the positions matching the filter, newest first.
// Limit is clamped to the range 1..1000 and Offset to a non-negative value.
func (s *Store) QueryPositions(ctx context.Context, filter model.PositionFilter) ([]model.Position, error) {
	query, args := positionQuery(filter)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: query positions: %w", err)
	}
	defer rows.Close()

	var positions []model.Position
	for rows.Next() {
		position, err := scanPosition(rows)
		if err != nil {
			return nil, err
		}
		positions = append(positions, *position)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query positions: %w", err)
	}
	return positions, nil
}

// LatestPositions returns the newest position of every device in deviceIDs,
// ordered by device id. A nil list means every device, which is how an admin
// connection seeds itself. This backs GET /api/socket.
func (s *Store) LatestPositions(ctx context.Context, deviceIDs []int64) ([]model.Position, error) {
	query, args := latestPositionsQuery(deviceIDs)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: query latest positions: %w", err)
	}
	defer rows.Close()

	var positions []model.Position
	for rows.Next() {
		position, err := scanPosition(rows)
		if err != nil {
			return nil, err
		}
		positions = append(positions, *position)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query latest positions: %w", err)
	}
	return positions, nil
}

// latestPositionsQuery builds the newest-row-per-device statement. A window
// function is used instead of PostgreSQL's DISTINCT ON so the same SQL runs on
// every supported database. A nil list means every device; an empty non-nil
// list matches nothing, like every other scope clause.
func latestPositionsQuery(deviceIDs []int64) (string, []any) {
	args := make([]any, 0, len(deviceIDs))
	where := ""
	if deviceIDs != nil {
		where = " WHERE " + deviceIDsClause(&args, "device_id", deviceIDs)
	}
	query := `SELECT ` + positionSelectColumns + ` FROM (
			SELECT ` + positionSelectColumns + `,
			       ROW_NUMBER() OVER (
			           PARTITION BY device_id
			           ORDER BY device_time DESC, id DESC
			       ) AS rn
			FROM positions` + where + `
		) ranked WHERE rn = 1 ORDER BY device_id`
	return query, args
}

// scanPosition reads one row in positionColumns order.
func scanPosition(row interface{ Scan(dest ...any) error }) (*model.Position, error) {
	var (
		position    model.Position
		latitude    sql.NullFloat64
		longitude   sql.NullFloat64
		altitude    sql.NullFloat64
		speed       sql.NullFloat64
		course      sql.NullFloat64
		accuracy    sql.NullFloat64
		attributes  []byte
		geofenceIDs []byte
	)
	if err := row.Scan(
		&position.ID, &position.DeviceID, &position.Protocol, &position.Time,
		&position.ServerTime, &position.Valid, &latitude, &longitude,
		&altitude, &speed, &course, &accuracy, &attributes, &geofenceIDs,
	); err != nil {
		return nil, fmt.Errorf("storage: scan position: %w", err)
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
			return nil, fmt.Errorf("storage: decode position attributes: %w", err)
		}
	}
	if len(geofenceIDs) > 0 {
		if err := json.Unmarshal(geofenceIDs, &position.GeofenceIDs); err != nil {
			return nil, fmt.Errorf("storage: decode position geofences: %w", err)
		}
	}
	return &position, nil
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
	if filter.IDs != nil {
		clauses = append(clauses, deviceIDsClause(&args, "id", filter.IDs))
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
	builder.WriteString("SELECT " + positionSelectColumns + " FROM positions")
	if len(clauses) > 0 {
		builder.WriteString(" WHERE ")
		builder.WriteString(strings.Join(clauses, " AND "))
	}

	args = append(args, limit, offset)
	order := "device_time DESC, id DESC"
	if filter.Ascending {
		order = "device_time ASC, id ASC"
	}
	fmt.Fprintf(&builder, " ORDER BY %s LIMIT $%d OFFSET $%d",
		order, len(args)-1, len(args))
	return builder.String(), args
}
