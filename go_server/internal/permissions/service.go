package permissions

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// ErrAccessDenied is returned by Check when the caller has no grant covering the
// object. The REST layer maps it to 403 for every object type, which is what
// "越權一律 403" asks for.
var ErrAccessDenied = errors.New("access denied")

// Store is the persistence the service needs. The postgres store implements it;
// tests use an in-memory map.
type Store interface {
	// UserGrants returns the direct device and group grants of a user. Group
	// grants are what device access is inherited through.
	UserGrants(ctx context.Context, userID int64) (devices []int64, groups []int64, err error)
	// GroupParents returns every group id and its parent, zero for a root.
	GroupParents(ctx context.Context) (map[int64]int64, error)
	// DevicesInGroups returns the ids of the devices whose group is in the set.
	DevicesInGroups(ctx context.Context, groupIDs []int64) ([]int64, error)
	// DeviceGroup returns the group of a device, zero when it has none.
	DeviceGroup(ctx context.Context, deviceID int64) (int64, error)

	// Permissions lists the pairs matching the arguments; a zero id means "no
	// restriction", which is how GET /api/permissions lists one user's grants.
	Permissions(ctx context.Context, owner Object, ownerID int64, property Object, propertyID int64) ([]Permission, error)
	// AddPermission stores a pair. Repeating the call is a no-op.
	AddPermission(ctx context.Context, permission Permission) error
	// RemovePermission drops a pair. Removing a pair that does not exist is
	// not an error.
	RemovePermission(ctx context.Context, permission Permission) error
}

// Service answers the permission questions the REST layer and the token lookup
// ask. It holds no state of its own; caching belongs to whoever calls it.
type Service struct {
	store Store
}

