package permissions

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// memoryStore is an in-memory Store so the service can be tested without a
// database. Groups 1 -> 2 -> 3 form a three level tree; device 10 sits in group
// 2, device 11 in group 3 and device 12 has no group at all.
type memoryStore struct {
	pairs   []Permission
	parents map[int64]int64
	devices map[int64]int64 // device id -> group id
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		parents: map[int64]int64{1: 0, 2: 1, 3: 2, 4: 0},
		devices: map[int64]int64{10: 2, 11: 3, 12: 0},
	}
}

func (m *memoryStore) UserGrants(_ context.Context, userID int64) ([]int64, []int64, error) {
	var devices, groups []int64
	for _, pair := range m.pairs {
		if pair.Owner != ObjectUser || pair.OwnerID != userID {
			continue
		}
		switch pair.Property {
		case ObjectDevice:
			devices = append(devices, pair.PropertyID)
		case ObjectGroup:
			groups = append(groups, pair.PropertyID)
		}
	}
	return devices, groups, nil
}

func (m *memoryStore) GroupParents(context.Context) (map[int64]int64, error) {
	copied := make(map[int64]int64, len(m.parents))
	for id, parent := range m.parents {
		copied[id] = parent
	}
	return copied, nil
}

func (m *memoryStore) DevicesInGroups(_ context.Context, groupIDs []int64) ([]int64, error) {
	wanted := make(map[int64]struct{}, len(groupIDs))
	for _, id := range groupIDs {
		wanted[id] = struct{}{}
	}
	var devices []int64
	for deviceID, groupID := range m.devices {
		if _, ok := wanted[groupID]; ok {
			devices = append(devices, deviceID)
		}
	}
	return devices, nil
}

func (m *memoryStore) DeviceGroup(_ context.Context, deviceID int64) (int64, error) {
	return m.devices[deviceID], nil
}

func (m *memoryStore) Permissions(_ context.Context, owner Object, ownerID int64, property Object, propertyID int64) ([]Permission, error) {
	var matched []Permission
	for _, pair := range m.pairs {
		if pair.Matches(owner, ownerID, property, propertyID) {
			matched = append(matched, pair)
		}
	}
	return matched, nil
}

func (m *memoryStore) AddPermission(_ context.Context, permission Permission) error {
	for _, pair := range m.pairs {
		if pair == permission {
			return nil
		}
	}
	m.pairs = append(m.pairs, permission)
	return nil
}

func (m *memoryStore) RemovePermission(_ context.Context, permission Permission) error {
	kept := m.pairs[:0]
	for _, pair := range m.pairs {
		if pair != permission {
			kept = append(kept, pair)
		}
	}
	m.pairs = kept
	return nil
}

func mustPair(t *testing.T, owner Object, ownerID int64, property Object, propertyID int64) Permission {
	t.Helper()
	pair, err := New(owner, ownerID, property, propertyID)
	if err != nil {
		t.Fatalf("new pair: %v", err)
	}
	return pair
}

func TestServiceDeviceIDsIncludeGroupDevices(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()

	// A grant on the root covers device 10 (group 2) and device 11 (group 3).
	if err := service.Grant(ctx, mustPair(t, ObjectUser, 5, ObjectGroup, 1)); err != nil {
		t.Fatalf("grant: %v", err)
	}
	got, err := service.DeviceIDs(ctx, 5)
	if err != nil {
		t.Fatalf("device ids: %v", err)
	}
	if !reflect.DeepEqual(got, []int64{10, 11}) {
		t.Fatalf("device ids = %v, want [10 11]", got)
	}

	// A grant on the middle group reaches only its own subtree.
	if err := service.Grant(ctx, mustPair(t, ObjectUser, 6, ObjectGroup, 2)); err != nil {
		t.Fatalf("grant: %v", err)
	}
	got, err = service.DeviceIDs(ctx, 6)
	if err != nil {
		t.Fatalf("device ids: %v", err)
	}
	if !reflect.DeepEqual(got, []int64{10, 11}) {
		t.Fatalf("device ids = %v, want [10 11]", got)
	}

	// The leaf reaches only itself, so device 10 in the parent is not included.
	if err := service.Grant(ctx, mustPair(t, ObjectUser, 7, ObjectGroup, 3)); err != nil {
		t.Fatalf("grant: %v", err)
	}
	got, err = service.DeviceIDs(ctx, 7)
	if err != nil {
		t.Fatalf("device ids: %v", err)
	}
	if !reflect.DeepEqual(got, []int64{11}) {
		t.Fatalf("device ids = %v, want [11]", got)
	}
}

