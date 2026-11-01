package model

import "strings"

// Driver is one row of the drivers table: the card a device reports in its
// positions. UniqueID is the value that appears in the driverUniqueId position
// attribute, which is how the driver handler joins the two.
type Driver struct {
	ID         int64
	Name       string
	UniqueID   string
	Attributes Attributes
}

// Normalize trims the unique id, which is what the Java setter does. The
// storage layer calls it before every write so an API caller cannot store
// " ABC " and then fail to match the device's "ABC".
func (d *Driver) Normalize() {
	d.UniqueID = strings.TrimSpace(d.UniqueID)
}
