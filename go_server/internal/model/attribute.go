package model

// Attribute is one row of the attributes table: a computed attribute, an
// expression evaluated for every position before it is stored. Attribute is
// the key the result lands on, Type the value type it claims, and Priority the
// evaluation order among competing rows.
type Attribute struct {
	ID          int64
	Description string
	Attribute   string
	Expression  string
	Type        string
	Priority    int
}
