// Package permissions implements the object permission model the Java server
// keeps in its tc_<owner>_<property> tables: a permission links two objects,
// one owner and one property, such as user 3 -> device 7 or group 2 ->
// geofence 5. Unlike the Java server the pairs live in one table (see the
// storage layer); this package only knows the names, the depth rule and the
// checks built on them.
package permissions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrInvalidPermission is returned for a pair that cannot exist: an unknown
// object name, a non-positive id, the same object twice, or a JSON body that is
// not exactly two key/value pairs.
var ErrInvalidPermission = errors.New("invalid permission")

// Object names the model type a permission refers to. The strings are the REST
// keys minus the "Id" suffix, so `device` is written `deviceId` in JSON and in
// the query string.
type Object string

const (
	ObjectUser         Object = "user"
	ObjectDevice       Object = "device"
	ObjectGroup        Object = "group"
	ObjectGeofence     Object = "geofence"
	ObjectDriver       Object = "driver"
	ObjectCalendar     Object = "calendar"
	ObjectMaintenance  Object = "maintenance"
	ObjectOrder        Object = "order"
	ObjectReport       Object = "report"
	ObjectNotification Object = "notification"
	ObjectCommand      Object = "command"
	ObjectAttribute    Object = "attribute"
)

// Objects lists every known object type in documentation order.
var Objects = []Object{
	ObjectUser, ObjectDevice, ObjectGroup, ObjectGeofence, ObjectDriver,
	ObjectCalendar, ObjectMaintenance, ObjectOrder, ObjectReport,
	ObjectNotification, ObjectCommand, ObjectAttribute,
}

// owners are the types that may appear on the left of a permission. A device
// inherits from its group and a group from its parent, so only these three can
// own a grant; "geofence 5 owns device 7" is not something either server
// stores.
var owners = map[Object]struct{}{
	ObjectUser:   {},
	ObjectGroup:  {},
	ObjectDevice: {},
}

const keySuffix = "Id"

// Valid reports whether the object name is known.
func (o Object) Valid() bool {
	for _, known := range Objects {
		if o == known {
			return true
		}
	}
	return false
}

// Key is the JSON and query string spelling, e.g. "deviceId".
func (o Object) Key() string { return string(o) + keySuffix }

func (o Object) String() string { return string(o) }

// ParseObject reads a key such as "deviceId" or "groupId" and returns the
// object. An unknown name or a missing suffix is an ErrInvalidPermission.
func ParseObject(key string) (Object, error) {
	if len(key) <= len(keySuffix) || key[len(key)-len(keySuffix):] != keySuffix {
		return "", fmt.Errorf("%w: %q is not an object key", ErrInvalidPermission, key)
	}
	object := Object(key[:len(key)-len(keySuffix)])
	if !object.Valid() {
		return "", fmt.Errorf("%w: unknown object %q", ErrInvalidPermission, object)
	}
	return object, nil
}

// Permission is one row: Owner owns Property.
type Permission struct {
	Owner      Object
	OwnerID    int64
	Property   Object
	PropertyID int64
}

// New validates a pair and returns it.
func New(owner Object, ownerID int64, property Object, propertyID int64) (Permission, error) {
	permission := Permission{Owner: owner, OwnerID: ownerID, Property: property, PropertyID: propertyID}
	if err := permission.Validate(); err != nil {
		return Permission{}, err
	}
	return permission, nil
}

// Validate reports whether the pair can be stored.
func (p Permission) Validate() error {
	if !p.Owner.Valid() {
		return fmt.Errorf("%w: unknown owner object %q", ErrInvalidPermission, p.Owner)
	}
	if !p.Property.Valid() {
		return fmt.Errorf("%w: unknown property object %q", ErrInvalidPermission, p.Property)
	}
	if _, ok := owners[p.Owner]; !ok {
		return fmt.Errorf("%w: %s cannot own a permission", ErrInvalidPermission, p.Owner)
	}
	if p.OwnerID <= 0 || p.PropertyID <= 0 {
		return fmt.Errorf("%w: ids must be positive, got %d and %d", ErrInvalidPermission, p.OwnerID, p.PropertyID)
	}
	if p.Owner == p.Property {
		return fmt.Errorf("%w: %s cannot own itself", ErrInvalidPermission, p.Owner)
	}
	return nil
}

// IsZero reports whether the permission was never filled in.
func (p Permission) IsZero() bool {
	return p.Owner == "" && p.OwnerID == 0 && p.Property == "" && p.PropertyID == 0
}

// Matches reports whether a query with the given arguments selects this
// permission. A zero id means "any", which is how GET /api/permissions lists
// every device of one user.
func (p Permission) Matches(owner Object, ownerID int64, property Object, propertyID int64) bool {
	if owner != "" && p.Owner != owner {
		return false
	}
	if ownerID != 0 && p.OwnerID != ownerID {
		return false
	}
	if property != "" && p.Property != property {
		return false
	}
	if propertyID != 0 && p.PropertyID != propertyID {
		return false
	}
	return true
}

func (p Permission) String() string {
	return fmt.Sprintf("%s %d -> %s %d", p.Owner, p.OwnerID, p.Property, p.PropertyID)
}

// MarshalJSON writes {"userId":3,"deviceId":7} with the owner first.
func (p Permission) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	writeJSONPair(&buffer, p.Owner.Key(), p.OwnerID)
	buffer.WriteByte(',')
	writeJSONPair(&buffer, p.Property.Key(), p.PropertyID)
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

func writeJSONPair(buffer *bytes.Buffer, key string, value int64) {
	encodedKey, _ := json.Marshal(key)
	buffer.Write(encodedKey)
	buffer.WriteByte(':')
	encodedValue, _ := json.Marshal(value)
	buffer.Write(encodedValue)
}

// UnmarshalJSON reads a permission object such as {"userId":3,"deviceId":7}.
// The order of the two keys decides which is the owner, exactly like the Java
// implementation, so the body is read as a token stream: a map would lose the
// order and make {"deviceId":7,"userId":3} indistinguishable from the reverse.
func (p *Permission) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPermission, err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return fmt.Errorf("%w: expected a JSON object", ErrInvalidPermission)
	}

	type entry struct {
		key   string
		value int64
	}
	entries := make([]entry, 0, 2)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPermission, err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("%w: expected a string key", ErrInvalidPermission)
		}
		var value int64
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("%w: %s must be a number: %v", ErrInvalidPermission, key, err)
		}
		entries = append(entries, entry{key: key, value: value})
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPermission, err)
	}
	if decoder.More() {
		return fmt.Errorf("%w: trailing data after the permission object", ErrInvalidPermission)
	}
	if len(entries) != 2 {
		return fmt.Errorf("%w: expected exactly 2 keys, got %d", ErrInvalidPermission, len(entries))
	}

	owner, err := ParseObject(entries[0].key)
	if err != nil {
		return err
	}
	property, err := ParseObject(entries[1].key)
	if err != nil {
		return err
	}
	parsed, err := New(owner, entries[0].value, property, entries[1].value)
	if err != nil {
		return err
	}
	*p = parsed
	return nil
}

// ParseKeyValue builds a permission from an ordered pair of key and id, which is
// how GET /api/permissions receives its two query parameters.
func ParseKeyValue(ownerKey string, ownerID int64, propertyKey string, propertyID int64) (Permission, error) {
	owner, err := ParseObject(ownerKey)
	if err != nil {
		return Permission{}, err
	}
	property, err := ParseObject(propertyKey)
	if err != nil {
		return Permission{}, err
	}
	return New(owner, ownerID, property, propertyID)
}
