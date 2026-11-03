package storage

import (
	"context"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// TestTemporaryCleanupStoreIntegration covers the query behind the scheduled
// cleanup of share accounts. It runs only when TRACCAR_POSTGRES_DSN is set.
func TestTemporaryCleanupStoreIntegration(t *testing.T) {
	dsn := testDSN()
	if dsn == "" {
		t.Skip("TRACCAR_TEST_DSN (or TRACCAR_POSTGRES_DSN) is not set")
	}

	ctx := context.Background()
	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	suffix := time.Now().Format("150405.000000")
	past := time.Now().Add(-time.Hour).UTC()
	future := time.Now().Add(time.Hour).UTC()

	create := func(label string, temporary bool, expiration *time.Time) *model.User {
		attributes := model.Attributes{}
		if expiration != nil {
			attributes["expirationTime"] = expiration.Format(time.RFC3339)
		}
		user, err := store.CreateUser(ctx, model.UserCreate{
			Email:      "temporary-" + label + "-" + suffix + "@example.test",
			Temporary:  temporary,
			Attributes: attributes,
		})
		if err != nil {
			t.Fatalf("create %s account: %v", label, err)
		}
		t.Cleanup(func() {
			_, _ = store.db.ExecContext(context.Background(),
				`DELETE FROM users WHERE id = $1`, user.ID)
		})
		return user
	}

	expired := create("expired", true, &past)
	futureAccount := create("future", true, &future)
	ordinary := create("ordinary", false, &past)

	if got := expired.Attributes["expirationTime"]; got != past.Format(time.RFC3339) {
		t.Errorf("expirationTime = %v, want %s", got, past.Format(time.RFC3339))
	}

	found := func(users []model.User, id int64) bool {
		for _, user := range users {
			if user.ID == id {
				return true
			}
		}
		return false
	}

	users, err := store.ExpiredTemporaryUsers(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("expired temporary users: %v", err)
	}
	if !found(users, expired.ID) {
		t.Errorf("the expired share account %d was not selected", expired.ID)
	}
	if found(users, futureAccount.ID) {
		t.Errorf("the unexpired share account %d was selected", futureAccount.ID)
	}
	if found(users, ordinary.ID) {
		t.Errorf("the ordinary account %d was selected", ordinary.ID)
	}

	if err := store.DeleteUser(ctx, expired.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	users, err = store.ExpiredTemporaryUsers(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("expired temporary users: %v", err)
	}
	if found(users, expired.ID) {
		t.Errorf("account %d is still selected after the delete", expired.ID)
	}
}
