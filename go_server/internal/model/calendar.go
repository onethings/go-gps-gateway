package model

// Calendar is one row of the calendars table: an iCalendar document the event
// handlers consult before they notify. The data is kept verbatim; evaluating
// the recurrence rules (VEVENT, RRULE, EXDATE) is not ported yet, so nothing in
// the gateway reads it back apart from the CRUD endpoints.
type Calendar struct {
	ID         int64
	Name       string
	Data       []byte
	Attributes Attributes
}
