package storage

import (
	"context"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
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

// TestExpiringQueriesStoreIntegration covers the windows the expiration notices
// read and the permission walk that decides who receives a device notice.
func TestExpiringQueriesStoreIntegration(t *testing.T) {
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
	now := time.Now().UTC().Truncate(time.Second)

	inside := now.Add(-10 * time.Minute)
	outside := now.Add(48 * time.Hour)

	makeUser := func(label string, expires time.Time) *model.User {
		user, err := store.CreateUser(ctx, model.UserCreate{
			Email:      "notice-" + label + "-" + suffix + "@example.test",
			Attributes: model.Attributes{"expirationTime": expires.Format(time.RFC3339)},
		})
		if err != nil {
			t.Fatalf("create user %s: %v", label, err)
		}
		return user
	}
	inWindow := makeUser("inside", inside)
	outOfWindow := makeUser("outside", outside)

	users, err := store.UsersExpiringBetween(ctx, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("UsersExpiringBetween: %v", err)
	}
	found := map[int64]bool{}
	for _, user := range users {
		found[user.ID] = true
	}
	if !found[inWindow.ID] {
		t.Error("the account expiring inside the window was not returned")
	}
	if found[outOfWindow.ID] {
		t.Error("the account expiring outside the window was returned")
	}

	parent := &model.Group{Name: "notice-parent-" + suffix}
	child := &model.Group{Name: "notice-child-" + suffix}
	if err := store.CreateGroup(ctx, parent); err != nil {
		t.Fatalf("create parent group: %v", err)
	}
	child.GroupID = parent.ID
	if err := store.CreateGroup(ctx, child); err != nil {
		t.Fatalf("create child group: %v", err)
	}

	device, err := store.CreateDevice(ctx, model.DeviceCreate{
		UniqueID:       "notice-device-" + suffix,
		Name:           "notice device",
		GroupID:        child.ID,
		ExpirationTime: inside,
	})
	if err != nil {
		t.Fatalf("create device: %v", err)
	}

	direct := makeUser("direct", outside)
	inherited := makeUser("inherited", outside)
	unrelated := makeUser("unrelated", outside)

	if err := store.AddPermission(ctx, permissions.Permission{
		Owner:      permissions.ObjectUser,
		OwnerID:    direct.ID,
		Property:   permissions.ObjectDevice,
		PropertyID: device.ID,
	}); err != nil {
		t.Fatalf("grant device: %v", err)
	}
	if err := store.AddPermission(ctx, permissions.Permission{
		Owner:      permissions.ObjectUser,
		OwnerID:    inherited.ID,
		Property:   permissions.ObjectGroup,
		PropertyID: parent.ID,
	}); err != nil {
		t.Fatalf("grant parent group: %v", err)
	}

	granted, err := store.DeviceUsers(ctx, device.ID)
	if err != nil {
		t.Fatalf("DeviceUsers: %v", err)
	}
	got := map[int64]bool{}
	for _, user := range granted {
		got[user.ID] = true
	}
	if !got[direct.ID] {
		t.Error("the account with a direct grant was not returned")
	}
	if !got[inherited.ID] {
		t.Error("the account with a grant on the parent group was not returned")
	}
	if got[unrelated.ID] {
		t.Error("an account without a grant was returned")
	}

	devices, err := store.DevicesExpiringBetween(ctx, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("DevicesExpiringBetween: %v", err)
	}
	foundDevice := false
	for _, candidate := range devices {
		if candidate.ID == device.ID {
			foundDevice = true
		}
	}
	if !foundDevice {
		t.Error("the device expiring inside the window was not returned")
	}
}
