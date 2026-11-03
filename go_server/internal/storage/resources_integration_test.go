package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
)

// requireResourceStore opens the database the resource CRUD tests run against,
// or skips when TRACCAR_TEST_DSN (or TRACCAR_POSTGRES_DSN) is not set, like every other integration
// test in this package.
func requireResourceStore(t *testing.T) *Store {
	t.Helper()
	dsn := testDSN()
	if dsn == "" {
		t.Skip("TRACCAR_TEST_DSN (or TRACCAR_POSTGRES_DSN) is not set")
	}
	store, err := New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func uniqueName(prefix string) string {
	return prefix + "-" + time.Now().Format("150405.000000")
}

// TestGroupCRUDIntegration walks one group through create, read, update and
// delete against a real database.
func TestGroupCRUDIntegration(t *testing.T) {
	store := requireResourceStore(t)
	ctx := context.Background()
	name := uniqueName("group")

	parent := &model.Group{Name: uniqueName("parent"), Attributes: model.Attributes{"color": "red"}}
	if err := store.CreateGroup(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteGroup(context.Background(), parent.ID) })

	group := &model.Group{Name: name, GroupID: parent.ID, Attributes: model.Attributes{"color": "blue"}}
	if err := store.CreateGroup(ctx, group); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteGroup(context.Background(), group.ID) })
	if group.ID == 0 {
		t.Fatal("create did not fill the id")
	}

	found, err := store.Groups(ctx, model.ResourceFilter{IDs: []int64{group.ID}})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(found) != 1 || found[0].Name != name || found[0].GroupID != parent.ID {
		t.Fatalf("listed %#v, want %q under %d", found, name, parent.ID)
	}
	if found[0].Attributes.GetString("color", "") != "blue" {
		t.Errorf("attributes = %#v, want color blue", found[0].Attributes)
	}

	group.Name = name + "-renamed"
	group.GroupID = 0
	group.Attributes.Set("color", "green")
	if err := store.UpdateGroup(ctx, group); err != nil {
		t.Fatalf("update: %v", err)
	}
	found, err = store.Groups(ctx, model.ResourceFilter{IDs: []int64{group.ID}})
	if err != nil {
		t.Fatalf("list after update: %v", err)
	}
	if len(found) != 1 || found[0].Name != group.Name || found[0].GroupID != 0 {
		t.Fatalf("after update = %#v", found)
	}
	if found[0].Attributes.GetString("color", "") != "green" {
		t.Errorf("attributes = %#v, want color green", found[0].Attributes)
	}

	keyword, err := store.Groups(ctx, model.ResourceFilter{Keyword: strings.ToUpper(name)})
	if err != nil {
		t.Fatalf("keyword: %v", err)
	}
	if len(keyword) != 1 || keyword[0].ID != group.ID {
		t.Errorf("keyword search found %#v, want group %d", keyword, group.ID)
	}

	if err := store.DeleteGroup(ctx, group.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.DeleteGroup(ctx, group.ID); !errors.Is(err, model.ErrResourceNotFound) {
		t.Errorf("second delete = %v, want ErrResourceNotFound", err)
	}
	if found, err := store.Groups(ctx, model.ResourceFilter{IDs: []int64{group.ID}}); err != nil || len(found) != 0 {
		t.Errorf("after delete found %#v, err %v", found, err)
	}
}

