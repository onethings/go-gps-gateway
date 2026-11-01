package model

// Group is one row of the groups table: a node of the tree device permissions
// are granted on. GroupID points at the parent group, zero means the group is
// a root. The Java server stores the same shape in tc_groups.
type Group struct {
	ID         int64
	Name       string
	GroupID    int64
	Attributes Attributes
}