// NewService builds a service over the storage.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// DeviceIDs returns every device a user may access: the direct grants, plus the
// devices of every group the user was granted, following the group tree inside
// MaxGroupDepth. The result is sorted and free of duplicates.
//
// An admin has no device list at all; the caller decides the admin case before
// asking, which is why this method always narrows.
func (s *Service) DeviceIDs(ctx context.Context, userID int64) ([]int64, error) {
	devices, groups, err := s.store.UserGrants(ctx, userID)
	if err != nil {
		return nil, err
	}

	reached := make(map[int64]struct{}, len(devices))
	for _, deviceID := range devices {
		reached[deviceID] = struct{}{}
	}

	if len(groups) > 0 {
		parents, err := s.store.GroupParents(ctx)
		if err != nil {
			return nil, err
		}
		inherited, err := s.store.DevicesInGroups(ctx, Reach(groups, parents))
		if err != nil {
			return nil, err
		}
		for _, deviceID := range inherited {
			reached[deviceID] = struct{}{}
		}
	}

	ids := make([]int64, 0, len(reached))
	for deviceID := range reached {
		ids = append(ids, deviceID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// ObjectIDs returns the ids of the objects of one type a user may reach. It is
// what the REST list endpoints filter on when they are not asked for "all".
//
// Devices inherit from the groups the user was granted, and so do groups
// themselves, exactly like Condition.Permission in the Java server: a grant on
// a group reaches that group and its descendants inside MaxGroupDepth. Every
// other object type, geofences for instance, is reachable only through a direct
// user -> object grant, which is also what PermissionsService.checkPermission
// does in Java.
func (s *Service) ObjectIDs(ctx context.Context, userID int64, object Object) ([]int64, error) {
	if !object.Valid() {
		return nil, fmt.Errorf("%w: unknown object %q", ErrInvalidPermission, object)
	}
	if userID <= 0 {
		return []int64{}, nil
	}
	if object == ObjectDevice {
		return s.DeviceIDs(ctx, userID)
	}

	pairs, err := s.store.Permissions(ctx, ObjectUser, userID, object, 0)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(pairs))
	for _, pair := range pairs {
		ids = append(ids, pair.PropertyID)
	}
	if object != ObjectGroup || len(ids) == 0 {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		return ids, nil
	}

	parents, err := s.store.GroupParents(ctx)
	if err != nil {
		return nil, err
	}
	return Reach(ids, parents), nil
}

// CanAccess reports whether a user may read or change one object. An admin
// reaches everything, a user reaches itself, a device is reachable through a
// direct grant or through its group, and every other object needs a grant of its
// own.
func (s *Service) CanAccess(ctx context.Context, userID int64, admin bool, object Object, objectID int64) (bool, error) {
	if admin {
		return true, nil
	}
	if userID <= 0 || objectID <= 0 {
		return false, nil
	}
	if !object.Valid() {
		return false, fmt.Errorf("%w: unknown object %q", ErrInvalidPermission, object)
	}
	if object == ObjectUser {
		// A user always reaches itself. Managing other accounts is not modelled
		// as a permission yet, so no other user is reachable.
		return userID == objectID, nil
	}

	if err := s.Check(ctx, userID, object, objectID); err != nil {
		if errors.Is(err, ErrAccessDenied) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Check returns ErrAccessDenied unless the user has a grant covering the object.
// A device is covered either by a direct user -> device grant or by a
// user -> group grant on the group the device sits in, or on one of its
// ancestors inside MaxGroupDepth.
func (s *Service) Check(ctx context.Context, userID int64, object Object, objectID int64) error {
	if !object.Valid() {
		return fmt.Errorf("%w: unknown object %q", ErrInvalidPermission, object)
	}
	if userID <= 0 || objectID <= 0 {
		return fmt.Errorf("%w: %s %d", ErrAccessDenied, object, objectID)
	}

	direct, err := s.store.Permissions(ctx, ObjectUser, userID, object, objectID)
	if err != nil {
		return err
	}
	if len(direct) > 0 {
		return nil
	}

	if object == ObjectDevice {
		covered, err := s.checkDeviceGroup(ctx, userID, objectID)
		if err != nil {
			return err
		}
		if covered {
			return nil
		}
	}
	if object == ObjectGroup {
		covered, err := s.checkGroupGrant(ctx, userID, objectID)
		if err != nil {
			return err
		}
		if covered {
			return nil
		}
	}

	return fmt.Errorf("%w: %s %d", ErrAccessDenied, object, objectID)
}

// checkDeviceGroup follows the group of a device up to the groups the user was
// granted.
func (s *Service) checkDeviceGroup(ctx context.Context, userID, deviceID int64) (bool, error) {
	groupID, err := s.store.DeviceGroup(ctx, deviceID)
	if err != nil {
		return false, err
	}
	if groupID <= 0 {
		return false, nil
	}

	_, groups, err := s.store.UserGrants(ctx, userID)
	if err != nil {
		return false, err
	}
	if len(groups) == 0 {
		return false, nil
	}

	parents, err := s.store.GroupParents(ctx)
	if err != nil {
		return false, err
	}
	for _, reached := range Reach(groups, parents) {
		if reached == groupID {
			return true, nil
		}
	}
	return false, nil
}

// checkGroupGrant follows the granted groups down to groupID: a grant on an
// ancestor covers the group itself and everything below it.
func (s *Service) checkGroupGrant(ctx context.Context, userID, groupID int64) (bool, error) {
	_, groups, err := s.store.UserGrants(ctx, userID)
	if err != nil {
		return false, err
	}
	if len(groups) == 0 {
		return false, nil
	}

	parents, err := s.store.GroupParents(ctx)
	if err != nil {
		return false, err
	}
	for _, reached := range Reach(groups, parents) {
		if reached == groupID {
			return true, nil
		}
	}
	return false, nil
}

// Permissions lists the pairs that match the arguments.
func (s *Service) Permissions(ctx context.Context, owner Object, ownerID int64, property Object, propertyID int64) ([]Permission, error) {
	return s.store.Permissions(ctx, owner, ownerID, property, propertyID)
}

// Grant stores a pair after validating it.
func (s *Service) Grant(ctx context.Context, permission Permission) error {
	if err := permission.Validate(); err != nil {
		return err
	}
	return s.store.AddPermission(ctx, permission)
}

// Revoke drops a pair after validating it.
func (s *Service) Revoke(ctx context.Context, permission Permission) error {
	if err := permission.Validate(); err != nil {
		return err
	}
	return s.store.RemovePermission(ctx, permission)
}
