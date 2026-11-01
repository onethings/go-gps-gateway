package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/model"
)

var statisticsSpec = resourceSpec[model.Statistics]{
	table: "statistics",
	columns: []string{
		"capture_time", "active_users", "active_devices", "requests",
		"messages_received", "messages_stored", "mail_sent", "sms_sent",
		"geocoder_requests", "geolocation_requests", "protocols", "attributes",
	},
	values: func(statistics *model.Statistics) ([]any, error) {
		attributes, err := encodeAttributes(statistics.Attributes)
		if err != nil {
			return nil, err
		}
		protocols, err := encodeProtocols(statistics.Protocols)
		if err != nil {
			return nil, err
		}
		return []any{
			nullableTime(statistics.CaptureTime),
			statistics.ActiveUsers, statistics.ActiveDevices, statistics.Requests,
			statistics.MessagesReceived, statistics.MessagesStored,
			statistics.MailSent, statistics.SmsSent,
			statistics.GeocoderRequests, statistics.GeolocationRequests,
			protocols, attributes,
		}, nil
	},
	scan: func(row rowScanner) (*model.Statistics, error) {
		var (
			statistics model.Statistics
			capture    sql.NullTime
			protocols  []byte
			attributes []byte
		)
		if err := row.Scan(&statistics.ID, &capture,
			&statistics.ActiveUsers, &statistics.ActiveDevices, &statistics.Requests,
			&statistics.MessagesReceived, &statistics.MessagesStored,
			&statistics.MailSent, &statistics.SmsSent,
			&statistics.GeocoderRequests, &statistics.GeolocationRequests,
			&protocols, &attributes); err != nil {
			return nil, notFoundAs(fmt.Errorf("postgres: scan statistics: %w", err))
		}
		if capture.Valid {
			statistics.CaptureTime = capture.Time
		}
		decodedProtocols, err := decodeProtocols(protocols)
		if err != nil {
			return nil, fmt.Errorf("postgres: statistics %d: %w", statistics.ID, err)
		}
		statistics.Protocols = decodedProtocols
		decoded, err := decodeAttributes(attributes)
		if err != nil {
			return nil, fmt.Errorf("postgres: statistics %d: %w", statistics.ID, err)
		}
		statistics.Attributes = decoded
		return &statistics, nil
	},
	id:    func(statistics *model.Statistics) int64 { return statistics.ID },
	setID: func(statistics *model.Statistics, id int64) { statistics.ID = id },
}

// CreateStatistics inserts a snapshot and fills its ID.
func (s *Store) CreateStatistics(ctx context.Context, statistics *model.Statistics) error {
	return createResource(ctx, s.db, statisticsSpec, statistics)
}

// UpdateStatistics replaces the columns of a snapshot. Snapshots are normally
// written once, but keeping update and delete makes the CRUD contract uniform.
func (s *Store) UpdateStatistics(ctx context.Context, statistics *model.Statistics) error {
	return updateResource(ctx, s.db, statisticsSpec, statistics)
}

// DeleteStatistics removes a snapshot.
func (s *Store) DeleteStatistics(ctx context.Context, statisticsID int64) error {
	return deleteResource(ctx, s.db, statisticsSpec.table, statisticsID)
}

// Statistics returns the snapshots matching the filter, oldest id first.
func (s *Store) Statistics(ctx context.Context, filter model.ResourceFilter) ([]*model.Statistics, error) {
	return listResources(ctx, s.db, statisticsSpec, filter)
}

// QueryStatistics returns the snapshots captured between from and to, oldest
// first. It backs GET /api/statistics, which the Java server orders by capture
// time. A zero bound leaves that side unrestricted.
func (s *Store) QueryStatistics(
	ctx context.Context, from, to time.Time,
) ([]*model.Statistics, error) {
	columns := "id, " + strings.Join(statisticsSpec.columns, ", ")
	query := "SELECT " + columns + " FROM statistics"
	args := make([]any, 0, 2)
	switch {
	case !from.IsZero() && !to.IsZero():
		args = append(args, from, to)
		query += " WHERE capture_time BETWEEN $1 AND $2"
	case !from.IsZero():
		args = append(args, from)
		query += " WHERE capture_time >= $1"
	case !to.IsZero():
		args = append(args, to)
		query += " WHERE capture_time <= $1"
	}
	query += " ORDER BY capture_time, id"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: query statistics: %w", err)
	}
	defer rows.Close()

	var snapshots []*model.Statistics
	for rows.Next() {
		snapshot, err := statisticsSpec.scan(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: query statistics: %w", err)
	}
	return snapshots, nil
}

// PositionCounts counts the positions of every given device in one time range.
// Device ids without a position are absent from the result, which the caller
// renders as zero.
func (s *Store) PositionCounts(
	ctx context.Context, deviceIDs []int64, from, to time.Time,
) (map[int64]int64, error) {
	counts := make(map[int64]int64, len(deviceIDs))
	if len(deviceIDs) == 0 {
		return counts, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT device_id, count(*) FROM positions
		 WHERE device_id = ANY($1) AND device_time BETWEEN $2 AND $3
		 GROUP BY device_id`,
		deviceIDs, from, to)
	if err != nil {
		return nil, fmt.Errorf("postgres: count positions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var deviceID, count int64
		if err := rows.Scan(&deviceID, &count); err != nil {
			return nil, fmt.Errorf("postgres: scan position count: %w", err)
		}
		counts[deviceID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: count positions: %w", err)
	}
	return counts, nil
}

func encodeProtocols(protocols map[string]int) (string, error) {
	if len(protocols) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(protocols)
	if err != nil {
		return "", fmt.Errorf("postgres: encode protocols: %w", err)
	}
	return string(encoded), nil
}

func decodeProtocols(raw []byte) (map[string]int, error) {
	protocols := map[string]int{}
	if len(raw) == 0 {
		return protocols, nil
	}
	if err := json.Unmarshal(raw, &protocols); err != nil {
		return nil, fmt.Errorf("postgres: decode protocols: %w", err)
	}
	return protocols, nil
}

// nullableTime binds a zero time as NULL, which is how an unset capture time
// is stored.
func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
