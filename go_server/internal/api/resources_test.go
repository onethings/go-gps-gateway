package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

// fakeResources is an in-memory ResourceStore. It stores each table in a slice,
// numbers rows from one counter and records every filter it was asked for, so a
// handler test can assert both the JSON it produced and the query it ran.
type fakeResources struct {
	mu           sync.Mutex
	nextID       int64
	groups       []*model.Group
	geofences    []*geofence.Geofence
	calendars    []*model.Calendar
	drivers      []*model.Driver
	maintenances []*model.Maintenance
	orders       []*model.Order
	reports      []*model.Report
	attributes   []*model.Attribute

	pairs        []permissions.Permission
	parents      map[int64]int64
	deviceGroups map[int64]int64

	filters  []model.ResourceFilter
	listErr  error
	grantErr error
}

func newFakeResources() *fakeResources {
	return &fakeResources{
		parents:      map[int64]int64{},
		deviceGroups: map[int64]int64{},
	}
}

func (f *fakeResources) recordFilter(filter model.ResourceFilter) {
	f.filters = append(f.filters, filter)
}

func (f *fakeResources) lastFilter(t *testing.T) model.ResourceFilter {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.filters) == 0 {
		t.Fatal("list was never called")
	}
	return f.filters[len(f.filters)-1]
}

func (f *fakeResources) add(t *testing.T, owner permissions.Object, ownerID int64, property permissions.Object, propertyID int64) {
	t.Helper()
	pair, err := permissions.New(owner, ownerID, property, propertyID)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pairs = append(f.pairs, pair)
}

func (f *fakeResources) snapshotPairs() []permissions.Permission {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]permissions.Permission(nil), f.pairs...)
}

func filterValues[T any](values []*T, filter model.ResourceFilter, id func(*T) int64) []*T {
	var wanted map[int64]struct{}
	if filter.IDs != nil {
		wanted = make(map[int64]struct{}, len(filter.IDs))
		for _, allow := range filter.IDs {
			wanted[allow] = struct{}{}
		}
	}
	kept := make([]*T, 0, len(values))
	for _, value := range values {
		if wanted != nil {
			if _, ok := wanted[id(value)]; !ok {
				continue
			}
		}
		kept = append(kept, value)
	}
	if filter.Offset > 0 {
		if filter.Offset >= len(kept) {
			return nil
		}
		kept = kept[filter.Offset:]
	}
	if filter.Limit > 0 && filter.Limit < len(kept) {
		kept = kept[:filter.Limit]
	}
	return kept
}

func replaceValue[T any](values []*T, value *T, id func(*T) int64) ([]*T, error) {
	for index, existing := range values {
		if id(existing) == id(value) {
			values[index] = value
			return values, nil
		}
	}
	return values, model.ErrResourceNotFound
}

func removeValue[T any](values []*T, id int64, objectID func(*T) int64) ([]*T, error) {
	for index, existing := range values {
		if objectID(existing) == id {
			return append(values[:index], values[index+1:]...), nil
		}
	}
	return values, model.ErrResourceNotFound
}

func (f *fakeResources) dropGrants(object permissions.Object, objectID int64) {
	kept := f.pairs[:0]
	for _, pair := range f.pairs {
		if pair.Property == object && pair.PropertyID == objectID {
			continue
		}
		kept = append(kept, pair)
	}
	f.pairs = kept
}

func (f *fakeResources) Groups(_ context.Context, filter model.ResourceFilter) ([]*model.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordFilter(filter)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return filterValues(f.groups, filter, func(group *model.Group) int64 { return group.ID }), nil
}

func (f *fakeResources) CreateGroup(_ context.Context, group *model.Group) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	group.ID = f.nextID
	f.groups = append(f.groups, group)
	if group.GroupID > 0 {
		f.parents[group.ID] = group.GroupID
	} else {
		f.parents[group.ID] = 0
	}
	return nil
}

func (f *fakeResources) UpdateGroup(_ context.Context, group *model.Group) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	updated, err := replaceValue(f.groups, group, func(item *model.Group) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.groups = updated
	f.parents[group.ID] = group.GroupID
	return nil
}

