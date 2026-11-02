package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
)

const (
	defaultCommandLimit = 100
	maxCommandLimit     = 1000

	// pendingCommandLimit caps how many undelivered commands one device can
	// recover when it reconnects.
	pendingCommandLimit = 100

	// commandColumns keeps the SELECT list and the scan order in sync.
	commandColumns = `id, device_id, type, attributes, status, result_code,
		serial, attempts, sent_at, done_at, created_at`
)

// openCommandStatuses are the states a command can still move out of: recorded
// but not written, or written and waiting for the device.
var openCommandStatuses = []string{
	model.CommandStatusPending,
	model.CommandStatusSent,
}

// ErrMissingCommand is returned when a command cannot be recorded because it
// is nil or carries no device id.
var ErrMissingCommand = errors.New("postgres: command without device id")

// ErrInvalidRetention is returned for a retention age that would delete the
// whole table, which is never what the caller meant.
var ErrInvalidRetention = errors.New("postgres: retention age must be positive")

// CreateCommand inserts a pending row and stores its id on the command.
func (s *Store) CreateCommand(ctx context.Context, command *model.Command) error {
	if command == nil {
		return ErrMissingCommand
	}
	if command.DeviceID == 0 {
		return ErrMissingCommand
	}

	attributes := command.Attributes
	if attributes == nil {
		attributes = map[string]any{}
	}
	encoded, err := json.Marshal(attributes)
	if err != nil {
		return fmt.Errorf("postgres: encode command attributes: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `INSERT INTO commands (device_id, type, attributes, serial)
		VALUES ($1, $2, $3, NULLIF($4, 0)) RETURNING id`,
		command.DeviceID, command.Type, string(encoded), command.Serial,
	).Scan(&command.ID); err != nil {
		return fmt.Errorf("postgres: insert command: %w", err)
	}

	command.Status = model.CommandStatusPending
	return nil
}

// MarkCommandSent records that the payload reached the device socket and counts
// the write. A row that an acknowledgement already closed is left alone: the
// device can answer before the REST handler gets here. A retransmission finds
// the row in 'sent' and only bumps the counter, so sent_at keeps the first time
// the device saw the command.
func (s *Store) MarkCommandSent(ctx context.Context, commandID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE commands
		SET status = $2, sent_at = COALESCE(sent_at, now()), attempts = attempts + 1
		WHERE id = $1 AND status = ANY($3)`,
		commandID, model.CommandStatusSent, openCommandStatuses)
	if err != nil {
		return fmt.Errorf("postgres: mark command sent: %w", err)
	}
	return nil
}

// MarkCommandFailed records that the command ended without the device
// executing it: it never left the server, or it was sent often enough that
// waiting longer makes no sense. done_at holds the time it ended, the same
// column a device answer fills.
func (s *Store) MarkCommandFailed(ctx context.Context, commandID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE commands
		SET status = $2, done_at = now()
		WHERE id = $1 AND status = ANY($3)`,
		commandID, model.CommandStatusFailed, openCommandStatuses)
	if err != nil {
		return fmt.Errorf("postgres: mark command failed: %w", err)
	}
	return nil
}

// AcknowledgeCommand closes the command of device that the device answered. A
// serial pairs the answer with the exact row; without one the newest sent row
// is taken, which is all the protocols that carry no serial allow. When nothing
// is waiting, the call is a no-op.
func (s *Store) AcknowledgeCommand(ctx context.Context, deviceID int64, ack codec.CommandAck) error {
	query, args := acknowledgeQuery(deviceID, ack)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: acknowledge command: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var (
		commandID int64
		result    sql.NullInt64
		doneAt    time.Time
	)
	err = tx.QueryRowContext(ctx, query+" RETURNING id, result_code, done_at", args...).
		Scan(&commandID, &result, &doneAt)
	if errors.Is(err, sql.ErrNoRows) {
		// Nothing was waiting for an answer; an unsolicited ack is not an error.
		return nil
	}
	if err != nil {
		return fmt.Errorf("postgres: acknowledge command: %w", err)
	}

	// The command result event is written in the same transaction as the
	// acknowledgement, so a command can never end without its event and an
	// event can never point at a command that is still waiting.
	var code any
	if result.Valid {
		code = result.Int64
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO events (device_id, event_time, type, attributes)
		 VALUES ($1, $2, $3, jsonb_build_object('result', $4::bigint, 'commandId', $5::bigint))`,
		deviceID, doneAt, model.EventCommandResult, code, commandID,
	); err != nil {
		return fmt.Errorf("postgres: record command result event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("postgres: acknowledge command: %w", err)
	}
	committed = true
	return nil
}

// acknowledgeQuery builds the statement that closes one waiting command.
func acknowledgeQuery(deviceID int64, ack codec.CommandAck) (string, []any) {
	status := model.CommandStatusDone
	if ack.Code != 0 {
		status = model.CommandStatusFailed
	}

	args := []any{deviceID}
	filter := ""
	if ack.Serial != nil {
		args = append(args, *ack.Serial)
		filter = fmt.Sprintf(" AND serial = $%d", len(args))
	}
	args = append(args, status, ack.Code, model.CommandStatusSent)

	return fmt.Sprintf(`UPDATE commands SET status = $%d, result_code = $%d, done_at = now()
		WHERE id = (
			SELECT id FROM commands
			WHERE device_id = $1%s AND status = $%d
			ORDER BY id DESC
			LIMIT 1
		)`,
		len(args)-2, len(args)-1, filter, len(args),
	), args
}

// PendingCommands returns the commands of device that were recorded but never
// reached the socket, oldest first, so a device that reconnects still receives
// what was queued for it before a restart. Limit is clamped to 1..100.
func (s *Store) PendingCommands(ctx context.Context, deviceID int64, limit int) ([]model.Command, error) {
	if limit <= 0 || limit > pendingCommandLimit {
		limit = pendingCommandLimit
	}

	rows, err := s.db.QueryContext(ctx, `SELECT `+commandColumns+` FROM commands
		WHERE device_id = $1 AND status = $2
		ORDER BY id ASC
		LIMIT $3`, deviceID, model.CommandStatusPending, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: query pending commands: %w", err)
	}
	defer rows.Close()

	return scanCommands(rows)
}

// DeleteCommandsBefore drops the history older than age. A command that ended is
// measured by when it ended; one that never ended is measured by when it was
// recorded, so a row that got stuck cannot grow the table forever.
func (s *Store) DeleteCommandsBefore(ctx context.Context, age time.Duration) (int64, error) {
	if age <= 0 {
		return 0, ErrInvalidRetention
	}

	result, err := s.db.ExecContext(ctx, `DELETE FROM commands
		WHERE (done_at IS NOT NULL AND done_at < now() - make_interval(secs => $1))
		   OR (done_at IS NULL AND created_at < now() - make_interval(secs => $1))`,
		age.Seconds())
	if err != nil {
		return 0, fmt.Errorf("postgres: delete old commands: %w", err)
	}

	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("postgres: delete old commands: %w", err)
	}
	return deleted, nil
}

