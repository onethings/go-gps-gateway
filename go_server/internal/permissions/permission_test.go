package permissions

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseObject(t *testing.T) {
	cases := map[string]Object{
		"userId":     ObjectUser,
		"deviceId":   ObjectDevice,
		"groupId":    ObjectGroup,
		"geofenceId": ObjectGeofence,
	}
	for key, want := range cases {
		got, err := ParseObject(key)
		if err != nil {
			t.Fatalf("ParseObject(%q): %v", key, err)
		}
		if got != want {
			t.Errorf("ParseObject(%q) = %q, want %q", key, got, want)
		}
		if key != got.Key() {
			t.Errorf("%q round trip gave %q", key, got.Key())
		}
	}

	for _, bad := range []string{"", "id", "devices", "Id", "deviceid", "devId", "unknownId"} {
		if _, err := ParseObject(bad); !errors.Is(err, ErrInvalidPermission) {
			t.Errorf("ParseObject(%q) = %v, want ErrInvalidPermission", bad, err)
		}
	}
}

func TestNewValidatesPairs(t *testing.T) {
	if _, err := New(ObjectUser, 1, ObjectDevice, 2); err != nil {
		t.Fatalf("user -> device: %v", err)
	}
	if _, err := New(ObjectGroup, 1, ObjectGeofence, 2); err != nil {
		t.Fatalf("group -> geofence: %v", err)
	}
	if _, err := New(ObjectDevice, 1, ObjectNotification, 2); err != nil {
		t.Fatalf("device -> notification: %v", err)
	}

	rejected := []struct {
		name       string
		owner      Object
		ownerID    int64
		property   Object
		propertyID int64
	}{
		{"unknown owner", "planet", 1, ObjectDevice, 2},
		{"unknown property", ObjectUser, 1, "planet", 2},
		{"owner cannot own", ObjectGeofence, 1, ObjectDevice, 2},
		{"zero owner id", ObjectUser, 0, ObjectDevice, 2},
		{"negative property id", ObjectUser, 1, ObjectDevice, -2},
		{"self reference", ObjectGroup, 1, ObjectGroup, 1},
	}
	for _, testCase := range rejected {
		if _, err := New(testCase.owner, testCase.ownerID, testCase.property, testCase.propertyID); !errors.Is(err, ErrInvalidPermission) {
			t.Errorf("%s: err = %v, want ErrInvalidPermission", testCase.name, err)
		}
	}
}

func TestPermissionJSONRoundTrip(t *testing.T) {
	permission, err := New(ObjectUser, 3, ObjectDevice, 7)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	encoded, err := json.Marshal(permission)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != `{"userId":3,"deviceId":7}` {
		t.Errorf("encoded = %s", encoded)
	}

	var decoded Permission
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded != permission {
		t.Errorf("decoded = %#v, want %#v", decoded, permission)
	}
}

// The Java server takes the first key as the owner, so the body order is part
// of the contract: a map would silently swap the two.
func TestPermissionJSONKeepsKeyOrder(t *testing.T) {
	var permission Permission
	if err := json.Unmarshal([]byte(`{"deviceId":2,"groupId":9}`), &permission); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if permission.Owner != ObjectDevice || permission.OwnerID != 2 {
		t.Fatalf("owner = %s %d, want device 2", permission.Owner, permission.OwnerID)
	}
	if permission.Property != ObjectGroup || permission.PropertyID != 9 {
		t.Fatalf("property = %s %d, want group 9", permission.Property, permission.PropertyID)
	}
}

func TestPermissionJSONRejectsBadBodies(t *testing.T) {
	bodies := []string{
		`[]`,
		`{}`,
		`{"userId":1}`,
		`{"userId":1,"deviceId":2,"groupId":3}`,
		`{"userId":"one","deviceId":2}`,
		`{"geofenceId":1,"deviceId":2}`,
		`{"userId":0,"deviceId":2}`,
	}
	for _, body := range bodies {
		var permission Permission
		if err := json.Unmarshal([]byte(body), &permission); !errors.Is(err, ErrInvalidPermission) {
			t.Errorf("%s: err = %v, want ErrInvalidPermission", body, err)
		}
	}
}

// Trailing data after the object is rejected by encoding/json itself, not by
// UnmarshalJSON, so it is asserted separately.
func TestPermissionJSONRejectsTrailingData(t *testing.T) {
	var permission Permission
	if err := json.Unmarshal([]byte(`{"userId":1,"deviceId":2} {}`), &permission); err == nil {
		t.Fatal("trailing data was accepted")
	}
}

func TestPermissionMatches(t *testing.T) {
	permission, _ := New(ObjectUser, 3, ObjectDevice, 7)

	if !permission.Matches(ObjectUser, 3, ObjectDevice, 7) {
		t.Error("exact match failed")
	}
	if !permission.Matches("", 3, ObjectDevice, 0) {
		t.Error("wildcard ids should match")
	}
	if permission.Matches(ObjectUser, 4, ObjectDevice, 7) {
		t.Error("a different owner id matched")
	}
	if permission.Matches(ObjectUser, 3, ObjectDevice, 8) {
		t.Error("a different property id matched")
	}
	if permission.Matches(ObjectGroup, 3, ObjectDevice, 7) {
		t.Error("a different owner type matched")
	}
}

func TestParseKeyValue(t *testing.T) {
	permission, err := ParseKeyValue("userId", 4, "geofenceId", 11)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if permission.Owner != ObjectUser || permission.OwnerID != 4 ||
		permission.Property != ObjectGeofence || permission.PropertyID != 11 {
		t.Fatalf("permission = %#v", permission)
	}

	if _, err := ParseKeyValue("userId", 4, "planetsId", 11); !errors.Is(err, ErrInvalidPermission) {
		t.Errorf("err = %v, want ErrInvalidPermission", err)
	}
}

func TestMarshalRejectsInvalidPermission(t *testing.T) {
	if _, err := json.Marshal(Permission{Owner: ObjectUser, OwnerID: 1}); !errors.Is(err, ErrInvalidPermission) {
		t.Errorf("err = %v, want ErrInvalidPermission", err)
	}
}

func TestObjectsAreUniqueAndParse(t *testing.T) {
	seen := map[string]struct{}{}
	for _, object := range Objects {
		key := object.Key()
		if _, duplicate := seen[key]; duplicate {
			t.Errorf("duplicate key %q", key)
		}
		seen[key] = struct{}{}
		if parsed, err := ParseObject(key); err != nil || parsed != object {
			t.Errorf("ParseObject(%q) = %q, %v", key, parsed, err)
		}
		if !strings.HasSuffix(key, keySuffix) {
			t.Errorf("%q does not end with %q", key, keySuffix)
		}
	}
}