func (f *fakeResources) DeleteGroup(_ context.Context, groupID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	remaining, err := removeValue(f.groups, groupID, func(item *model.Group) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.groups = remaining
	delete(f.parents, groupID)
	f.dropGrants(permissions.ObjectGroup, groupID)
	return nil
}

func (f *fakeResources) QueryGeofences(_ context.Context, filter model.ResourceFilter) ([]*geofence.Geofence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordFilter(filter)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return filterValues(f.geofences, filter, func(fence *geofence.Geofence) int64 { return fence.ID }), nil
}

func (f *fakeResources) CreateGeofence(_ context.Context, fence *geofence.Geofence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	fence.ID = f.nextID
	f.geofences = append(f.geofences, fence)
	return nil
}

func (f *fakeResources) UpdateGeofence(_ context.Context, fence *geofence.Geofence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	updated, err := replaceValue(f.geofences, fence, func(item *geofence.Geofence) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.geofences = updated
	return nil
}

func (f *fakeResources) DeleteGeofence(_ context.Context, geofenceID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	remaining, err := removeValue(f.geofences, geofenceID, func(item *geofence.Geofence) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.geofences = remaining
	f.dropGrants(permissions.ObjectGeofence, geofenceID)
	return nil
}

func (f *fakeResources) Calendars(_ context.Context, filter model.ResourceFilter) ([]*model.Calendar, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordFilter(filter)
	return filterValues(f.calendars, filter, func(calendar *model.Calendar) int64 { return calendar.ID }), nil
}

func (f *fakeResources) CreateCalendar(_ context.Context, calendar *model.Calendar) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	calendar.ID = f.nextID
	f.calendars = append(f.calendars, calendar)
	return nil
}

func (f *fakeResources) UpdateCalendar(_ context.Context, calendar *model.Calendar) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	updated, err := replaceValue(f.calendars, calendar, func(item *model.Calendar) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.calendars = updated
	return nil
}

func (f *fakeResources) DeleteCalendar(_ context.Context, calendarID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	remaining, err := removeValue(f.calendars, calendarID, func(item *model.Calendar) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.calendars = remaining
	f.dropGrants(permissions.ObjectCalendar, calendarID)
	return nil
}

func (f *fakeResources) Drivers(_ context.Context, filter model.ResourceFilter) ([]*model.Driver, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordFilter(filter)
	return filterValues(f.drivers, filter, func(driver *model.Driver) int64 { return driver.ID }), nil
}

func (f *fakeResources) CreateDriver(_ context.Context, driver *model.Driver) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	driver.ID = f.nextID
	f.drivers = append(f.drivers, driver)
	return nil
}

func (f *fakeResources) UpdateDriver(_ context.Context, driver *model.Driver) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	updated, err := replaceValue(f.drivers, driver, func(item *model.Driver) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.drivers = updated
	return nil
}

func (f *fakeResources) DeleteDriver(_ context.Context, driverID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	remaining, err := removeValue(f.drivers, driverID, func(item *model.Driver) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.drivers = remaining
	f.dropGrants(permissions.ObjectDriver, driverID)
	return nil
}

func (f *fakeResources) Maintenances(_ context.Context, filter model.ResourceFilter) ([]*model.Maintenance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordFilter(filter)
	return filterValues(f.maintenances, filter, func(item *model.Maintenance) int64 { return item.ID }), nil
}

func (f *fakeResources) CreateMaintenance(_ context.Context, maintenance *model.Maintenance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	maintenance.ID = f.nextID
	f.maintenances = append(f.maintenances, maintenance)
	return nil
}

func (f *fakeResources) UpdateMaintenance(_ context.Context, maintenance *model.Maintenance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	updated, err := replaceValue(f.maintenances, maintenance, func(item *model.Maintenance) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.maintenances = updated
	return nil
}

func (f *fakeResources) DeleteMaintenance(_ context.Context, maintenanceID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	remaining, err := removeValue(f.maintenances, maintenanceID, func(item *model.Maintenance) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.maintenances = remaining
	f.dropGrants(permissions.ObjectMaintenance, maintenanceID)
	return nil
}

func (f *fakeResources) Orders(_ context.Context, filter model.ResourceFilter) ([]*model.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordFilter(filter)
	return filterValues(f.orders, filter, func(order *model.Order) int64 { return order.ID }), nil
}

func (f *fakeResources) CreateOrder(_ context.Context, order *model.Order) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	order.ID = f.nextID
	f.orders = append(f.orders, order)
	return nil
}

func (f *fakeResources) UpdateOrder(_ context.Context, order *model.Order) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	updated, err := replaceValue(f.orders, order, func(item *model.Order) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.orders = updated
	return nil
}

func (f *fakeResources) DeleteOrder(_ context.Context, orderID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	remaining, err := removeValue(f.orders, orderID, func(item *model.Order) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.orders = remaining
	f.dropGrants(permissions.ObjectOrder, orderID)
	return nil
}

func (f *fakeResources) Reports(_ context.Context, filter model.ResourceFilter) ([]*model.Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordFilter(filter)
	return filterValues(f.reports, filter, func(report *model.Report) int64 { return report.ID }), nil
}

func (f *fakeResources) CreateReport(_ context.Context, report *model.Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	report.ID = f.nextID
	f.reports = append(f.reports, report)
	return nil
}

func (f *fakeResources) UpdateReport(_ context.Context, report *model.Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	updated, err := replaceValue(f.reports, report, func(item *model.Report) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.reports = updated
	return nil
}

func (f *fakeResources) DeleteReport(_ context.Context, reportID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	remaining, err := removeValue(f.reports, reportID, func(item *model.Report) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.reports = remaining
	f.dropGrants(permissions.ObjectReport, reportID)
	return nil
}

func (f *fakeResources) Attributes(_ context.Context, filter model.ResourceFilter) ([]*model.Attribute, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordFilter(filter)
	return filterValues(f.attributes, filter, func(attribute *model.Attribute) int64 { return attribute.ID }), nil
}

func (f *fakeResources) CreateAttribute(_ context.Context, attribute *model.Attribute) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	attribute.ID = f.nextID
	f.attributes = append(f.attributes, attribute)
	return nil
}

func (f *fakeResources) UpdateAttribute(_ context.Context, attribute *model.Attribute) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	updated, err := replaceValue(f.attributes, attribute, func(item *model.Attribute) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.attributes = updated
	return nil
}

func (f *fakeResources) DeleteAttribute(_ context.Context, attributeID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	remaining, err := removeValue(f.attributes, attributeID, func(item *model.Attribute) int64 { return item.ID })
	if err != nil {
		return err
	}
	f.attributes = remaining
	f.dropGrants(permissions.ObjectAttribute, attributeID)
	return nil
}

func (f *fakeResources) UserGrants(_ context.Context, userID int64) ([]int64, []int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var devices, groups []int64
	for _, pair := range f.pairs {
		if pair.Owner != permissions.ObjectUser || pair.OwnerID != userID {
			continue
		}
		switch pair.Property {
		case permissions.ObjectDevice:
			devices = append(devices, pair.PropertyID)
		case permissions.ObjectGroup:
			groups = append(groups, pair.PropertyID)
		}
	}
	return devices, groups, nil
}

func (f *fakeResources) GroupParents(context.Context) (map[int64]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := make(map[int64]int64, len(f.parents))
	for id, parent := range f.parents {
		copied[id] = parent
	}
	return copied, nil
}

func (f *fakeResources) DevicesInGroups(_ context.Context, groupIDs []int64) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	wanted := make(map[int64]struct{}, len(groupIDs))
	for _, id := range groupIDs {
		wanted[id] = struct{}{}
	}
	var devices []int64
	for deviceID, groupID := range f.deviceGroups {
		if _, ok := wanted[groupID]; ok {
			devices = append(devices, deviceID)
		}
	}
	return devices, nil
}

func (f *fakeResources) DeviceGroup(_ context.Context, deviceID int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deviceGroups[deviceID], nil
}

func (f *fakeResources) Permissions(_ context.Context, owner permissions.Object, ownerID int64, property permissions.Object, propertyID int64) ([]permissions.Permission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var matched []permissions.Permission
	for _, pair := range f.pairs {
		if pair.Matches(owner, ownerID, property, propertyID) {
			matched = append(matched, pair)
		}
	}
	return matched, nil
}

func (f *fakeResources) AddPermission(_ context.Context, permission permissions.Permission) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.grantErr != nil {
		return f.grantErr
	}
	for _, pair := range f.pairs {
		if pair == permission {
			return nil
		}
	}
	f.pairs = append(f.pairs, permission)
	return nil
}

func (f *fakeResources) RemovePermission(_ context.Context, permission permissions.Permission) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.pairs[:0]
	for _, pair := range f.pairs {
		if pair != permission {
			kept = append(kept, pair)
		}
	}
	f.pairs = kept
	return nil
}

func decodeBodyValue[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode body %q: %v", recorder.Body, err)
	}
	return value
}

func decodeBodyList[T any](t *testing.T, recorder *httptest.ResponseRecorder) []T {
	t.Helper()
	var values []T
	if err := json.Unmarshal(recorder.Body.Bytes(), &values); err != nil {
		t.Fatalf("decode body %q: %v", recorder.Body, err)
	}
	return values
}

func TestResourceRoutesNeedStore(t *testing.T) {
	handler := NewServer(nil).Handler()

	recorder := get(t, handler, "/api/groups")
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestResourceListNeedsScope(t *testing.T) {
	store := newFakeResources()
	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadDevices}, nil)
	handler := NewServer(nil, WithResources(store), withTestToken(principal)).Handler()

	recorder := withToken(t, handler, http.MethodGet, "/api/groups", "")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestResourceListPassesQueryParameters(t *testing.T) {
	store := newFakeResources()
	store.groups = []*model.Group{
		{ID: 1, Name: "Depot", Attributes: model.Attributes{"k": "v"}},
		{ID: 2, Name: "North", Attributes: model.Attributes{"k": "v"}},
		{ID: 3, Name: "South", Attributes: model.Attributes{"k": "v"}},
	}
	handler := NewServer(nil, WithResources(store)).Handler()

	recorder := get(t, handler, "/api/groups?keyword=depot&limit=1&offset=1&excludeAttributes=true")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	// traccar-web reads a bare array, not a wrapper object.
	var raw []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode body %q: %v", recorder.Body, err)
	}
	if len(raw) != 1 {
		t.Fatalf("rows = %d, want 1", len(raw))
	}
	if _, ok := raw[0]["attributes"]; ok {
		t.Errorf("attributes = %v, want the field excluded", raw[0]["attributes"])
	}

	filter := store.lastFilter(t)
	if filter.Keyword != "depot" || filter.Limit != 1 || filter.Offset != 1 {
		t.Errorf("filter = %+v, want keyword/limit/offset forwarded", filter)
	}
	if filter.IDs != nil {
		t.Errorf("ids = %v, want no restriction without authentication", filter.IDs)
	}
}

func TestResourceListRejectsBadQuery(t *testing.T) {
	store := newFakeResources()
	handler := NewServer(nil, WithResources(store)).Handler()

	for _, target := range []string{
		"/api/groups?limit=abc",
		"/api/groups?userId=-1",
		"/api/groups?all=perhaps",
		"/api/groups?groupId=x",
	} {
		recorder := get(t, handler, target)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, recorder.Code)
		}
	}
}

