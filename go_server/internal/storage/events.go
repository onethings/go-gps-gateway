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
)

// ErrMissingEventType is returned when an event has no type.
var ErrMissingEventType = errors.New("postgres: event without type")

// eventColumns is the number of bound values per event row.
const eventColumns = 6

const (
	defaultEventLimit = 100
	maxEventLimit     = 1000
)

// execer is what *sql.DB and *sql.Tx have in common, so the statements below can
// run inside the report transaction or on their own.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// InsertEvents writes a batch of events in a single statement and fills in the
// ids. Events without a device id or type are skipped, mirroring what
// InsertPositions does for positions without a device.
func (s *Store) InsertEvents(ctx context.Context, events []*model.Event) error {
	return insertEventsWith(ctx, s.db, events)
}

func insertEventsWith(ctx context.Context, db execer, events []*model.Event) error {
	query, args, err := eventsInsert(events)
	if err != nil {
		return err
	}
	if query == "" {
		return nil
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("postgres: insert events: %w", err)
	}
	defer rows.Close()

	index := 0
	for rows.Next() {
		if index >= len(events) {
			break
		}
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("postgres: scan event id: %w", err)
		}
		events[index].ID = id
		index++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: insert events: %w", err)
	}
	return nil
}

// eventsInsert builds the multi row statement. Extracted so the placeholder
// ordering can be tested without a database.
func eventsInsert(events []*model.Event) (string, []any, error) {
	if len(events) == 0 {
		return "", nil, nil
	}

	values := make([]string, 0, len(events))
	args := make([]any, 0, len(events)*eventColumns)

	for _, event := range events {
		row, err := eventArgs(event)
		if err != nil {
			if errors.Is(err, ErrMissingDevice) || errors.Is(err, ErrMissingEventType) {
				continue
			}
			return "", nil, err
		}

		placeholders := make([]string, eventColumns)
		for column := 0; column < eventColumns; column++ {
			placeholders[column] = fmt.Sprintf("$%d", len(args)+column+1)
		}
		placeholders[eventColumns-1] += "::jsonb"

		values = append(values, "("+strings.Join(placeholders, ", ")+")")
		args = append(args, row...)
	}

	if len(values) == 0 {
		return "", nil, nil
	}

	query := `INSERT INTO events (
			device_id, position_id, geofence_id, type, event_time, attributes
		) VALUES ` + strings.Join(values, ", ") + ` RETURNING id`
	return query, args, nil
}

// eventArgs flattens an event into the column order of the insert.
func eventArgs(event *model.Event) ([]any, error) {
	if event == nil || event.DeviceID == 0 {
		return nil, ErrMissingDevice
	}
	if event.Type == "" {
		return nil, ErrMissingEventType
	}

	eventTime := event.Time
	if eventTime.IsZero() {
		eventTime = time.Now()
	}

	attributes := event.Attributes
	if attributes == nil {
		attributes = map[string]any{}
	}
	encoded, err := json.Marshal(attributes)
	if err != nil {
		return nil, fmt.Errorf("postgres: encode event attributes: %w", err)
	}

	return []any{
		event.DeviceID,
		nullableID(event.PositionID),
		nullableID(event.GeofenceID),
		event.Type,
		eventTime.UTC(),
		string(encoded),
	}, nil
}

// InsertReports writes every position together with the events it raised in one
// transaction, so each event carries the id of its position. This is what the
// alerts table could not do: its position_id stayed NULL because the alert was
// written before the position it belonged to.
func (s *Store) InsertReports(ctx context.Context, reports []*model.PositionReport) error {
	positions := make([]*model.Position, 0, len(reports))
	eventsByPosition := make(map[*model.Position][]*model.Event, len(reports))
	totalEvents := 0

	for _, report := range reports {
		if report == nil || report.Position == nil || report.Position.DeviceID == 0 {
			continue
		}
		positions = append(positions, report.Position)
		eventsByPosition[report.Position] = report.Events
		totalEvents += len(report.Events)
	}
	if len(positions) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: begin report: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	ids, touched, err := insertPositionsReturning(ctx, tx, positions)
	if err != nil {
		return err
	}

	linkReportIDs(positions, eventsByPosition, ids)

	if totalEvents > 0 {
		events := make([]*model.Event, 0, totalEvents)
		for _, position := range positions {
			events = append(events, eventsByPosition[position]...)
		}
		if err := insertEventsWith(ctx, tx, events); err != nil {
			return err
		}
	}

	if err := updateLastUpdateWith(ctx, tx, touched); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("postgres: commit report: %w", err)
	}
	committed = true
	return nil
}

// nullableID turns a zero id into NULL, which is what the optional foreign keys
// need.
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// linkReportIDs puts the id the database returned on every position and on the
// events that position raised. It is the step that removes the old limitation:
// an alert row written before its position existed could never carry the id.
func linkReportIDs(
	positions []*model.Position,
	eventsByPosition map[*model.Position][]*model.Event,
	ids positionIDs,
) {
	for _, position := range positions {
		position.ID = ids.take(positionKeyOf(position))
		for _, event := range eventsByPosition[position] {
			event.PositionID = position.ID
		}
	}
}

// positionKey identifies a position by the two columns that make it unique in a
// batch. It is how the ids returned by RETURNING are matched back to the
// positions that were written.
type positionKey struct {
	deviceID int64
	at       time.Time
}