func TestServiceDeviceIDsMergeDirectAndGroupGrants(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()

	if err := service.Grant(ctx, mustPair(t, ObjectUser, 5, ObjectGroup, 1)); err != nil {
		t.Fatalf("grant group: %v", err)
	}
	if err := service.Grant(ctx, mustPair(t, ObjectUser, 5, ObjectDevice, 12)); err != nil {
		t.Fatalf("grant device: %v", err)
	}
	// Two grants that resolve to the same device must not duplicate it.
	if err := service.Grant(ctx, mustPair(t, ObjectUser, 5, ObjectDevice, 10)); err != nil {
		t.Fatalf("grant device: %v", err)
	}

	got, err := service.DeviceIDs(ctx, 5)
	if err != nil {
		t.Fatalf("device ids: %v", err)
	}
	if !reflect.DeepEqual(got, []int64{10, 11, 12}) {
		t.Fatalf("device ids = %v, want [10 11 12]", got)
	}
}

func TestServiceDeviceIDsWithoutGrants(t *testing.T) {
	service := NewService(newMemoryStore())
	got, err := service.DeviceIDs(context.Background(), 9)
	if err != nil {
		t.Fatalf("device ids: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("device ids = %v, want none", got)
	}
}

func TestServiceCheckDeviceThroughGroup(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()

	if err := service.Grant(ctx, mustPair(t, ObjectUser, 5, ObjectGroup, 1)); err != nil {
		t.Fatalf("grant: %v", err)
	}

	for _, deviceID := range []int64{10, 11} {
		if err := service.Check(ctx, 5, ObjectDevice, deviceID); err != nil {
			t.Errorf("device %d through the group: %v", deviceID, err)
		}
	}
	// Device 12 has no group and no grant.
	if err := service.Check(ctx, 5, ObjectDevice, 12); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("device 12: err = %v, want ErrAccessDenied", err)
	}
	// A different user has no grant at all.
	if err := service.Check(ctx, 6, ObjectDevice, 10); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("other user: err = %v, want ErrAccessDenied", err)
	}
}

func TestServiceCheckOtherObjectsNeedTheirOwnGrant(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()

	if err := service.Grant(ctx, mustPair(t, ObjectUser, 5, ObjectGeofence, 8)); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := service.Check(ctx, 5, ObjectGeofence, 8); err != nil {
		t.Errorf("geofence 8: %v", err)
	}
	if err := service.Check(ctx, 5, ObjectGeofence, 9); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("geofence 9: err = %v, want ErrAccessDenied", err)
	}
	// A group grant does not widen any other object type.
	if err := service.Grant(ctx, mustPair(t, ObjectUser, 6, ObjectGroup, 1)); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := service.Check(ctx, 6, ObjectGeofence, 8); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("geofence through a group grant: err = %v, want ErrAccessDenied", err)
	}
}

func TestServiceCanAccess(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()
	if err := service.Grant(ctx, mustPair(t, ObjectUser, 5, ObjectGroup, 1)); err != nil {
		t.Fatalf("grant: %v", err)
	}

	cases := []struct {
		name   string
		userID int64
		admin  bool
		object Object
		id     int64
		want   bool
	}{
		{"admin reaches everything", 5, true, ObjectGeofence, 99, true},
		{"own account", 5, false, ObjectUser, 5, true},
		{"another account", 5, false, ObjectUser, 6, false},
		{"device through group", 5, false, ObjectDevice, 11, true},
		{"device without grant", 5, false, ObjectDevice, 12, false},
		{"zero id", 5, false, ObjectDevice, 0, false},
		{"unknown user", 0, false, ObjectDevice, 10, false},
	}
	for _, testCase := range cases {
		got, err := service.CanAccess(ctx, testCase.userID, testCase.admin, testCase.object, testCase.id)
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		if got != testCase.want {
			t.Errorf("%s: CanAccess = %v, want %v", testCase.name, got, testCase.want)
		}
	}

	if _, err := service.CanAccess(ctx, 5, false, "planet", 1); !errors.Is(err, ErrInvalidPermission) {
		t.Errorf("unknown object: err = %v, want ErrInvalidPermission", err)
	}
}

func TestServiceGrantAndRevokeValidate(t *testing.T) {
	service := NewService(newMemoryStore())
	ctx := context.Background()

	if err := service.Grant(ctx, Permission{Owner: ObjectUser, OwnerID: 1}); !errors.Is(err, ErrInvalidPermission) {
		t.Errorf("grant: err = %v, want ErrInvalidPermission", err)
	}
	if err := service.Revoke(ctx, Permission{Owner: ObjectGeofence, OwnerID: 1, Property: ObjectDevice, PropertyID: 2}); !errors.Is(err, ErrInvalidPermission) {
		t.Errorf("revoke: err = %v, want ErrInvalidPermission", err)
	}
}

