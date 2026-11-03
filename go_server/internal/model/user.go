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
	// ErrResetTokenInvalid is returned when a password reset code is unknown,
	// expired or already used.
	ErrResetTokenInvalid = errors.New("password reset token is invalid or expired")
	// ErrResetTooSoon is returned when the account already has a fresh reset
	// code, so the resend throttle suppresses a second email.
	ErrResetTooSoon = errors.New("password reset was requested moments ago")
)

// User is one row of the users table: an account that owns devices and
// authenticates with API tokens.
type User struct {
	ID    int64
	Email string
	Name  string
	Admin bool
	// Disabled blocks every token of the user without deleting them.
	Disabled bool
	// Temporary marks an account created by a share link. It has no password
	// and cannot be used to share anything itself.
	Temporary bool
	CreatedAt time.Time
	// Attributes holds the free form account settings the web interface
	// stores, matching users.attributes in the Java server.
	Attributes Attributes
	// DeviceCount and TokenCount are computed by the list query so an admin
	// does not need one request per user.
	DeviceCount int
	TokenCount  int
}

// UserFilter selects users for a query. Limit and Offset are clamped by the
// storage layer.
type UserFilter struct {
	// Email restricts the result to one exact address, which is how a share
	// link finds the account it created earlier.
	Email  string
	Limit  int
	Offset int
}

// UserCreate holds the fields accepted when creating a user. Email is the
// login identity and cannot be changed later.
type UserCreate struct {
	Email string
	Name  string
	Admin bool
	// Temporary creates a share account: no password, and refused as the owner
	// of a further share.
	Temporary bool
	// Attributes seeds users.attributes. A share account records its
	// expirationTime here, which is what the scheduled cleanup reads.
	Attributes Attributes
}

// UserUpdate holds the mutable fields of a user. Nil fields are left as they
// are, which is what makes the PUT body a partial update.
type UserUpdate struct {
	Name     *string
	Admin    *bool
	Disabled *bool
	// Attributes replaces the whole map when it is non-nil. The web interface
	// stores per-user preferences here (terms acceptance, notification
	// overrides).
	Attributes *Attributes
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
