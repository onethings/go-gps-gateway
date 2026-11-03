package postgres

import (
	"strings"
	"testing"

	"traccar-server_go/go_server/internal/model"
)

func TestResourceQueryDefaults(t *testing.T) {
	query, args := resourceQuery(groupSpec, model.ResourceFilter{})

	want := "SELECT id, name, group_id, attributes FROM groups ORDER BY id LIMIT $1 OFFSET $2"
	if query != want {
		t.Errorf("query = %q, want %q", query, want)
	}
	if len(args) != 2 || args[0] != defaultResourceLimit || args[1] != 0 {
		t.Errorf("args = %#v, want [%d 0]", args, defaultResourceLimit)
	}
}

func TestResourceQueryClampsPaging(t *testing.T) {
	_, args := resourceQuery(groupSpec, model.ResourceFilter{Limit: -5, Offset: -3})
	if args[0] != defaultResourceLimit || args[1] != 0 {
		t.Errorf("args = %#v, want the defaults", args)
	}

	_, args = resourceQuery(groupSpec, model.ResourceFilter{Limit: maxResourceLimit + 1, Offset: 7})
	if args[0] != maxResourceLimit || args[1] != 7 {
		t.Errorf("args = %#v, want [%d 7]", args, maxResourceLimit)
	}
}

func TestResourceQueryIDs(t *testing.T) {
	query, args := resourceQuery(groupSpec, model.ResourceFilter{IDs: []int64{4, 9}})
	if !strings.Contains(query, "WHERE id IN ($1, $2)") {
		t.Errorf("query = %q, want an id filter", query)
	}
	if len(args) != 4 || args[0] != int64(4) || args[1] != int64(9) {
		t.Errorf("args = %#v, want the ids first", args)
	}

	// A non-nil empty slice must match nothing, not everything.
	query, _ = resourceQuery(groupSpec, model.ResourceFilter{IDs: []int64{}})
	if !strings.Contains(query, "WHERE false") {
		t.Errorf("query = %q, want an empty id set to match nothing", query)
	}
}

func TestResourceQueryKeyword(t *testing.T) {
	query, args := resourceQuery(driverSpec, model.ResourceFilter{Keyword: " bob "})
	if !strings.Contains(query, "(name ILIKE $1 OR unique_id ILIKE $1)") {
		t.Errorf("query = %q, want the keyword over both text columns", query)
	}
	if len(args) != 3 || args[0] != "%bob%" {
		t.Errorf("args = %#v, want %q first", args, "%bob%")
	}

	query, _ = resourceQuery(attributeSpec, model.ResourceFilter{Keyword: "   "})
	if strings.Contains(query, "ILIKE") {
		t.Errorf("query = %q, a blank keyword must not filter", query)
	}
}

func TestResourcesMigration(t *testing.T) {
	content, err := migrationsFS.ReadFile("migrations/0012_resources.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}

	statements := splitStatements(string(content))
	if len(statements) != 14 {
		t.Fatalf("statements = %d, want 14: %#v", len(statements), statements)
	}

	joined := strings.Join(statements, "\n")
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS calendars",
		"CREATE TABLE IF NOT EXISTS groups",
		"ADD COLUMN IF NOT EXISTS group_id BIGINT REFERENCES groups (id)",
		"ADD COLUMN IF NOT EXISTS calendar_id BIGINT REFERENCES calendars (id)",
		"CREATE TABLE IF NOT EXISTS drivers",
		"CREATE TABLE IF NOT EXISTS maintenances",
		"CREATE TABLE IF NOT EXISTS orders",
		"CREATE TABLE IF NOT EXISTS reports",
		"CREATE TABLE IF NOT EXISTS statistics",
		"CREATE TABLE IF NOT EXISTS attributes",
	} {
		if !strings.Contains(joined, fragment) {
			t.Errorf("migration is missing %q", fragment)
		}
	}
}
