package storage

import (
	"context"
	"database/sql"
)

// DB wraps *sql.DB so every statement goes through the dialect's placeholder
// rewriting. The embedded pool keeps PingContext, Close and the other methods
// available unchanged.
type DB struct {
	*sql.DB
	dialect dialect
}

// Tx wraps *sql.Tx for the same reason as DB.
type Tx struct {
	*sql.Tx
	dialect dialect
}

func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	query, args = d.dialect.rebind(query, args)
	return d.DB.ExecContext(ctx, query, args...)
}

func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	query, args = d.dialect.rebind(query, args)
	return d.DB.QueryContext(ctx, query, args...)
}

func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	query, args = d.dialect.rebind(query, args)
	return d.DB.QueryRowContext(ctx, query, args...)
}

func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx, dialect: d.dialect}, nil
}

func (t *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	query, args = t.dialect.rebind(query, args)
	return t.Tx.ExecContext(ctx, query, args...)
}

func (t *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	query, args = t.dialect.rebind(query, args)
	return t.Tx.QueryContext(ctx, query, args...)
}

func (t *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	query, args = t.dialect.rebind(query, args)
	return t.Tx.QueryRowContext(ctx, query, args...)
}

func (d *DB) dbDialect() dialect { return d.dialect }

func (t *Tx) dbDialect() dialect { return t.dialect }

// dialectOf returns the dialect behind a DB or Tx. The fallback only matters
// for a bare *sql.DB, which the store never uses.
func dialectOf(exec any) dialect {
	if holder, ok := exec.(interface{ dbDialect() dialect }); ok {
		return holder.dbDialect()
	}
	return postgresDialect
}

// insertReturningID runs an INSERT ... RETURNING id. PostgreSQL and SQLite
// return the id from the statement; MySQL gets it from LastInsertId after the
// RETURNING clause is removed.
func insertReturningID(ctx context.Context, exec execer, query string, args ...any) (int64, error) {
	selected := dialectOf(exec)
	if selected.returning {
		var id int64
		err := exec.QueryRowContext(ctx, query, args...).Scan(&id)
		return id, err
	}

	result, err := exec.ExecContext(ctx, stripReturning(query), args...)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}
