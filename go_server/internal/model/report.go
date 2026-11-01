package model

// Report is one position together with the events the rules raised for it. The
// two are stored in one transaction, which is what lets an event carry the id of
// the position that caused it.
type Report struct {
	Position *Position
	Events   []*Event
}
