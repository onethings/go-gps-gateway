package storage

import (
	"context"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

// TestAdminStoreIntegration exercises the statements that the CRUD tests do not
// reach: the server settings row, protocol settings, audit rows, statistics,
// maintenance links, media and the retention sweeps. It runs on whichever
// dialect TRACCAR_TEST_DSN names, so a new dialect cannot silently keep a
// PostgreSQL-only query.
func TestAdminStoreIntegration(t *testing.T) {
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

	settings, err := store.ServerSettings(ctx)
	if err != nil {
		t.Fatalf("server settings: %v", err)
	}
	settings.Announcement = "hello-" + suffix
	settings.Map = "osm"
	settings.Attributes = model.Attributes{"test": suffix}
	updated, err := store.UpdateServerSettings(ctx, settings)
	if err != nil {
		t.Fatalf("update server settings: %v", err)
	}
	if updated.Announcement != settings.Announcement ||
		updated.Attributes["test"] != suffix {
		t.Fatalf("updated settings = %+v", updated)
	}

	protocol := "test-" + suffix
	if err := store.EnsureProtocolSettings(ctx, []model.ProtocolSettings{{
		Protocol: protocol, Port: 12000, Transport: model.TransportTCP,
	}}); err != nil {
		t.Fatalf("ensure protocol settings: %v", err)
	}
	if err := store.EnsureProtocolSettings(ctx, []model.ProtocolSettings{{
		Protocol: protocol, Port: 12001, Transport: model.TransportUDP,
	}}); err != nil {
		t.Fatalf("ensure protocol settings again: %v", err)
	}
	protocols, err := store.ProtocolSettings(ctx)
	if err != nil {
		t.Fatalf("protocol settings: %v", err)
	}
	var protocolID int64
	for _, row := range protocols {
		if row.Protocol == protocol {
			protocolID = row.ID
			if row.Port != 12000 {
				t.Errorf("existing protocol was overwritten: %+v", row)
			}
		}
	}
	if protocolID == 0 {
		t.Fatalf("protocol %q is missing", protocol)
	}
	changed, err := store.UpdateProtocolSettings(ctx, protocolID, 12002, model.TransportUDP)
	if err != nil {
		t.Fatalf("update protocol settings: %v", err)
	}
	if changed.Port != 12002 || changed.Transport != model.TransportUDP {
		t.Fatalf("updated protocol = %+v", changed)
	}

	action := &model.Action{
		ActionTime: time.Now().UTC(),
		Address:    "127.0.0.1",
		ActionType: "test",
		Attributes: model.Attributes{"case": suffix},
	}
	if err := store.RecordAction(ctx, action); err != nil {
		t.Fatalf("record action: %v", err)
	}
	actions, err := store.QueryActions(ctx, time.Time{}, time.Time{}, 10, 0)
	if err != nil {
		t.Fatalf("query actions: %v", err)
	}
	foundAction := false
	for _, row := range actions {
		if row.ActionType == "test" && row.Attributes["case"] == suffix {
			foundAction = true
		}
	}
	if !foundAction {
		t.Fatalf("recorded action is missing: %+v", actions)
	}

	statistics := &model.Statistics{
		CaptureTime: time.Now().UTC(),
		Requests:    3,
		Protocols:   map[string]int{"gt06": 2},
		Attributes:  model.Attributes{"test": suffix},
	}
	if err := store.CreateStatistics(ctx, statistics); err != nil {
		t.Fatalf("create statistics: %v", err)
	}
	if statistics.ID == 0 {
		t.Fatal("statistics id was not filled")
	}
	snapshots, err := store.QueryStatistics(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("query statistics: %v", err)
	}
	foundSnapshot := false
	for _, snapshot := range snapshots {
		if snapshot.ID == statistics.ID {
			foundSnapshot = true
			if snapshot.Protocols["gt06"] != 2 {
				t.Errorf("protocols = %v", snapshot.Protocols)
			}
		}
	}
	if !foundSnapshot {
		t.Fatalf("created statistics %d is missing", statistics.ID)
	}

	device, err := store.CreateDevice(ctx, model.DeviceCreate{
		UniqueID: "ADM" + suffix,
		Name:     "admin",
		Protocol: "gt06",
	})
	if err != nil {
		t.Fatalf("create device: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM devices WHERE id = $1`, device.ID)
	})
	if _, err := store.Register(device.UniqueID, "gt06"); err != nil {
		t.Fatalf("register existing device: %v", err)
	}

	maintenance := &model.Maintenance{
		Name: "service-" + suffix, Type: "distance", Start: 1, Period: 2,
		Attributes: model.Attributes{"test": suffix},
	}
	if err := store.CreateMaintenance(ctx, maintenance); err != nil {
		t.Fatalf("create maintenance: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM maintenances WHERE id = $1`, maintenance.ID)
	})
	if err := store.AddPermission(ctx, permissions.Permission{
		Owner: permissions.ObjectDevice, OwnerID: device.ID,
		Property: permissions.ObjectMaintenance, PropertyID: maintenance.ID,
	}); err != nil {
		t.Fatalf("link maintenance: %v", err)
	}
	maintenances, err := store.DeviceMaintenances(ctx)
	if err != nil {
		t.Fatalf("device maintenances: %v", err)
	}
	if len(maintenances[device.ID]) != 1 || maintenances[device.ID][0].Start != 1 {
		t.Fatalf("device maintenances = %+v", maintenances[device.ID])
	}

	incurred := time.Now().UTC()
	position := samplePosition()
	position.DeviceID = device.ID
	position.Time = incurred
	if err := store.Handle(position); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	counts, err := store.PositionCounts(ctx, []int64{device.ID}, incurred.Add(-time.Hour), incurred.Add(time.Hour))
	if err != nil {
		t.Fatalf("position counts: %v", err)
	}
	if counts[device.ID] != 1 {
		t.Errorf("position counts = %v", counts)
	}
	latest, err := store.LatestPositions(ctx, []int64{device.ID})
	if err != nil {
		t.Fatalf("latest positions: %v", err)
	}
	if len(latest) != 1 || latest[0].DeviceID != device.ID {
		t.Fatalf("latest positions = %+v", latest)
	}
	if _, _, err := store.OnlineCounts(ctx); err != nil {
		t.Fatalf("online counts: %v", err)
	}

	media := &model.MediaFile{
		DeviceID: device.ID,
		FileName: "media-" + suffix + ".jpg",
		Type:     model.MediaTypeImage,
		Size:     12,
		Attributes: model.Attributes{
			"test": suffix,
		},
	}
	if err := store.CreateMediaFile(ctx, media); err != nil {
		t.Fatalf("create media: %v", err)
	}
	if media.ID == 0 {
		t.Fatal("media id was not filled")
	}
	files, err := store.MediaFiles(ctx, device.ID, incurred.Add(-time.Hour), incurred.Add(time.Hour))
	if err != nil {
		t.Fatalf("query media: %v", err)
	}
	if len(files) != 1 || files[0].FileName != media.FileName {
		t.Fatalf("media files = %+v", files)
	}
	if err := store.DeleteMediaFile(ctx, media.ID); err != nil {
		t.Fatalf("delete media: %v", err)
	}

	past := time.Now().UTC().Add(-time.Hour)
	expiredUser, err := store.CreateUser(ctx, model.UserCreate{
		Email:      "expired-" + suffix + "@example.test",
		Name:       "expired",
		Attributes: model.Attributes{"expirationTime": past.Format(time.RFC3339)},
	})
	if err != nil {
		t.Fatalf("create expired user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, expiredUser.ID)
	})
	expired, err := store.ExpiredUsers(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("expired users: %v", err)
	}
	foundExpired := false
	for _, user := range expired {
		if user.ID == expiredUser.ID {
			foundExpired = true
		}
	}
	if !foundExpired {
		t.Fatalf("expired user %d is missing", expiredUser.ID)
	}
	if err := store.SetUserDisabled(ctx, expiredUser.ID, true); err != nil {
		t.Fatalf("disable user: %v", err)
	}

	if _, _, err := store.IssueTokenForUser(ctx, expiredUser.ID, []string{"websocket"}, &past); err != nil {
		t.Fatalf("issue expired token: %v", err)
	}
	if deleted, err := store.DeleteExpiredTokens(ctx, time.Now().UTC()); err != nil || deleted < 1 {
		t.Fatalf("delete expired tokens = %d, %v", deleted, err)
	}
	if err := store.CreatePasswordReset(ctx, expiredUser.ID, "reset-"+suffix, past); err != nil {
		t.Fatalf("create expired reset: %v", err)
	}
	if deleted, err := store.DeleteExpiredPasswordResets(ctx, time.Now().UTC()); err != nil || deleted < 1 {
		t.Fatalf("delete expired resets = %d, %v", deleted, err)
	}

	oldAction := &model.Action{
		ActionTime: time.Now().UTC().Add(-24 * time.Hour),
		ActionType: "old",
	}
	if err := store.RecordAction(ctx, oldAction); err != nil {
		t.Fatalf("record old action: %v", err)
	}
	if deleted, err := store.DeleteOldActions(ctx, time.Now().UTC().Add(-time.Hour)); err != nil || deleted < 1 {
		t.Fatalf("delete old actions = %d, %v", deleted, err)
	}

	if deleted, err := store.DeletePositionsBefore(ctx, time.Now().UTC().Add(time.Hour)); err != nil || deleted < 1 {
		t.Fatalf("delete positions = %d, %v", deleted, err)
	}
	if deleted, err := store.DeleteEventsBefore(ctx, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("delete events = %d, %v", deleted, err)
	}

	expiring, err := store.CreateDevice(ctx, model.DeviceCreate{
		UniqueID:       "EXP" + suffix,
		Name:           "expiring",
		Protocol:       "gt06",
		ExpirationTime: past,
	})
	if err != nil {
		t.Fatalf("create expiring device: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM devices WHERE id = $1`, expiring.ID)
	})
	if _, err := store.DisableExpiredDevices(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("disable expired devices: %v", err)
	}
	reloaded, err := store.Device(ctx, expiring.ID)
	if err != nil {
		t.Fatalf("reload expiring device: %v", err)
	}
	if !reloaded.Disabled {
		t.Fatal("expired device was not disabled")
	}
}
