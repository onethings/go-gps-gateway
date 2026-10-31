package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
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

// TestUserAdminStoreIntegration covers the account administration queries:
// user CRUD, the disabled flag, device grants and token listing/revocation.
func TestUserAdminStoreIntegration(t *testing.T) {
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

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	suffix := time.Now().Format("150405.000000")
	email := "admin-" + suffix + "@example.test"

	user, err := store.CreateUser(ctx, model.UserCreate{Email: email, Name: " Admin ", Admin: true})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID)
	})
	if user.ID == 0 || user.Email != email || !user.Admin || user.Disabled ||
		user.DeviceCount != 0 || user.TokenCount != 0 || user.CreatedAt.IsZero() {
		t.Fatalf("created user = %+v", user)
	}

	if _, err := store.CreateUser(ctx, model.UserCreate{Email: email}); !errors.Is(err, model.ErrDuplicateUser) {
		t.Errorf("duplicate create err = %v, want ErrDuplicateUser", err)
	}

	listed, err := store.Users(ctx, model.UserFilter{Limit: 1000})
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	var found bool
	for _, candidate := range listed {
		if candidate.ID == user.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("user %d is missing from the list", user.ID)
	}

	name, admin := "Renamed", false
	updated, err := store.UpdateUser(ctx, user.ID, model.UserUpdate{Name: &name, Admin: &admin})
	if err != nil {
		t.Fatalf("update user: %v", err)
	}
	if updated.Name != name || updated.Admin {
		t.Errorf("updated user = %+v", updated)
	}
	if _, err := store.UpdateUser(ctx, user.ID+1_000_000, model.UserUpdate{Name: &name}); !errors.Is(err, model.ErrUserNotFound) {
		t.Errorf("update unknown user err = %v, want ErrUserNotFound", err)
	}

	token, plaintext, err := store.IssueTokenForUser(ctx, user.ID, []string{"read:positions"}, nil)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if plaintext == "" || token.UserID != user.ID || token.ExpiresAt != nil {
		t.Fatalf("issued token = %+v", token)
	}
	principal, err := store.LookupToken(ctx, auth.HashToken(plaintext))
	if err != nil || principal == nil || !principal.Has(auth.ScopeReadPositions) {
		t.Fatalf("lookup after issue = %+v, err = %v", principal, err)
	}
	if _, _, err := store.IssueTokenForUser(ctx, user.ID+1_000_000, nil, nil); !errors.Is(err, model.ErrUserNotFound) {
		t.Errorf("issue for unknown user err = %v, want ErrUserNotFound", err)
	}

	disabled := true
	if _, err := store.UpdateUser(ctx, user.ID, model.UserUpdate{Disabled: &disabled}); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if principal, err := store.LookupToken(ctx, auth.HashToken(plaintext)); err != nil || principal != nil {
		t.Errorf("disabled user token = %+v, err = %v; want no principal", principal, err)
	}

	tokens, err := store.Tokens(ctx, user.ID)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0].ID != token.ID || tokens[0].Scopes[0] != "read:positions" {
		t.Fatalf("tokens = %+v", tokens)
	}

	deviceID, err := store.Register("ADM"+suffix, "gt06")
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM devices WHERE id = $1`, deviceID)
	})
	if err := store.AssignDevice(ctx, user.ID, deviceID); err != nil {
		t.Fatalf("assign: %v", err)
	}
	deviceIDs, err := store.UserDevices(ctx, user.ID)
	if err != nil {
		t.Fatalf("user devices: %v", err)
	}
	if len(deviceIDs) != 1 || deviceIDs[0] != deviceID {
		t.Fatalf("user devices = %v", deviceIDs)
	}
	if err := store.UnassignDevice(ctx, user.ID, deviceID); err != nil {
		t.Fatalf("unassign: %v", err)
	}
	if deviceIDs, err = store.UserDevices(ctx, user.ID); err != nil || len(deviceIDs) != 0 {
		t.Fatalf("user devices after unassign = %v, err = %v", deviceIDs, err)
	}

	if err := store.RevokeToken(ctx, user.ID, token.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := store.RevokeToken(ctx, user.ID, token.ID); !errors.Is(err, model.ErrTokenNotFound) {
		t.Errorf("second revoke err = %v, want ErrTokenNotFound", err)
	}

	if err := store.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if _, err := store.User(ctx, user.ID); !errors.Is(err, model.ErrUserNotFound) {
		t.Errorf("user after delete err = %v, want ErrUserNotFound", err)
	}
	if err := store.DeleteUser(ctx, user.ID); !errors.Is(err, model.ErrUserNotFound) {
		t.Errorf("second delete err = %v, want ErrUserNotFound", err)
	}
}
