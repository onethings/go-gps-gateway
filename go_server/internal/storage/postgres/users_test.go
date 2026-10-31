package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
)

// TestTokenStoreIntegration covers the users / user_devices / api_tokens path
// against a real database. It runs only when TRACCAR_POSTGRES_DSN is set.
func TestTokenStoreIntegration(t *testing.T) {
	dsn := os.Getenv("TRACCAR_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TRACCAR_POSTGRES_DSN is not set")
	}

	ctx := context.Background()
	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	suffix := time.Now().Format("150405.000000")
	email := "token-" + suffix + "@example.test"

	var userID int64
	if err := store.db.QueryRowContext(ctx,
		`INSERT INTO users (email, name) VALUES ($1, 'test') RETURNING id`,
		email,
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(),
			`DELETE FROM users WHERE id = $1`, userID)
	})

	deviceID, err := store.Register("TOK"+suffix, "gt06")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(),
			`DELETE FROM devices WHERE id = $1`, deviceID)
	})

	if err := store.AssignDevice(ctx, userID, deviceID); err != nil {
		t.Fatalf("assign: %v", err)
	}
	// Assigning twice must not fail.
	if err := store.AssignDevice(ctx, userID, deviceID); err != nil {
		t.Fatalf("assign twice: %v", err)
	}

	token, err := store.IssueToken(ctx, email, []auth.Scope{auth.ScopeReadPositions}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	principal, err := store.LookupToken(ctx, auth.HashToken(token))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if principal == nil {
		t.Fatal("lookup returned no principal")
	}
	if principal.UserID != userID || principal.Admin {
		t.Errorf("principal = %+v", principal)
	}
	if !principal.Has(auth.ScopeReadPositions) || principal.Has(auth.ScopeReadAlerts) {
		t.Errorf("scopes were not applied: %+v", principal)
	}
	if !principal.CanAccessDevice(deviceID) || principal.CanAccessDevice(deviceID+1000) {
		t.Errorf("devices were not applied: %+v", principal)
	}

	if unknown, err := store.LookupToken(ctx, auth.HashToken("not-a-token")); err != nil || unknown != nil {
		t.Errorf("unknown token = %+v, err = %v", unknown, err)
	}
	if _, err := store.IssueToken(ctx, "nobody@example.test", nil, nil); !errors.Is(err, ErrUnknownUser) {
		t.Errorf("issue for unknown user err = %v, want ErrUnknownUser", err)
	}
}
