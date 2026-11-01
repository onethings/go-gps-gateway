package model

import "time"

// Action types, matching org.traccar.helper.LogAction. They are what
// /api/audit reports in the actionType column.
const (
	ActionCreate       = "create"
	ActionEdit         = "edit"
	ActionRemove       = "remove"
	ActionLink         = "link"
	ActionUnlink       = "unlink"
	ActionLogin        = "login"
	ActionLogout       = "logout"
	ActionDenied       = "denied"
	ActionToken        = "token"
	ActionCommand      = "command"
	ActionReport       = "report"
	ActionAccumulators = "accumulators"
)

// Action is one row of actions: who did what to which object, and when. The
// Java server keeps the same row in tc_actions; /api/audit reads it back with
// the account email resolved.
type Action struct {
	ID         int64
	ActionTime time.Time
	Address    string
	UserID     int64
	ActionType string
	ObjectType string
	ObjectID   int64
	// Attributes carries the metadata the Java server stores on an action,
	// such as the report type or the linked owner.
	Attributes Attributes
	// UserEmail is filled by the audit query, not stored on the row.
	UserEmail string
}
