package model

// Order is one row of the orders table: a delivery a device is expected to
// make. The addresses are free text and never geocoded by the gateway.
type Order struct {
	ID          int64
	UniqueID    string
	Description string
	FromAddress string
	ToAddress   string
	Attributes  Attributes
}
