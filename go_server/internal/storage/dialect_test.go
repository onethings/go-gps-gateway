package storage

import (
	"strings"
	"testing"
	"time"
)

func TestParseDSN(t *testing.T) {
	tests := []struct {
		name   string
		dsn    string
		want   string
		driver string
	}{
		{"postgres", "postgres://user:pass@localhost:5432/traccar?sslmode=disable", "postgres", "pgx"},
		{"postgresql", "postgresql://localhost/traccar", "postgres", "pgx"},
		{"mysql", "mysql://user:pass@localhost:3306/traccar", "mysql", "mysql"},
		{"sqlite file", "sqlite:///var/lib/traccar/traccar.db", "sqlite", "sqlite"},
		{"sqlite memory", "sqlite::memory:", "sqlite", "sqlite"},
		{"sqlite file uri", "sqlite://file:traccar.db?cache=shared", "sqlite", "sqlite"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selected, driverDSN, err := parseDSN(test.dsn)
			if err != nil {
				t.Fatalf("parseDSN: %v", err)
			}
			if selected.name != test.want || selected.driver != test.driver {
				t.Fatalf("dialect = %s/%s, want %s/%s",
					selected.name, selected.driver, test.want, test.driver)
			}
			if driverDSN == "" {
				t.Fatal("driver DSN is empty")
			}
		})
	}
}

func TestParseDSNRejectsUnknownScheme(t *testing.T) {
	if _, _, err := parseDSN("oracle://localhost/traccar"); err == nil {
		t.Fatal("expected an error for an unsupported scheme")
	}
}

func TestMySQLDSN(t *testing.T) {
	dsn, err := mysqlDSN("mysql://user:secret@db.example:3307/traccar?tls=true")
	if err != nil {
		t.Fatalf("mysqlDSN: %v", err)
	}
	for _, want := range []string{"user:secret@tcp(db.example:3307)/traccar", "parseTime=true", "clientFoundRows=true"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("dsn = %q, want %q", dsn, want)
		}
	}

	socket, err := mysqlDSN("mysql://root@localhost/traccar?socket=/tmp/mysql.sock")
	if err != nil {
		t.Fatalf("mysqlDSN socket: %v", err)
	}
	if !strings.Contains(socket, "unix(/tmp/mysql.sock)") {
		t.Errorf("dsn = %q, want a unix socket", socket)
	}
}

func TestSQLiteDSN(t *testing.T) {
	tests := []struct {
		dsn  string
		want string
	}{
		{"sqlite::memory:", "file::memory:?cache=shared&"},
		{"sqlite:///tmp/traccar.db", "file:/tmp/traccar.db?_pragma=foreign_keys(1)"},
		{"sqlite://file:/tmp/traccar.db?cache=shared", "file:/tmp/traccar.db?cache=shared&_pragma=foreign_keys(1)"},
	}
	for _, test := range tests {
		got := sqliteDSN(test.dsn)
		if !strings.HasPrefix(got, test.want) {
			t.Errorf("sqliteDSN(%q) = %q, want prefix %q", test.dsn, got, test.want)
		}
	}
}

func TestRebindPlaceholders(t *testing.T) {
	query, args := mysqlDialect.rebind(
		`SELECT * FROM t WHERE a = $1 AND b = $2 AND c = $1`,
		[]any{"a", "b"},
	)
	if query != `SELECT * FROM t WHERE a = ? AND b = ? AND c = ?` {
		t.Errorf("query = %q", query)
	}
	want := []any{"a", "b", "a"}
	if len(args) != len(want) {
		t.Fatalf("args = %#v", args)
	}
	for index := range want {
		if args[index] != want[index] {
			t.Fatalf("args = %#v, want %#v", args, want)
		}
	}

	query, args = postgresDialect.rebind(`SELECT $1`, []any{7})
	if query != `SELECT $1` || len(args) != 1 {
		t.Fatalf("postgres rebind changed the query: %q %#v", query, args)
	}
}

func TestRebindKeepsTimesUTC(t *testing.T) {
	zone := time.FixedZone("CST", 8*3600)
	value := time.Date(2026, 11, 3, 18, 0, 0, 0, zone)
	_, args := sqliteDialect.rebind(`SELECT $1`, []any{value})
	if got := args[0].(time.Time); !got.Equal(value) || got.Location() != time.UTC {
		t.Fatalf("time = %s (%s), want UTC", got, got.Location())
	}
}

func TestStripReturning(t *testing.T) {
	tests := map[string]string{
		"INSERT INTO t (a) VALUES ($1) RETURNING id":      "INSERT INTO t (a) VALUES ($1)",
		"INSERT INTO t (a) VALUES ($1)\n\t\tRETURNING id": "INSERT INTO t (a) VALUES ($1)",
		"SELECT 1": "SELECT 1",
	}
	for query, want := range tests {
		if got := stripReturning(query); strings.TrimSpace(got) != want {
			t.Errorf("stripReturning(%q) = %q, want %q", query, got, want)
		}
	}
}
