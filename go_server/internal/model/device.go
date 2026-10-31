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
	ID         int64
	UniqueID   string
	Name       string
	Model      string
	Protocol   string
	Attributes map[string]any
	// LastUpdate is the newest device time stored for the device, zero when it
	// has never reported a position.
	LastUpdate time.Time
	CreatedAt  time.Time
}

// DeviceFilter selects devices for a query. Zero values mean "no restriction";
// Limit and Offset are clamped by the storage layer.
type DeviceFilter struct {
	Protocol string
	Limit    int
	Offset   int
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

// DeviceUpdate holds the mutable fields of a device. Both are replaced
// wholesale, an empty string clears the column.
type DeviceUpdate struct {
	Name  string
	Model string
}
