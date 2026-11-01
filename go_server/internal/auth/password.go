package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// PasswordCost is the bcrypt work factor every stored hash uses. Traccar
// writes bcrypt hashes too, so the column can hold either server's output.
const PasswordCost = 12

// maxPasswordLength is bcrypt's input limit. A longer password is rejected
// here instead of being silently truncated or surfacing as a server error.
const maxPasswordLength = 72

var (
	// ErrEmptyPassword is returned instead of hashing a blank password, which
	// would otherwise produce a hash that any blank login could satisfy.
	ErrEmptyPassword = errors.New("auth: password is empty")
	// ErrPasswordTooLong rejects a password bcrypt cannot take whole.
	ErrPasswordTooLong = fmt.Errorf("auth: password is longer than %d bytes", maxPasswordLength)
)

// HashPassword returns a bcrypt hash of password. The hash carries its own
// salt and cost, so users.password_hash stores nothing else.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", ErrEmptyPassword
	}
	if len(password) > maxPasswordLength {
		return "", ErrPasswordTooLong
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), PasswordCost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(hash), nil
}

// emptyHashStandIn is a bcrypt hash of a throwaway string. Nothing a caller
// types matches it, and comparing against it costs one bcrypt like any other
// check.
const emptyHashStandIn = "$2a$12$wJXE5CCFMZPhdi3vmddmQu8CYgiF8I7MYaMTkxcBVji9wXYhPNBf."

// VerifyPassword reports whether password matches hash. An empty password and
// a malformed hash return false.
//
// An empty hash, which is how an account without a password reaches here, also
// returns false but still spends a bcrypt. Returning early instead would make
// "this address has an account but no password" and "this address has no
// account" differ by three orders of magnitude in response time, which is the
// whole list of accounts.
func VerifyPassword(hash, password string) bool {
	if password == "" {
		return false
	}
	if hash == "" {
		hash = emptyHashStandIn
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// Credential is the password login view of one account: the stored hash plus
// the flag that decides whether a login may turn into a token. It is
// deliberately not part of model.User, which the REST layer renders as JSON.
type Credential struct {
	UserID       int64
	PasswordHash string
	Disabled     bool
}