func positionKeyOf(position *model.Position) positionKey {
	return positionKey{deviceID: position.DeviceID, at: deviceTimeOf(position).UTC()}
}

// positionIDs hands out the ids of one batch. A key can repeat - the same device
// can report two positions with one device time - so the ids are kept in the
// order the database returned them.
type positionIDs map[positionKey][]int64

func (ids positionIDs) take(key positionKey) int64 {
	list := ids[key]
	if len(list) == 0 {
		return 0
	}
	ids[key] = list[1:]
	return list[0]
}

// insertPositionsReturning writes the positions and returns their ids, keyed by
// device and device time, together with the newest device time per device.
func insertPositionsReturning(
	ctx context.Context, db execer, positions []*model.Position,
) (positionIDs, map[int64]time.Time, error) {
	values := make([]string, 0, len(positions))
	args := make([]any, 0, len(positions)*positionColumns)
	touched := make(map[int64]time.Time, 4)

	for _, position := range positions {
		row, err := positionArgs(position)
		if err != nil {
			return nil, nil, err
		}

		deviceTime := deviceTimeOf(position)
		if previous, ok := touched[position.DeviceID]; !ok || deviceTime.After(previous) {
			touched[position.DeviceID] = deviceTime
		}

		placeholders := make([]string, positionColumns)
		for column := 0; column < positionColumns; column++ {
			placeholders[column] = fmt.Sprintf("$%d", len(args)+column+1)
		}
		placeholders[positionColumns-2] += "::jsonb"
		placeholders[positionColumns-1] += "::jsonb"

		values = append(values, "("+strings.Join(placeholders, ", ")+")")
		args = append(args, row...)
	}

	if len(values) == 0 {
		return nil, touched, nil
	}

	query := `INSERT INTO positions (
			device_id, protocol, device_time, valid, latitude, longitude,
			altitude, speed, course, accuracy, geofenceids, attributes
		) VALUES ` + strings.Join(values, ", ") + `
		RETURNING id, device_id, device_time`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: insert positions: %w", err)
	}
	defer rows.Close()

	ids := make(positionIDs, len(positions))
	for rows.Next() {
		var (
			id       int64
			deviceID int64
			at       time.Time
		)
		if err := rows.Scan(&id, &deviceID, &at); err != nil {
			return nil, nil, fmt.Errorf("postgres: scan position id: %w", err)
		}
		key := positionKey{deviceID: deviceID, at: at.UTC()}
		ids[key] = append(ids[key], id)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("postgres: insert positions: %w", err)
	}
	return ids, touched, nil
}

// QueryEvents returns the events matching the filter, newest first. Limit is
// clamped to the range 1..1000.
func (s *Store) QueryEvents(ctx context.Context, filter model.EventFilter) ([]model.Event, error) {
	query, args := eventQuery(filter)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: query events: %w", err)
	}
	defer rows.Close()

	var events []model.Event
	for rows.Next() {
		var (
			event      model.Event
			attributes []byte
		)
		if err := rows.Scan(
			&event.ID, &event.DeviceID, &event.PositionID, &event.Type,
			&event.Time, &event.GeofenceID, &attributes,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan event: %w", err)
		}
		event.Attributes = map[string]any{}
		if len(attributes) > 0 {
			if err := json.Unmarshal(attributes, &event.Attributes); err != nil {
				return nil, fmt.Errorf("postgres: decode event attributes: %w", err)
			}
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: query events: %w", err)
	}
	return events, nil
}

func clampEventLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultEventLimit
	case limit > maxEventLimit:
		return maxEventLimit
	default:
		return limit
	}
}

// eventQuery builds the statement for a filter. Extracted so the placeholder
// ordering can be tested without a database.
func eventQuery(filter model.EventFilter) (string, []any) {
	var (
		builder    strings.Builder
		conditions []string
		args       []any
	)

	placeholder := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}

	builder.WriteString(`SELECT id, device_id, COALESCE(position_id, 0), type, event_time,
			COALESCE(geofence_id, 0), attributes
		FROM events`)

	if filter.ID != 0 {
		conditions = append(conditions, "id = "+placeholder(filter.ID))
	}
	if filter.DeviceID != 0 {
		conditions = append(conditions, "device_id = "+placeholder(filter.DeviceID))
	}
	if filter.DeviceIDs != nil {
		conditions = append(conditions, deviceIDsClause(&args, "device_id", filter.DeviceIDs))
	}
	if len(filter.Types) > 0 {
		types := make([]string, 0, len(filter.Types))
		for _, eventType := range filter.Types {
			if eventType != "" {
				types = append(types, placeholder(eventType))
			}
		}
		if len(types) > 0 {
			conditions = append(conditions, "type IN ("+strings.Join(types, ", ")+")")
		}
	}
	if !filter.From.IsZero() {
		conditions = append(conditions, "event_time >= "+placeholder(filter.From.UTC()))
	}
	if !filter.To.IsZero() {
		conditions = append(conditions, "event_time <= "+placeholder(filter.To.UTC()))
	}
	if len(conditions) > 0 {
		builder.WriteString(" WHERE ")
		builder.WriteString(strings.Join(conditions, " AND "))
	}

	builder.WriteString(" ORDER BY event_time DESC, id DESC LIMIT ")
	builder.WriteString(placeholder(clampEventLimit(filter.Limit)))
	if filter.Offset > 0 {
		builder.WriteString(" OFFSET ")
		builder.WriteString(placeholder(filter.Offset))
	}

	return builder.String(), args
}