func TestResourceListScopesToCaller(t *testing.T) {
	store := newFakeResources()
	store.parents = map[int64]int64{1: 0, 2: 1, 3: 2}
	store.groups = []*model.Group{{ID: 1, Name: "root"}, {ID: 2, Name: "child"}, {ID: 3, Name: "grandchild"}}
	store.add(t, permissions.ObjectUser, 2, permissions.ObjectGroup, 1)

	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadGroups}, nil)
	handler := NewServer(nil, WithResources(store), withTestToken(principal)).Handler()

	recorder := withToken(t, handler, http.MethodGet, "/api/groups", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if filter := store.lastFilter(t); len(filter.IDs) != 3 {
		t.Errorf("ids = %v, want the granted group and its descendants", filter.IDs)
	}

	// all=true is the same answer for a non-admin.
	recorder = withToken(t, handler, http.MethodGet, "/api/groups?all=true", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if filter := store.lastFilter(t); len(filter.IDs) != 3 {
		t.Errorf("ids = %v, want the granted group and its descendants", filter.IDs)
	}

	// Naming another account is admin only.
	recorder = withToken(t, handler, http.MethodGet, "/api/groups?userId=3", "")
	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}

	// all=true ignores userId, the same way the Java branch does, so this is
	// the caller's own list and not a 403.
	recorder = withToken(t, handler, http.MethodGet, "/api/groups?all=true&userId=3", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if filter := store.lastFilter(t); len(filter.IDs) != 3 {
		t.Errorf("ids = %v, want the caller's own grants", filter.IDs)
	}
}

func TestResourceListAllIsAdminOnly(t *testing.T) {
	store := newFakeResources()
	store.parents = map[int64]int64{1: 0}
	store.groups = []*model.Group{{ID: 1, Name: "root"}}
	store.add(t, permissions.ObjectUser, 1, permissions.ObjectGroup, 1)

	principal := auth.NewPrincipal(1, true, []auth.Scope{auth.ScopeReadGroups}, nil)
	handler := NewServer(nil, WithResources(store), withTestToken(principal)).Handler()

	// Without all an admin sees its own objects, exactly like the Java server.
	recorder := withToken(t, handler, http.MethodGet, "/api/groups", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if filter := store.lastFilter(t); filter.IDs == nil || len(filter.IDs) != 1 || filter.IDs[0] != 1 {
		t.Errorf("ids = %v, want the admin's own grant", filter.IDs)
	}

	recorder = withToken(t, handler, http.MethodGet, "/api/groups?all=true", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if filter := store.lastFilter(t); filter.IDs != nil {
		t.Errorf("ids = %v, want every group for all=true", filter.IDs)
	}
}

func TestResourceListGroupAndDeviceFilters(t *testing.T) {
	store := newFakeResources()
	store.geofences = []*geofence.Geofence{{ID: 5, Name: "depot"}, {ID: 6, Name: "yard"}}
	store.add(t, permissions.ObjectGroup, 4, permissions.ObjectGeofence, 5)
	store.add(t, permissions.ObjectGroup, 4, permissions.ObjectGeofence, 6)
	store.add(t, permissions.ObjectDevice, 7, permissions.ObjectGeofence, 6)

	principal := auth.NewPrincipal(2, true, []auth.Scope{auth.ScopeReadGeofences}, nil)
	handler := NewServer(nil, WithResources(store), withTestToken(principal)).Handler()

	recorder := withToken(t, handler, http.MethodGet, "/api/geofences?all=true&groupId=4", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if rows := decodeBodyList[map[string]any](t, recorder); len(rows) != 2 {
		t.Errorf("rows = %v, want the two geofences of group 4", rows)
	}

	recorder = withToken(t, handler, http.MethodGet, "/api/geofences?all=true&groupId=4&deviceId=7", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if rows := decodeBodyList[map[string]any](t, recorder); len(rows) != 1 || rows[0]["name"] != "yard" {
		t.Errorf("rows = %v, want only geofence 6", rows)
	}

	// deviceId is checked with the device rules, so a caller without the
	// device gets 403 even when the geofence itself is reachable.
	limited := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeReadGeofences}, nil)
	limitedHandler := NewServer(nil, WithResources(store), withTestToken(limited)).Handler()
	recorder = withToken(t, limitedHandler, http.MethodGet, "/api/geofences?deviceId=7", "")
	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestCreateGroupAddsOwnerGrant(t *testing.T) {
	store := newFakeResources()
	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeWriteGroups, auth.ScopeReadGroups}, nil)
	handler := NewServer(nil, WithResources(store), withTestToken(principal)).Handler()

	recorder := withToken(t, handler, http.MethodPost, "/api/groups", `{"name":"North"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body)
	}
	group := decodeBodyValue[groupValue](t, recorder)
	if group.ID != 1 || group.Name != "North" {
		t.Errorf("group = %+v, want the stored row", group)
	}
	if group.Attributes == nil || len(*group.Attributes) != 0 {
		t.Errorf("attributes = %v, want an empty object", group.Attributes)
	}

	pairs := store.snapshotPairs()
	want := permissions.Permission{Owner: permissions.ObjectUser, OwnerID: 2, Property: permissions.ObjectGroup, PropertyID: 1}
	if len(pairs) != 1 || pairs[0] != want {
		t.Errorf("pairs = %v, want %v", pairs, want)
	}

	recorder = withToken(t, handler, http.MethodPost, "/api/groups", `{"name":`)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("invalid body status = %d, want 400", recorder.Code)
	}
}

func TestCreateGroupRollsBackWhenGrantFails(t *testing.T) {
	store := newFakeResources()
	store.grantErr = errors.New("boom")
	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeWriteGroups}, nil)
	handler := NewServer(nil, WithResources(store), withTestToken(principal)).Handler()

	recorder := withToken(t, handler, http.MethodPost, "/api/groups", `{"name":"North"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", recorder.Code, recorder.Body)
	}
	if len(store.groups) != 0 {
		t.Errorf("groups = %v, want the insert rolled back", store.groups)
	}
}

func TestCreateGroupRejectsDeepParent(t *testing.T) {
	store := newFakeResources()
	store.parents = map[int64]int64{1: 0, 2: 1, 3: 2}
	handler := NewServer(nil, WithResources(store)).Handler()

	recorder := withToken(t, handler, http.MethodPost, "/api/groups", `{"name":"too deep","groupId":3}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "depth") {
		t.Errorf("body = %s, want the depth error", recorder.Body)
	}

	recorder = withToken(t, handler, http.MethodPost, "/api/groups", `{"name":"missing parent","groupId":99}`)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("unknown parent status = %d, want 400 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestCreateReportChecksCalendarAccess(t *testing.T) {
	store := newFakeResources()
	store.calendars = []*model.Calendar{{ID: 9, Name: "work"}}
	store.add(t, permissions.ObjectUser, 2, permissions.ObjectCalendar, 9)

	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeWriteReports}, nil)
	handler := NewServer(nil, WithResources(store), withTestToken(principal)).Handler()

	recorder := withToken(t, handler, http.MethodPost, "/api/reports",
		`{"calendarId":9,"type":"route","description":"daily"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body)
	}

	store.pairs = nil
	recorder = withToken(t, handler, http.MethodPost, "/api/reports",
		`{"calendarId":9,"type":"route","description":"daily"}`)
	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a calendar the caller cannot reach (body %s)", recorder.Code, recorder.Body)
	}
}

// TestGeofenceCRUD is the acceptance case of M2-3: a fence written through the
// API, floor and ceiling included, is read back and can be replaced without SQL.
func TestGeofenceCRUD(t *testing.T) {
	store := newFakeResources()
	handler := NewServer(nil, WithResources(store)).Handler()

	recorder := withToken(t, handler, http.MethodPost, "/api/geofences",
		`{"name":"depot","description":"main yard","area":"CIRCLE (51.5 0.0, 100)","attributes":{"floor":10,"ceiling":120}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (body %s)", recorder.Code, recorder.Body)
	}
	created := decodeBodyValue[map[string]any](t, recorder)
	if created["name"] != "depot" || created["calendarId"] != float64(0) {
		t.Errorf("created = %v", created)
	}
	if !strings.HasPrefix(created["area"].(string), "CIRCLE") {
		t.Errorf("area = %v, want the WKT the API stored", created["area"])
	}
	attributes, _ := created["attributes"].(map[string]any)
	if attributes["floor"] != float64(10) || attributes["ceiling"] != float64(120) {
		t.Errorf("attributes = %v, want floor and ceiling", attributes)
	}

	recorder = withToken(t, handler, http.MethodGet, "/api/geofences/1", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if fence := decodeBodyValue[map[string]any](t, recorder); fence["description"] != "main yard" {
		t.Errorf("fence = %v, want the stored description", fence)
	}

	recorder = withToken(t, handler, http.MethodPut, "/api/geofences/1",
		`{"name":"depot","area":"POLYGON ((0 0, 1 0, 1 1, 0 0))"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	if fence := decodeBodyValue[map[string]any](t, recorder); !strings.HasPrefix(fence["area"].(string), "POLYGON") {
		t.Errorf("area = %v, want the replaced polygon", fence["area"])
	}

	recorder = withToken(t, handler, http.MethodDelete, "/api/geofences/1", "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 (body %s)", recorder.Code, recorder.Body)
	}
	recorder = withToken(t, handler, http.MethodGet, "/api/geofences/1", "")
	if recorder.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", recorder.Code)
	}

	recorder = withToken(t, handler, http.MethodPost, "/api/geofences", `{"name":"bad","area":"TRIANGLE (1 2)"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("invalid area status = %d, want 400 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestResourceWriteNeedsAccess(t *testing.T) {
	store := newFakeResources()
	store.geofences = []*geofence.Geofence{{ID: 5, Name: "depot"}}
	store.add(t, permissions.ObjectUser, 2, permissions.ObjectGeofence, 5)

	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeWriteGeofences}, nil)
	handler := NewServer(nil, WithResources(store), withTestToken(principal)).Handler()

	recorder := withToken(t, handler, http.MethodPut, "/api/geofences/5", `{"name":"renamed","area":"CIRCLE (1 2, 3)"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	// An id the caller has no grant on is 403, not 404: existence is not leaked.
	recorder = withToken(t, handler, http.MethodDelete, "/api/geofences/6", "")
	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestResourceUpdateMissingIsNotFound(t *testing.T) {
	store := newFakeResources()
	handler := NewServer(nil, WithResources(store)).Handler()

	recorder := withToken(t, handler, http.MethodPut, "/api/drivers/9", `{"name":"nobody","uniqueId":"ABC"}`)
	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestCalendarDataRoundTripsAsBase64(t *testing.T) {
	store := newFakeResources()
	handler := NewServer(nil, WithResources(store)).Handler()

	recorder := withToken(t, handler, http.MethodPost, "/api/calendars",
		`{"name":"work","data":"QkVHSU46VkNBTEVOREFS"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body)
	}
	// Jackson encodes the byte[] column as base64, so the Go API does too.
	if body := strings.TrimSpace(recorder.Body.String()); body != `{"id":1,"name":"work","data":"QkVHSU46VkNBTEVOREFS","attributes":{}}` {
		t.Errorf("body = %s", body)
	}
}

func TestMaintenanceAliasReachesTheSameRows(t *testing.T) {
	store := newFakeResources()
	store.maintenances = []*model.Maintenance{{ID: 3, Name: "oil", Type: "totalDistance", Start: 1000, Period: 5000}}
	handler := NewServer(nil, WithResources(store)).Handler()

	for _, target := range []string{"/api/maintenance", "/api/maintenances"} {
		recorder := get(t, handler, target)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body %s)", target, recorder.Code, recorder.Body)
		}
		rows := decodeBodyList[maintenanceValue](t, recorder)
		if len(rows) != 1 || rows[0].Period != 5000 {
			t.Errorf("%s: rows = %+v, want the stored interval", target, rows)
		}
	}
}

func TestComputedAttributesAreAdminOnly(t *testing.T) {
	store := newFakeResources()
	principal := auth.NewPrincipal(2, false, []auth.Scope{auth.ScopeWriteAttributes, auth.ScopeReadAttributes}, nil)
	handler := NewServer(nil, WithResources(store), withTestToken(principal)).Handler()

	body := `{"description":"speed in knots","attribute":"speedKn","expression":"speed * 1.94384","type":"number","priority":1}`
	recorder := withToken(t, handler, http.MethodPost, "/api/attributes/computed", body)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a non-admin (body %s)", recorder.Code, recorder.Body)
	}

	admin := auth.NewPrincipal(1, true, []auth.Scope{auth.ScopeWriteAttributes}, nil)
	adminHandler := NewServer(nil, WithResources(store), withTestToken(admin)).Handler()
	recorder = withToken(t, adminHandler, http.MethodPost, "/api/attributes/computed", body)
	if recorder.Code != http.StatusCreated {
		t.Errorf("admin status = %d, want 201 (body %s)", recorder.Code, recorder.Body)
	}
}
