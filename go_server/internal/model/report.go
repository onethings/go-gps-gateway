package model

// Report is one row of the reports table: a scheduled report definition, the
// same row the Java server stores in tc_reports. CalendarID links the schedule
// that decides when it runs, Type names the report (route, trips, summary, ...)
// and the remaining settings live in Attributes.
type Report struct {
	ID          int64
	CalendarID  int64
	Type        string
	Description string
	Attributes  Attributes
}

// PositionReport is one position together with the events the rules raised for
// it. The two are stored in one transaction, which is what lets an event carry
// the id of the position that caused it. It is not a table: it is the unit the
// ingest chain hands to the batch writer.
type PositionReport struct {
	Position *Position
	Events   []*Event
}
