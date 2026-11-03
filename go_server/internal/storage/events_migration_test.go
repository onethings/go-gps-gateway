package storage

import (
	"strings"
	"testing"
)

// TestEventsMigration checks the embedded DDL still splits into the statements
// the runner expects, and that the status column lands on devices.
func TestEventsMigration(t *testing.T) {
	content, err := migrationsFS.ReadFile("migrations/postgres/0010_events.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}

	statements := splitStatements(string(content))
	if len(statements) != 6 {
		t.Fatalf("statements = %d, want 6: %#v", len(statements), statements)
	}

	joined := strings.Join(statements, "\n")
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS status",
		"CREATE TABLE IF NOT EXISTS events",
		"REFERENCES positions (id) ON DELETE SET NULL",
		"events (device_id, event_time DESC)",
	} {
		if !strings.Contains(joined, fragment) {
			t.Errorf("migration is missing %q", fragment)
		}
	}
}
