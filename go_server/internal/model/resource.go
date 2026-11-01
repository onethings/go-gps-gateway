package model

import "errors"

// ErrResourceNotFound is returned by the storage layer when an id has no row
// in one of the resource tables (groups, geofences, calendars, drivers,
// maintenances, orders, reports, statistics, attributes). The REST layer maps
// it to 404 for every one of them.
var ErrResourceNotFound = errors.New("resource not found")

// ResourceFilter selects rows of a resource table. Zero values mean "no
// restriction"; Limit and Offset are clamped by the storage layer the same way
// the device query clamps them. Keyword is matched case insensitively against
// the table's text columns.
type ResourceFilter struct {
	// IDs restricts the result to a set of rows. Nil means "no restriction";
	// a non-nil empty slice matches nothing.
	IDs     []int64
	Keyword string
	Limit   int
	Offset  int
}
