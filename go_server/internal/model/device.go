package model

import (
	"errors"
	"time"
)

// Storage level device errors, shared by the persistence layer and the REST
// handlers so neither has to import the other.
var (
	// ErrDeviceNotFound is returned when an id has no device record.
	ErrDeviceNotFound = errors.New("device not found")
	// ErrDuplicateDevice is returned when the unique id already exists.
	ErrDuplicateDevice = errors.New("duplicate device unique id")
)

// Device is one row of the devices table: the registration record the gateway
// creates on the first handshake of a device.
type Device struct {
	ID       int64
	UniqueID string
	Name     string
	Model    string
	Protocol string
	// GroupID is the groups row the device belongs to, zero for a device
	// outside the tree. Device permissions are granted on groups.
	GroupID    int64
	Attributes Attributes
	// Status is the connection state the session manager last recorded:
	// online while the device holds a connection, offline after it went away.
	Status string
	// LastUpdate is the newest device time stored for the device, zero when it
	// has never reported a position.
	LastUpdate time.Time
	CreatedAt  time.Time
}

// DeviceFilter selects devices for a query. Zero values mean "no restriction";
// Limit and Offset are clamped by the storage layer.
type DeviceFilter struct {
	Protocol string
	// UniqueIDs and IDs are the uniqueId and id query parameters. Both are
	// exact matches and narrow the result when present; the two lists are
	// combined with OR, the same way the Java resource unions them.
	UniqueIDs []string
	IDs       []int64
	// Keyword is matched case insensitively against name, uniqueId and model.
	Keyword string
	// DeviceIDs restricts the result to a set of devices. Nil means "no
	// restriction"; a non-nil empty slice matches nothing.
	DeviceIDs []int64
	Limit     int
	Offset    int
}

// DeviceCreate holds the fields accepted when creating a device. The gateway
// fills Protocol on the first handshake, so provisioning it up front is
// optional but recommended for paired devices.
type DeviceCreate struct {
	UniqueID string
	Name     string
	Model    string
	Protocol string
}

// DeviceUpdate holds the mutable fields of a device. Name and model are
// replaced wholesale, an empty string clears the column. Attributes replaces
// the whole map when it is non-nil; a nil pointer leaves the stored attributes
// untouched, so a client that only renames a device does not wipe them.
type DeviceUpdate struct {
	Name       string
	Model      string
	Attributes *Attributes
}