func TestCalendarCRUDIntegration(t *testing.T) {
	store := requireResourceStore(t)
	ctx := context.Background()

	calendar := &model.Calendar{
		Name:       uniqueName("calendar"),
		Data:       []byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"),
		Attributes: model.Attributes{},
	}
	if err := store.CreateCalendar(ctx, calendar); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteCalendar(context.Background(), calendar.ID) })

	found, err := store.Calendars(ctx, model.ResourceFilter{IDs: []int64{calendar.ID}})
	if err != nil || len(found) != 1 {
		t.Fatalf("list = %#v, err %v", found, err)
	}
	if string(found[0].Data) != string(calendar.Data) {
		t.Errorf("data = %q, want the iCalendar document back", found[0].Data)
	}

	calendar.Data = []byte("BEGIN:VCALENDAR\r\nX-CHANGED:1\r\nEND:VCALENDAR\r\n")
	if err := store.UpdateCalendar(ctx, calendar); err != nil {
		t.Fatalf("update: %v", err)
	}
	found, err = store.Calendars(ctx, model.ResourceFilter{IDs: []int64{calendar.ID}})
	if err != nil || len(found) != 1 || !strings.Contains(string(found[0].Data), "X-CHANGED") {
		t.Fatalf("after update = %#v, err %v", found, err)
	}

	if err := store.DeleteCalendar(ctx, calendar.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestDriverCRUDIntegration(t *testing.T) {
	store := requireResourceStore(t)
	ctx := context.Background()

	driver := &model.Driver{Name: uniqueName("driver"), UniqueID: "  ABC123  "}
	if err := store.CreateDriver(ctx, driver); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteDriver(context.Background(), driver.ID) })
	if driver.UniqueID != "ABC123" {
		t.Errorf("unique id = %q, want the trimmed value", driver.UniqueID)
	}

	found, err := store.Drivers(ctx, model.ResourceFilter{IDs: []int64{driver.ID}})
	if err != nil || len(found) != 1 {
		t.Fatalf("list = %#v, err %v", found, err)
	}
	if found[0].UniqueID != "ABC123" {
		t.Errorf("stored unique id = %q, want ABC123", found[0].UniqueID)
	}

	driver.Name = uniqueName("driver-renamed")
	driver.UniqueID = "XYZ"
	if err := store.UpdateDriver(ctx, driver); err != nil {
		t.Fatalf("update: %v", err)
	}
	found, err = store.Drivers(ctx, model.ResourceFilter{IDs: []int64{driver.ID}})
	if err != nil || len(found) != 1 || found[0].UniqueID != "XYZ" {
		t.Fatalf("after update = %#v, err %v", found, err)
	}

	if err := store.DeleteDriver(ctx, driver.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestMaintenanceCRUDIntegration(t *testing.T) {
	store := requireResourceStore(t)
	ctx := context.Background()

	maintenance := &model.Maintenance{
		Name: uniqueName("maintenance"), Type: "totalDistance",
		Start: 1000, Period: 5000,
		Attributes: model.Attributes{"reset": true},
	}
	if err := store.CreateMaintenance(ctx, maintenance); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteMaintenance(context.Background(), maintenance.ID) })

	found, err := store.Maintenances(ctx, model.ResourceFilter{IDs: []int64{maintenance.ID}})
	if err != nil || len(found) != 1 {
		t.Fatalf("list = %#v, err %v", found, err)
	}
	if found[0].Start != 1000 || found[0].Period != 5000 || !found[0].Attributes.GetBoolean("reset") {
		t.Errorf("stored maintenance = %#v", found[0])
	}

	maintenance.Period = 7500
	if err := store.UpdateMaintenance(ctx, maintenance); err != nil {
		t.Fatalf("update: %v", err)
	}
	found, err = store.Maintenances(ctx, model.ResourceFilter{IDs: []int64{maintenance.ID}})
	if err != nil || len(found) != 1 || found[0].Period != 7500 {
		t.Fatalf("after update = %#v, err %v", found, err)
	}

	if err := store.DeleteMaintenance(ctx, maintenance.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestOrderCRUDIntegration(t *testing.T) {
	store := requireResourceStore(t)
	ctx := context.Background()

	order := &model.Order{
		UniqueID: uniqueName("order"), Description: "deliver",
		FromAddress: "A", ToAddress: "B",
	}
	if err := store.CreateOrder(ctx, order); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteOrder(context.Background(), order.ID) })

	found, err := store.Orders(ctx, model.ResourceFilter{IDs: []int64{order.ID}})
	if err != nil || len(found) != 1 {
		t.Fatalf("list = %#v, err %v", found, err)
	}
	if found[0].FromAddress != "A" || found[0].ToAddress != "B" {
		t.Errorf("stored order = %#v", found[0])
	}

	order.ToAddress = "C"
	if err := store.UpdateOrder(ctx, order); err != nil {
		t.Fatalf("update: %v", err)
	}
	found, err = store.Orders(ctx, model.ResourceFilter{IDs: []int64{order.ID}})
	if err != nil || len(found) != 1 || found[0].ToAddress != "C" {
		t.Fatalf("after update = %#v, err %v", found, err)
	}

	if err := store.DeleteOrder(ctx, order.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestReportCRUDIntegration(t *testing.T) {
	store := requireResourceStore(t)
	ctx := context.Background()

	calendar := &model.Calendar{Name: uniqueName("report-calendar")}
	if err := store.CreateCalendar(ctx, calendar); err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteCalendar(context.Background(), calendar.ID) })

	report := &model.Report{
		CalendarID: calendar.ID, Type: "summary", Description: uniqueName("report"),
	}
	if err := store.CreateReport(ctx, report); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteReport(context.Background(), report.ID) })

	found, err := store.Reports(ctx, model.ResourceFilter{IDs: []int64{report.ID}})
	if err != nil || len(found) != 1 {
		t.Fatalf("list = %#v, err %v", found, err)
	}
	if found[0].CalendarID != calendar.ID || found[0].Type != "summary" {
		t.Errorf("stored report = %#v", found[0])
	}

	report.Type = "trips"
	report.CalendarID = 0
	if err := store.UpdateReport(ctx, report); err != nil {
		t.Fatalf("update: %v", err)
	}
	found, err = store.Reports(ctx, model.ResourceFilter{IDs: []int64{report.ID}})
	if err != nil || len(found) != 1 || found[0].Type != "trips" || found[0].CalendarID != 0 {
		t.Fatalf("after update = %#v, err %v", found, err)
	}

	if err := store.DeleteReport(ctx, report.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestStatisticsCRUDIntegration(t *testing.T) {
	store := requireResourceStore(t)
	ctx := context.Background()

	capture := time.Now().UTC().Truncate(time.Millisecond)
	statistics := &model.Statistics{
		CaptureTime: capture,
		ActiveUsers: 3,
		Requests:    42,
		Protocols:   map[string]int{"gt06": 10, "h02": 4},
		Attributes:  model.Attributes{},
	}
	if err := store.CreateStatistics(ctx, statistics); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteStatistics(context.Background(), statistics.ID) })

	found, err := store.Statistics(ctx, model.ResourceFilter{IDs: []int64{statistics.ID}})
	if err != nil || len(found) != 1 {
		t.Fatalf("list = %#v, err %v", found, err)
	}
	if !found[0].CaptureTime.Equal(capture) {
		t.Errorf("capture time = %v, want %v", found[0].CaptureTime, capture)
	}
	if found[0].Protocols["gt06"] != 10 || found[0].Protocols["h02"] != 4 || found[0].Requests != 42 {
		t.Errorf("stored statistics = %#v", found[0])
	}

	statistics.Requests = 43
	if err := store.UpdateStatistics(ctx, statistics); err != nil {
		t.Fatalf("update: %v", err)
	}
	found, err = store.Statistics(ctx, model.ResourceFilter{IDs: []int64{statistics.ID}})
	if err != nil || len(found) != 1 || found[0].Requests != 43 {
		t.Fatalf("after update = %#v, err %v", found, err)
	}

	if err := store.DeleteStatistics(ctx, statistics.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestComputedAttributeCRUDIntegration(t *testing.T) {
	store := requireResourceStore(t)
	ctx := context.Background()

	attribute := &model.Attribute{
		Description: uniqueName("computed"), Attribute: "driverName",
		Expression: "'unknown'", Type: "string", Priority: 10,
	}
	if err := store.CreateAttribute(ctx, attribute); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteAttribute(context.Background(), attribute.ID) })

	found, err := store.Attributes(ctx, model.ResourceFilter{IDs: []int64{attribute.ID}})
	if err != nil || len(found) != 1 {
		t.Fatalf("list = %#v, err %v", found, err)
	}
	if found[0].Expression != "'unknown'" || found[0].Priority != 10 {
		t.Errorf("stored attribute = %#v", found[0])
	}

	attribute.Priority = 20
	if err := store.UpdateAttribute(ctx, attribute); err != nil {
		t.Fatalf("update: %v", err)
	}
	found, err = store.Attributes(ctx, model.ResourceFilter{IDs: []int64{attribute.ID}})
	if err != nil || len(found) != 1 || found[0].Priority != 20 {
		t.Fatalf("after update = %#v, err %v", found, err)
	}

	if err := store.DeleteAttribute(ctx, attribute.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestGeofenceCRUDIntegration(t *testing.T) {
	store := requireResourceStore(t)
	ctx := context.Background()

	area, err := geofence.Parse("CIRCLE (54.94535 24.01762, 150)")
	if err != nil {
		t.Fatalf("parse area: %v", err)
	}
	fence := &geofence.Geofence{
		Name: uniqueName("geofence"), Description: "depot", Area: area,
		Attributes: model.Attributes{"floor": 10.0, "ceiling": 500.0},
	}
	if err := store.CreateGeofence(ctx, fence); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteGeofence(context.Background(), fence.ID) })

	found, err := store.QueryGeofences(ctx, model.ResourceFilter{IDs: []int64{fence.ID}})
	if err != nil || len(found) != 1 {
		t.Fatalf("list = %#v, err %v", found, err)
	}
	if found[0].Area == nil || found[0].Description != "depot" {
		t.Errorf("stored geofence = %#v", found[0])
	}
	if found[0].Attributes.GetDouble("floor", 0) != 10 {
		t.Errorf("floor = %v, want 10", found[0].Attributes.GetDouble("floor", 0))
	}

	fence.Description = "warehouse"
	fence.Attributes.Set("floor", 20.0)
	if err := store.UpdateGeofence(ctx, fence); err != nil {
		t.Fatalf("update: %v", err)
	}
	found, err = store.QueryGeofences(ctx, model.ResourceFilter{IDs: []int64{fence.ID}})
	if err != nil || len(found) != 1 || found[0].Description != "warehouse" {
		t.Fatalf("after update = %#v, err %v", found, err)
	}
	if found[0].Attributes.GetDouble("floor", 0) != 20 {
		t.Errorf("floor after update = %v, want 20", found[0].Attributes.GetDouble("floor", 0))
	}

	if err := store.DeleteGeofence(ctx, fence.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.UpdateGeofence(ctx, fence); !errors.Is(err, model.ErrResourceNotFound) {
		t.Errorf("update after delete = %v, want ErrResourceNotFound", err)
	}
}