// QueryCommands returns the commands matching the filter, newest first. Limit
// is clamped to the range 1..1000 and Offset to a non-negative value.
func (s *Store) QueryCommands(ctx context.Context, filter model.CommandFilter) ([]model.Command, error) {
	query, args := commandQuery(filter)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: query commands: %w", err)
	}
	defer rows.Close()

	return scanCommands(rows)
}

// scanCommands reads a result set built from commandColumns.
func scanCommands(rows *sql.Rows) ([]model.Command, error) {
	var commands []model.Command
	for rows.Next() {
		var (
			command    model.Command
			resultCode sql.NullInt64
			serial     sql.NullInt64
			sentAt     sql.NullTime
			doneAt     sql.NullTime
			attributes []byte
		)
		if err := rows.Scan(
			&command.ID, &command.DeviceID, &command.Type, &attributes,
			&command.Status, &resultCode, &serial, &command.Attempts,
			&sentAt, &doneAt, &command.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan command: %w", err)
		}

		command.Attributes = map[string]any{}
		if len(attributes) > 0 {
			if err := json.Unmarshal(attributes, &command.Attributes); err != nil {
				return nil, fmt.Errorf("postgres: decode command attributes: %w", err)
			}
		}
		if resultCode.Valid {
			code := int(resultCode.Int64)
			command.ResultCode = &code
		}
		if serial.Valid {
			command.Serial = int(serial.Int64)
		}
		if sentAt.Valid {
			command.SentAt = sentAt.Time
		}
		if doneAt.Valid {
			command.DoneAt = doneAt.Time
		}
		commands = append(commands, command)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: query commands: %w", err)
	}
	return commands, nil
}

// commandQuery builds the statement and its arguments.
func commandQuery(filter model.CommandFilter) (string, []any) {
	args := make([]any, 0, 3)
	clauses := make([]string, 0, 2)

	if filter.DeviceID != 0 {
		args = append(args, filter.DeviceID)
		clauses = append(clauses, fmt.Sprintf("device_id = $%d", len(args)))
	}
	if filter.IDs != nil {
		clauses = append(clauses, deviceIDsClause(&args, "id", filter.IDs))
	}
	if filter.DeviceIDs != nil {
		clauses = append(clauses, deviceIDsClause(&args, "device_id", filter.DeviceIDs))
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultCommandLimit
	}
	if limit > maxCommandLimit {
		limit = maxCommandLimit
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	var builder strings.Builder
	builder.WriteString("SELECT " + commandColumns + " FROM commands")
	if len(clauses) > 0 {
		builder.WriteString(" WHERE ")
		builder.WriteString(strings.Join(clauses, " AND "))
	}

	args = append(args, limit, offset)
	fmt.Fprintf(&builder, " ORDER BY id DESC LIMIT $%d OFFSET $%d",
		len(args)-1, len(args))
	return builder.String(), args
}
