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
	defaultResourceLimit = 100
	maxResourceLimit     = 1000
)

// rowScanner is the part of *sql.Row and *sql.Rows a scan function needs.
type rowScanner interface {
	Scan(dest ...any) error
}

// resourceSpec binds one table to one model type: which columns it has, how to
// turn a value into bind arguments, how to read a row back, and how to reach
// the id. The CRUD helpers below take it so the column order can only be
// written once per resource.
type resourceSpec[T any] struct {
	table string
	// columns are the data columns after id, in values order.
	columns []string
	// keyword lists the text columns a keyword filter searches.
	keyword []string
	values  func(*T) ([]any, error)
	scan    func(rowScanner) (*T, error)
	id      func(*T) int64
	setID   func(*T, int64)
}

// encodeAttributes renders an attribute map for a JSONB column. A nil map
// becomes an empty object so the column keeps its NOT NULL default shape.
func encodeAttributes(attributes model.Attributes) (string, error) {
	if attributes == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(attributes)
	if err != nil {
		return "", fmt.Errorf("postgres: encode attributes: %w", err)
	}
	return string(encoded), nil
}

// decodeAttributes reads a JSONB column into an attribute map. The result is
// never nil, so callers can set attributes without a nil check.
func decodeAttributes(raw []byte) (model.Attributes, error) {
	attributes := model.Attributes{}
	if len(raw) == 0 {
		return attributes, nil
	}
	if err := json.Unmarshal(raw, &attributes); err != nil {
		return nil, fmt.Errorf("postgres: decode attributes: %w", err)
	}
	return attributes, nil
}

// createResource inserts value and stores the generated id back into it.
func createResource[T any](ctx context.Context, db *sql.DB, spec resourceSpec[T], value *T) error {
	args, err := spec.values(value)
	if err != nil {
		return err
	}

	placeholders := make([]string, len(args))
	for index := range args {
		placeholders[index] = fmt.Sprintf("$%d", index+1)
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING id",
		spec.table, strings.Join(spec.columns, ", "), strings.Join(placeholders, ", "))

	var id int64
	if err := db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
		return fmt.Errorf("postgres: insert %s: %w", spec.table, err)
	}
	spec.setID(value, id)
	return nil
}

// updateResource replaces every data column of an existing row. A missing id
// reports model.ErrResourceNotFound, which the REST layer turns into 404.
func updateResource[T any](ctx context.Context, db *sql.DB, spec resourceSpec[T], value *T) error {
	id := spec.id(value)
	if id == 0 {
		return model.ErrResourceNotFound
	}

	args, err := spec.values(value)
	if err != nil {
		return err
	}
	assignments := make([]string, len(spec.columns))
	for index, column := range spec.columns {
		assignments[index] = fmt.Sprintf("%s = $%d", column, index+1)
	}
	args = append(args, id)
	query := fmt.Sprintf("UPDATE %s SET %s WHERE id = $%d",
		spec.table, strings.Join(assignments, ", "), len(args))

	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("postgres: update %s %d: %w", spec.table, id, err)
	}
	return requireAffected(result, spec.table, id)
}

// deleteResource removes one row.
func deleteResource(ctx context.Context, db *sql.DB, table string, id int64) error {
	result, err := db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", table), id)
	if err != nil {
		return fmt.Errorf("postgres: delete %s %d: %w", table, id, err)
	}
	return requireAffected(result, table, id)
}

func requireAffected(result sql.Result, table string, id int64) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres: %s %d: %w", table, id, err)
	}
	if affected == 0 {
		return model.ErrResourceNotFound
	}
	return nil
}

// listResources reads whole rows selected by filter, ordered by id so paging is
// stable.
func listResources[T any](ctx context.Context, db *sql.DB, spec resourceSpec[T], filter model.ResourceFilter) ([]*T, error) {
	query, args := resourceQuery(spec, filter)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: query %s: %w", spec.table, err)
	}
	defer rows.Close()

	var values []*T
	for rows.Next() {
		value, err := spec.scan(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: query %s: %w", spec.table, err)
	}
	return values, nil
}

// resourceQuery builds the SELECT and its arguments. Only table and column
// names from a resourceSpec reach the statement text.
func resourceQuery[T any](spec resourceSpec[T], filter model.ResourceFilter) (string, []any) {
	args := make([]any, 0, 4)
	clauses := make([]string, 0, 2)

	if filter.IDs != nil {
		clauses = append(clauses, deviceIDsClause(&args, "id", filter.IDs))
	}
	if keyword := strings.TrimSpace(filter.Keyword); keyword != "" && len(spec.keyword) > 0 {
		args = append(args, "%"+keyword+"%")
		placeholder := fmt.Sprintf("$%d", len(args))
		patterns := make([]string, len(spec.keyword))
		for index, column := range spec.keyword {
			patterns[index] = fmt.Sprintf("%s ILIKE %s", column, placeholder)
		}
		clauses = append(clauses, "("+strings.Join(patterns, " OR ")+")")
	}

	var builder strings.Builder
	builder.WriteString("SELECT id, ")
	builder.WriteString(strings.Join(spec.columns, ", "))
	builder.WriteString(" FROM ")
	builder.WriteString(spec.table)
	if len(clauses) > 0 {
		builder.WriteString(" WHERE ")
		builder.WriteString(strings.Join(clauses, " AND "))
	}

	args = append(args, clampResourceLimit(filter.Limit), clampResourceOffset(filter.Offset))
	fmt.Fprintf(&builder, " ORDER BY id LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	return builder.String(), args
}

func clampResourceLimit(limit int) int {
	if limit <= 0 {
		return defaultResourceLimit
	}
	if limit > maxResourceLimit {
		return maxResourceLimit
	}
	return limit
}

func clampResourceOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

// notFoundAs maps a sql.ErrNoRows scan error to the resource error the REST
// layer expects.
func notFoundAs(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return model.ErrResourceNotFound
	}
	return err
}
