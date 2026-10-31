package model

import "time"

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
