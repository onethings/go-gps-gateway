package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	sqlite3 "modernc.org/sqlite"
)

// dialect describes the SQL differences the store has to bridge. The store is
// written in PostgreSQL flavoured SQL ($1 placeholders, RETURNING); a dialect
// rewrites the placeholders and flags the few statements that need help.
type dialect struct {
	name string
	// driver is the database/sql driver name.
	driver string
	// migrations is the subdirectory of migrations/ to apply.
	migrations string
	// dollarPlaceholders keeps $1 style parameters. MySQL and SQLite use ?.
	dollarPlaceholders bool
	// returning reports whether the database supports RETURNING.
	returning bool
}

var (
	postgresDialect = dialect{
		name:               "postgres",
		driver:             "pgx",
		migrations:         "postgres",
		dollarPlaceholders: true,
		returning:          true,
	}
	mysqlDialect = dialect{
		name:       "mysql",
		driver:     "mysql",
		migrations: "mysql",
		returning:  false,
	}
	sqliteDialect = dialect{
		name:       "sqlite",
		driver:     "sqlite",
		migrations: "sqlite",
		returning:  true,
	}
)

// parseDSN picks the dialect from the URL scheme. PostgreSQL keeps its URL
// untouched; MySQL is converted to the go-sql-driver DSN; SQLite accepts a
// plain path, sqlite:// and file: URLs.
func parseDSN(dsn string) (dialect, string, error) {
	lower := strings.ToLower(dsn)
	switch {
	case strings.HasPrefix(lower, "postgres://"), strings.HasPrefix(lower, "postgresql://"):
		return postgresDialect, dsn, nil
	case strings.HasPrefix(lower, "mysql://"):
		converted, err := mysqlDSN(dsn)
		if err != nil {
			return dialect{}, "", err
		}
		return mysqlDialect, converted, nil
	case strings.HasPrefix(lower, "sqlite://"), strings.HasPrefix(lower, "sqlite:"),
		strings.HasPrefix(lower, "file:"):
		return sqliteDialect, sqliteDSN(dsn), nil
	default:
		return dialect{}, "", fmt.Errorf(
			"storage: unsupported database url %q (use postgres://, mysql:// or sqlite://)", dsn)
	}
}

// mysqlDSN turns a mysql:// URL into the DSN the driver expects. The driver
// parameters that matter to the store are always set: timestamps are parsed
// into time.Time, bound in UTC, and RowsAffected counts matched rows so an
// update that changes nothing is not mistaken for a missing row.
func mysqlDSN(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("storage: parse mysql url: %w", err)
	}

	config := mysql.NewConfig()
	config.Net = "tcp"
	config.Addr = parsed.Host
	if config.Addr == "" {
		config.Addr = "127.0.0.1:3306"
	}
	if parsed.User != nil {
		config.User = parsed.User.Username()
		config.Passwd, _ = parsed.User.Password()
	}
	config.DBName = strings.TrimPrefix(parsed.Path, "/")
	config.ParseTime = true
	config.Loc = time.UTC
	config.ClientFoundRows = true
	config.Collation = "utf8mb4_unicode_ci"
	config.Params = make(map[string]string)
	for key, values := range parsed.Query() {
		if len(values) > 0 {
			config.Params[key] = values[len(values)-1]
		}
	}
	if socket := config.Params["socket"]; socket != "" {
		config.Net = "unix"
		config.Addr = socket
		delete(config.Params, "socket")
	} else if socket := config.Params["unix_socket"]; socket != "" {
		config.Net = "unix"
		config.Addr = socket
		delete(config.Params, "unix_socket")
	}
	if _, ok := config.Params["time_zone"]; !ok {
		config.Params["time_zone"] = "'+00:00'"
	}
	return config.FormatDSN(), nil
}

// sqliteDSN keeps a file path or an in-memory name and adds the pragmas the
// store relies on. A shared in-memory database is used when no path is given,
// so the pool's connections see the same schema.
func sqliteDSN(raw string) string {
	path := raw
	switch {
	case strings.HasPrefix(path, "sqlite://"):
		path = strings.TrimPrefix(path, "sqlite://")
	case strings.HasPrefix(path, "sqlite:"):
		path = strings.TrimPrefix(path, "sqlite:")
	}
	if path == "" || path == ":memory:" || path == "memory:" {
		return "file::memory:?cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	}

	pragmas := "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	if !strings.HasPrefix(path, "file:") {
		path = "file:" + path
	}
	if strings.Contains(path, "?") {
		return path + "&" + pragmas
	}
	return path + "?" + pragmas
}

// rebind converts $n placeholders to ? for the drivers that want them, and
// reorders the arguments to the order the placeholders appear. A repeated
// placeholder duplicates its argument, which is what the positional driver
// needs.
func (d dialect) rebind(query string, args []any) (string, []any) {
	args = normalizeArgs(args)
	if d.dollarPlaceholders {
		return query, args
	}

	var builder strings.Builder
	builder.Grow(len(query) + 8)
	bound := make([]any, 0, len(args))

	for i := 0; i < len(query); i++ {
		if query[i] != '$' || i+1 >= len(query) || query[i+1] < '0' || query[i+1] > '9' {
			builder.WriteByte(query[i])
			continue
		}
		end := i + 1
		for end < len(query) && query[end] >= '0' && query[end] <= '9' {
			end++
		}
		index, err := strconv.Atoi(query[i+1 : end])
		if err != nil || index < 1 || index > len(args) {
			// Leave an invalid reference untouched so the driver reports it
			// instead of the store silently sending the wrong argument.
			builder.WriteString(query[i:end])
			i = end - 1
			continue
		}
		builder.WriteByte('?')
		bound = append(bound, args[index-1])
		i = end - 1
	}

	return builder.String(), bound
}

// normalizeArgs stores time values in UTC, so a timestamp written by one node
// compares the same on the next read.
func normalizeArgs(args []any) []any {
	for index, arg := range args {
		switch value := arg.(type) {
		case time.Time:
			args[index] = value.UTC()
		case *time.Time:
			if value != nil {
				utc := value.UTC()
				args[index] = &utc
			}
		case sql.NullTime:
			if value.Valid {
				args[index] = sql.NullTime{Time: value.Time.UTC(), Valid: true}
			}
		}
	}
	return args
}

// isDuplicate reports whether err is a unique constraint violation. The
// callers that turn a duplicate key into a model error use it instead of
// relying on ON CONFLICT, which MySQL spells differently.
func (d dialect) isDuplicate(err error) bool {
	switch d.name {
	case "postgres":
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && pgErr.Code == "23505"
	case "mysql":
		var myErr *mysql.MySQLError
		return errors.As(err, &myErr) && myErr.Number == 1062
	case "sqlite":
		var sqliteErr *sqlite3.Error
		if errors.As(err, &sqliteErr) {
			code := sqliteErr.Code()
			return code == 2067 || code == 1555 // SQLITE_CONSTRAINT_UNIQUE, _PRIMARYKEY
		}
		return strings.Contains(strings.ToLower(err.Error()), "unique constraint")
	default:
		return false
	}
}

// stripReturning removes a trailing RETURNING clause for the databases that
// cannot execute it. The calls that need the returned id use LastInsertId.
func stripReturning(query string) string {
	upper := strings.ToUpper(query)
	if index := strings.LastIndex(upper, "RETURNING"); index >= 0 {
		return strings.TrimSpace(query[:index])
	}
	return query
}
