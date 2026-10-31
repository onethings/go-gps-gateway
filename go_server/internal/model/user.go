package model

import (
	"errors"
	"time"
)

// Storage level user errors, shared by the persistence layer and the REST
// handlers so neither has to import the other.
var (
	// ErrUserNotFound is returned when an id has no users row.
	ErrUserNotFound = errors.New("user not found")
	// ErrDuplicateUser is returned when the email already exists.
	ErrDuplicateUser = errors.New("duplicate user email")
	// ErrTokenNotFound is returned when a token id does not belong to the
	// user it was looked up for.
	ErrTokenNotFound = errors.New("token not found")
)

// User is one row of the users table: an account that owns devices and
// authenticates with API tokens.
type User struct {
	ID    int64
	Email string
	Name  string
	Admin bool
	// Disabled blocks every token of the user without deleting them.
	Disabled  bool
	CreatedAt time.Time
	// DeviceCount and TokenCount are computed by the list query so an admin
	// does not need one request per user.
	DeviceCount int
	TokenCount  int
}

// UserFilter selects users for a query. Limit and Offset are clamped by the
// storage layer.
type UserFilter struct {
	Limit  int
	Offset int
}

// UserCreate holds the fields accepted when creating a user. Email is the
// login identity and cannot be changed later.
type UserCreate struct {
	Email string
	Name  string
	Admin bool
}

// UserUpdate holds the mutable fields of a user. Nil fields are left as they
// are, which is what makes the PUT body a partial update.
type UserUpdate struct {
	Name     *string
	Admin    *bool
	Disabled *bool
}

// APIToken is one row of api_tokens without the hash: the plaintext token is
// only ever returned once, by the call that creates it.
type APIToken struct {
	ID        int64
	UserID    int64
	Scopes    []string
	ExpiresAt *time.Time
	CreatedAt time.Time
}

// TokenCreate holds the fields accepted when issuing a token. A nil ExpiresAt
// means the token never expires.
type TokenCreate struct {
	Scopes    []string
	ExpiresAt *time.Time
}
