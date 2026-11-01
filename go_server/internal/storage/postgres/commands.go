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
	defaultCommandLimit = 100
	maxCommandLimit     = 1000

	// commandColumns keeps the SELECT list and the scan order in sync.
	commandColumns = `id, device_id, type, attributes, status, result_code,
		sent_at, done_at, created_at`
)

// ErrMissingCommand is returned when a command cannot be recorded because it
// is nil or carries no device id.
var ErrMissingCommand = errors.New("postgres: command without device id")

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

	if err := s.db.QueryRowContext(ctx, `INSERT INTO commands (device_id, type, attributes)
		VALUES ($1, $2, $3) RETURNING id`,
		command.DeviceID, command.Type, string(encoded),
	).Scan(&command.ID); err != nil {
		return fmt.Errorf("postgres: insert command: %w", err)
	}

	command.Status = model.CommandStatusPending
	return nil
}

// MarkCommandSent records that the payload reached the device socket. A row
// that an acknowledgement already closed is left alone: the device can answer
// before the REST handler gets here.
func (s *Store) MarkCommandSent(ctx context.Context, commandID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE commands
		SET status = $2, sent_at = now()
		WHERE id = $1 AND status = $3`,
		commandID, model.CommandStatusSent, model.CommandStatusPending)
	if err != nil {
		return fmt.Errorf("postgres: mark command sent: %w", err)
	}
	return nil
}

// MarkCommandFailed records that the payload never left the server. done_at
// holds the time it ended, the same column a device answer fills.
func (s *Store) MarkCommandFailed(ctx context.Context, commandID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE commands
		SET status = $2, done_at = now()
		WHERE id = $1 AND status = $3`,
		commandID, model.CommandStatusFailed, model.CommandStatusPending)
	if err != nil {
		return fmt.Errorf("postgres: mark command failed: %w", err)
	}
	return nil
}

// AcknowledgeCommand closes the newest command of device that is waiting for an
// answer, using resultCode as the device reported it. Neither protocol that
// answers commands here carries a correlation id this server can match against,
// so the newest sent row is the one the answer belongs to. When nothing is
// waiting, the call is a no-op.
func (s *Store) AcknowledgeCommand(ctx context.Context, deviceID int64, resultCode int) error {
	status := model.CommandStatusDone
	if resultCode != 0 {
		status = model.CommandStatusFailed
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE commands
		SET status = $2, result_code = $3, done_at = now()
		WHERE id = (
			SELECT id FROM commands
			WHERE device_id = $1 AND status = $4
			ORDER BY id DESC
			LIMIT 1
		)`,
		deviceID, status, resultCode, model.CommandStatusSent,
	); err != nil {
		return fmt.Errorf("postgres: acknowledge command: %w", err)
	}
	return nil
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

	var commands []model.Command
	for rows.Next() {
		var (
			command    model.Command
			resultCode sql.NullInt64
			sentAt     sql.NullTime
			doneAt     sql.NullTime
			attributes []byte
		)
		if err := rows.Scan(
			&command.ID, &command.DeviceID, &command.Type, &attributes,
			&command.Status, &resultCode, &sentAt, &doneAt, &command.CreatedAt,
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