func TestServicePermissionsFilters(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()
	for _, pair := range []Permission{
		mustPair(t, ObjectUser, 5, ObjectDevice, 10),
		mustPair(t, ObjectUser, 5, ObjectDevice, 11),
		mustPair(t, ObjectUser, 6, ObjectGeofence, 8),
	} {
		if err := service.Grant(ctx, pair); err != nil {
			t.Fatalf("grant: %v", err)
		}
	}

	all, err := service.Permissions(ctx, ObjectUser, 5, "", 0)
	if err != nil {
		t.Fatalf("permissions: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("permissions = %v, want the two device grants", all)
	}

	one, err := service.Permissions(ctx, ObjectUser, 5, ObjectDevice, 11)
	if err != nil || len(one) != 1 || one[0].PropertyID != 11 {
		t.Fatalf("filtered = %v, err %v", one, err)
	}
}

func TestServiceObjectIDs(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()

	// Group 1 is the root of 1 -> 2 -> 3; group 4 is a separate root.
	for _, pair := range []Permission{
		mustPair(t, ObjectUser, 5, ObjectGroup, 1),
		mustPair(t, ObjectUser, 5, ObjectGeofence, 20),
		mustPair(t, ObjectGroup, 1, ObjectGeofence, 21),
		mustPair(t, ObjectUser, 5, ObjectDriver, 30),
	} {
		if err := service.Grant(ctx, pair); err != nil {
			t.Fatalf("grant: %v", err)
		}
	}

	groups, err := service.ObjectIDs(ctx, 5, ObjectGroup)
	if err != nil {
		t.Fatalf("group ids: %v", err)
	}
	if want := []int64{1, 2, 3}; !reflect.DeepEqual(groups, want) {
		t.Errorf("group ids = %v, want %v (the grant covers descendants, not group 4)", groups, want)
	}

	devices, err := service.ObjectIDs(ctx, 5, ObjectDevice)
	if err != nil {
		t.Fatalf("device ids: %v", err)
	}
	if want := []int64{10, 11}; !reflect.DeepEqual(devices, want) {
		t.Errorf("device ids = %v, want %v", devices, want)
	}

	// Every other type is reachable through a direct user grant only, which
	// is what PermissionsService.checkPermission does in the Java server.
	geofences, err := service.ObjectIDs(ctx, 5, ObjectGeofence)
	if err != nil {
		t.Fatalf("geofence ids: %v", err)
	}
	if want := []int64{20}; !reflect.DeepEqual(geofences, want) {
		t.Errorf("geofence ids = %v, want %v", geofences, want)
	}

	drivers, err := service.ObjectIDs(ctx, 5, ObjectDriver)
	if err != nil {
		t.Fatalf("driver ids: %v", err)
	}
	if want := []int64{30}; !reflect.DeepEqual(drivers, want) {
		t.Errorf("driver ids = %v, want %v", drivers, want)
	}

	empty, err := service.ObjectIDs(ctx, 5, ObjectCalendar)
	if err != nil || len(empty) != 0 || empty == nil {
		t.Errorf("calendar ids = %v, err %v, want an empty non-nil slice", empty, err)
	}

	if _, err := service.ObjectIDs(ctx, 5, "planet"); !errors.Is(err, ErrInvalidPermission) {
		t.Errorf("unknown object: err = %v, want ErrInvalidPermission", err)
	}
}

func TestServiceCheckGroupInheritsFromGrantedParent(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()

	if err := service.Grant(ctx, mustPair(t, ObjectUser, 5, ObjectGroup, 1)); err != nil {
		t.Fatalf("grant: %v", err)
	}

	if err := service.Check(ctx, 5, ObjectGroup, 3); err != nil {
		t.Errorf("group 3 is below the granted root: %v", err)
	}
	if err := service.Check(ctx, 5, ObjectGroup, 4); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("group 4 is a sibling root: err = %v, want ErrAccessDenied", err)
	}
}

func TestServiceDirectIDsSkipsGroupExpansion(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()

	// A direct grant on device 10 and a grant on the root group, which covers
	// devices 10 and 11 through the tree.
	for _, pair := range []Permission{
		mustPair(t, ObjectUser, 5, ObjectDevice, 10),
		mustPair(t, ObjectUser, 5, ObjectGroup, 1),
	} {
		if err := service.Grant(ctx, pair); err != nil {
			t.Fatalf("grant: %v", err)
		}
	}

	direct, err := service.DirectIDs(ctx, 5, ObjectDevice)
	if err != nil {
		t.Fatalf("direct ids: %v", err)
	}
	if want := []int64{10}; !reflect.DeepEqual(direct, want) {
		t.Errorf("direct ids = %v, want %v", direct, want)
	}

	reached, err := service.ObjectIDs(ctx, 5, ObjectDevice)
	if err != nil {
		t.Fatalf("object ids: %v", err)
	}
	if want := []int64{10, 11}; !reflect.DeepEqual(reached, want) {
		t.Errorf("object ids = %v, want %v", reached, want)
	}
}

func TestServiceGroupDeviceIDs(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()

	cases := []struct {
		name    string
		groupID int64
		want    []int64
	}{
		{"root covers the tree", 1, []int64{10, 11}},
		{"child covers its subtree", 2, []int64{10, 11}},
		{"leaf covers its own device", 3, []int64{11}},
		{"empty root", 4, []int64{}},
		{"zero id", 0, []int64{}},
	}
	for _, testCase := range cases {
		got, err := service.GroupDeviceIDs(ctx, testCase.groupID)
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		if !reflect.DeepEqual(got, testCase.want) {
			t.Errorf("%s: ids = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}
