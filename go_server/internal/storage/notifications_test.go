package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

// TestNotificationStoreIntegration covers notifications / user_notifications
// and the dispatcher's DeviceSubscriptions query. It runs only when
// TRACCAR_POSTGRES_DSN is set.
func TestNotificationStoreIntegration(t *testing.T) {
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
	user, err := store.CreateUser(ctx, model.UserCreate{
		Email: "notify-" + suffix + "@example.test",
		Name:  "Notify",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID)
	})

	deviceID, err := store.Register("NTF"+suffix, "gt06")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM devices WHERE id = $1`, deviceID)
	})
	if err := store.AssignDevice(ctx, user.ID, deviceID); err != nil {
		t.Fatalf("assign device: %v", err)
	}

	created, err := store.CreateNotification(ctx, model.NotificationCreate{
		Type:         model.EventDeviceOverspeed,
		Notificators: []string{"mail", "webhook"},
		Description:  "fast",
	})
	if err != nil {
		t.Fatalf("create notification: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(),
			`DELETE FROM notifications WHERE id = $1`, created.ID)
	})
	if created.ID == 0 || created.Type != model.EventDeviceOverspeed ||
		strings.Join(created.Notificators, ",") != "mail,webhook" ||
		created.Description != "fast" || created.CreatedAt.IsZero() {
		t.Fatalf("created = %+v", created)
	}

	listed, err := store.Notifications(ctx)
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	if !containsNotification(listed, created.ID) {
		t.Errorf("notification %d is missing from the list", created.ID)
	}

	updated, err := store.UpdateNotification(ctx, created.ID, model.NotificationUpdate{
		Type:         stringPointer(model.EventPowerCut),
		Notificators: &[]string{"mail"},
	})
	if err != nil {
		t.Fatalf("update notification: %v", err)
	}
	if updated.Type != model.EventPowerCut || strings.Join(updated.Notificators, ",") != "mail" ||
		updated.Description != "fast" {
		t.Errorf("updated = %+v", updated)
	}
	if _, err := store.Notification(ctx, created.ID+1000); !errors.Is(err, model.ErrNotificationNotFound) {
		t.Errorf("unknown notification err = %v, want ErrNotificationNotFound", err)
	}

	// A disabled account must not receive anything.
	if err := store.AddPermission(ctx, permissions.Permission{
		Owner: permissions.ObjectDevice, OwnerID: deviceID,
		Property: permissions.ObjectNotification, PropertyID: created.ID,
	}); err != nil {
		t.Fatalf("link notification to device: %v", err)
	}
	if err := store.AssignNotification(ctx, user.ID, created.ID); err != nil {
		t.Fatalf("assign notification: %v", err)
	}
	if err := store.AssignNotification(ctx, user.ID, created.ID); err != nil {
		t.Fatalf("assign notification twice: %v", err)
	}

	subscriptions, err := store.DeviceSubscriptions(ctx, deviceID, model.EventPowerCut)
	if err != nil {
		t.Fatalf("device subscriptions: %v", err)
	}
	if len(subscriptions) != 1 || subscriptions[0].UserID != user.ID ||
		subscriptions[0].Email != user.Email ||
		strings.Join(subscriptions[0].Notificators, ",") != "mail" {
		t.Fatalf("subscriptions = %+v", subscriptions)
	}

	// A wildcard preset is a second subscription of the same user; the
	// dispatcher decides what to do with each row.
	wildcard, err := store.CreateNotification(ctx, model.NotificationCreate{
		Type:         "*",
		Notificators: []string{"webhook"},
	})
	if err != nil {
		t.Fatalf("create wildcard: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(),
			`DELETE FROM notifications WHERE id = $1`, wildcard.ID)
	})
	if err := store.AddPermission(ctx, permissions.Permission{
		Owner: permissions.ObjectDevice, OwnerID: deviceID,
		Property: permissions.ObjectNotification, PropertyID: wildcard.ID,
	}); err != nil {
		t.Fatalf("link wildcard to device: %v", err)
	}
	if err := store.AssignNotification(ctx, user.ID, wildcard.ID); err != nil {
		t.Fatalf("assign wildcard: %v", err)
	}

	subscriptions, err = store.DeviceSubscriptions(ctx, deviceID, model.EventGeofenceEnter)
	if err != nil {
		t.Fatalf("device subscriptions: %v", err)
	}
	if len(subscriptions) != 1 || strings.Join(subscriptions[0].Notificators, ",") != "webhook" {
		t.Fatalf("wildcard subscriptions = %+v", subscriptions)
	}

	subscriptions, err = store.DeviceSubscriptions(ctx, deviceID, model.EventPowerCut)
	if err != nil {
		t.Fatalf("device subscriptions: %v", err)
	}
	if len(subscriptions) != 2 ||
		strings.Join(subscriptions[0].Notificators, ",") != "mail" ||
		strings.Join(subscriptions[1].Notificators, ",") != "webhook" {
		t.Fatalf("subscriptions = %+v", subscriptions)
	}

	userNotifications, err := store.UserNotifications(ctx, user.ID)
	if err != nil {
		t.Fatalf("user notifications: %v", err)
	}
	if len(userNotifications) != 2 {
		t.Fatalf("user notifications = %+v", userNotifications)
	}

	disabled := true
	if _, err := store.UpdateUser(ctx, user.ID, model.UserUpdate{Disabled: &disabled}); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	subscriptions, err = store.DeviceSubscriptions(ctx, deviceID, model.EventPowerCut)
	if err != nil {
		t.Fatalf("device subscriptions after disable: %v", err)
	}
	if len(subscriptions) != 0 {
		t.Errorf("disabled user still has subscriptions: %+v", subscriptions)
	}

	if err := store.UnassignNotification(ctx, user.ID, wildcard.ID); err != nil {
		t.Fatalf("unassign wildcard: %v", err)
	}
	if err := store.UnassignNotification(ctx, user.ID, wildcard.ID); err != nil {
		t.Fatalf("unassign wildcard twice: %v", err)
	}

	if err := store.DeleteNotification(ctx, created.ID); err != nil {
		t.Fatalf("delete notification: %v", err)
	}
	if err := store.DeleteNotification(ctx, created.ID); !errors.Is(err, model.ErrNotificationNotFound) {
		t.Errorf("second delete err = %v, want ErrNotificationNotFound", err)
	}
}

func containsNotification(notifications []model.Notification, id int64) bool {
	for _, notification := range notifications {
		if notification.ID == id {
			return true
		}
	}
	return false
}

func stringPointer(value string) *string { return &value }

func TestAppendUniqueStrings(t *testing.T) {
	values := appendUniqueStrings(nil, "mail", "webhook", "mail")
	if strings.Join(values, ",") != "mail,webhook" {
		t.Errorf("values = %v", values)
	}
	values = appendUniqueStrings(values, "webhook", "sms")
	if strings.Join(values, ",") != "mail,webhook,sms" {
		t.Errorf("values = %v", values)
	}
}

func TestEncodeNotificators(t *testing.T) {
	encoded, err := encodeNotificators([]string{"mail", "webhook"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if encoded != `["mail","webhook"]` {
		t.Errorf("encoded = %s", encoded)
	}

	encoded, err = encodeNotificators(nil)
	if err != nil {
		t.Fatalf("encode nil: %v", err)
	}
	if encoded != `[]` {
		t.Errorf("encoded nil = %s, want []", encoded)
	}
}
