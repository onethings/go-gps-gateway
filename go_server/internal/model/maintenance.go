package model

// Maintenance is one row of the maintenances table: a service interval such as
// "oil change every 10000 km". Type is the position attribute the interval is
// measured against (totalDistance, hours, ...), Start the value it begins at
// and Period the length of the interval.
type Maintenance struct {
	ID         int64
	Name       string
	Type       string
	Start      float64
	Period     float64
	Attributes Attributes
}
