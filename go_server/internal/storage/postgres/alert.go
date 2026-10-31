package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
)

// ErrMissingAlertType is returned when an alert has no type.
var ErrMissingAlertType = errors.New("postgres: alert without type")

// alertColumns is the number of bound values per alert row.
const alertColumns = 6

const (
	defaultAlertLimit = 100
	maxAlertLimit     = 1000
)

// InsertAlerts writes a batch of alerts in a single statement. Alerts without
// a device id or type are skipped, mirroring what InsertPositions does for
// positions without a device.
func (s *Store) InsertAlerts(ctx context.Context, alerts []*model.Alert) error {
	query, args, err := alertsInsert(alerts)
	if err != nil {
		return err
	}
	if query == "" {
		return nil
	}

	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("postgres: insert alerts: %w", err)
	}
	return nil
}

// alertsInsert builds the multi row statement. Extracted so the placeholder
// ordering can be tested without a database.
func alertsInsert(alerts []*model.Alert) (string, []any, error) {
	if len(alerts) == 0 {
		return "", nil, nil
	}

	values := make([]string, 0, len(alerts))
	args := make([]any, 0, len(alerts)*alertColumns)

	for _, alert := range alerts {
		row, err := alertArgs(alert)
		if err != nil {
			if errors.Is(err, ErrMissingDevice) || errors.Is(err, ErrMissingAlertType) {
				continue
			}
			return "", nil, err
		}

		placeholders := make([]string, alertColumns)
		for column := 0; column < alertColumns; column++ {
			placeholders[column] = fmt.Sprintf("$%d", len(args)+column+1)
		}
		placeholders[alertColumns-1] += "::jsonb"

		values = append(values, "("+strings.Join(placeholders, ", ")+")")
		args = append(args, row...)
	}

	if len(values) == 0 {
		return "", nil, nil
	}

	query := `INSERT INTO alerts (
			device_id, position_id, type, alert_time, geofence_id, attributes
		) VALUES ` + strings.Join(values, ", ")
	return query, args, nil
}

// alertArgs flattens an alert into the column order of the insert.
func alertArgs(alert *model.Alert) ([]any, error) {
	if alert == nil || alert.DeviceID == 0 {
		return nil, ErrMissingDevice
	}
	if alert.Type == "" {
		return nil, ErrMissingAlertType
	}

	alertTime := alert.Time
	if alertTime.IsZero() {
		alertTime = time.Now()
	}

	attributes := alert.Attributes
	if attributes == nil {
		attributes = map[string]any{}
	}
	encoded, err := json.Marshal(attributes)
	if err != nil {
		return nil, fmt.Errorf("postgres: encode alert attributes: %w", err)
	}

	return []any{
		alert.DeviceID,
		nullableID(alert.PositionID),
		alert.Type,
		alertTime.UTC(),
		nullableID(alert.GeofenceID),
		string(encoded),
	}, nil
}

// QueryAlerts returns the alerts matching the filter, newest first. Limit is
// clamped to the range 1..1000.
func (s *Store) QueryAlerts(ctx context.Context, filter model.AlertFilter) ([]model.Alert, error) {
	query, args := alertQuery(filter)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: query alerts: %w", err)
	}
	defer rows.Close()

	var alerts []model.Alert
	for rows.Next() {
		var (
			alert      model.Alert
			attributes []byte
		)
		if err := rows.Scan(
			&alert.ID, &alert.DeviceID, &alert.PositionID, &alert.Type,
			&alert.Time, &alert.GeofenceID, &attributes,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan alert: %w", err)
		}
		if len(attributes) > 0 {
			if err := json.Unmarshal(attributes, &alert.Attributes); err != nil {
				return nil, fmt.Errorf("postgres: decode alert attributes: %w", err)
			}
		}
		alerts = append(alerts, alert)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: query alerts: %w", err)
	}
	return alerts, nil
}

// Geofences loads every geofence and parses its Traccar WKT area. A row with an
// unparseable area is skipped and reported in the returned error together with
// the geofences that did parse, so one bad row does not disable the engine.
func (s *Store) Geofences(ctx context.Context) ([]*geofence.Geofence, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, area, attributes FROM geofences ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: query geofences: %w", err)
	}
	defer rows.Close()

	var (
		geofences []*geofence.Geofence
		failures  []error
	)
	for rows.Next() {
		var (
			id         int64
			name       string
			area       string
			attributes []byte
		)
		if err := rows.Scan(&id, &name, &area, &attributes); err != nil {
			return nil, fmt.Errorf("postgres: scan geofence: %w", err)
		}

		parsed, err := geofence.Parse(area)
		if err != nil {
			failures = append(failures, fmt.Errorf("postgres: geofence %d: %w", id, err))
			continue
		}

		fence := &geofence.Geofence{ID: id, Name: name, Area: parsed}
		if len(attributes) > 0 {
			if err := json.Unmarshal(attributes, &fence.Attributes); err != nil {
				failures = append(failures, fmt.Errorf("postgres: geofence %d attributes: %w", id, err))
			}
		}
		geofences = append(geofences, fence)
	}
	if err := rows.Err(); err != nil {
		return geofences, fmt.Errorf("postgres: query geofences: %w", err)
	}
	return geofences, errors.Join(failures...)
}

func clampAlertLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultAlertLimit
	case limit > maxAlertLimit:
		return maxAlertLimit
	default:
		return limit
	}
}

// alertQuery builds the statement for a filter. Extracted so the placeholder
// ordering can be tested without a database.
func alertQuery(filter model.AlertFilter) (string, []any) {
	var (
		builder    strings.Builder
		conditions []string
		args       []any
	)

	placeholder := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}

	builder.WriteString(`SELECT id, device_id, COALESCE(position_id, 0), type, alert_time,
			COALESCE(geofence_id, 0), attributes
		FROM alerts`)

	if filter.DeviceID != 0 {
		conditions = append(conditions, "device_id = "+placeholder(filter.DeviceID))
	}
	if len(filter.Types) > 0 {
		types := make([]string, 0, len(filter.Types))
		for _, alertType := range filter.Types {
			if alertType != "" {
				types = append(types, placeholder(alertType))
			}
		}
		if len(types) > 0 {
			conditions = append(conditions, "type IN ("+strings.Join(types, ", ")+")")
		}
	}
	if !filter.From.IsZero() {
		conditions = append(conditions, "alert_time >= "+placeholder(filter.From.UTC()))
	}
	if !filter.To.IsZero() {
		conditions = append(conditions, "alert_time <= "+placeholder(filter.To.UTC()))
	}
	if len(conditions) > 0 {
		builder.WriteString(" WHERE ")
		builder.WriteString(strings.Join(conditions, " AND "))
	}

	builder.WriteString(" ORDER BY alert_time DESC, id DESC LIMIT ")
	builder.WriteString(placeholder(clampAlertLimit(filter.Limit)))
	if filter.Offset > 0 {
		builder.WriteString(" OFFSET ")
		builder.WriteString(placeholder(filter.Offset))
	}

	return builder.String(), args
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
