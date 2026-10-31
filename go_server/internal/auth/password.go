package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// PasswordCost is the bcrypt work factor every stored hash uses. Traccar
// writes bcrypt hashes too, so the column can hold either server's output.
const PasswordCost = 12

// ErrEmptyPassword is returned instead of hashing a blank password, which
// would otherwise produce a hash that any blank login could satisfy.
var ErrEmptyPassword = errors.New("auth: password is empty")

// HashPassword returns a bcrypt hash of password. The hash carries its own
// salt and cost, so users.password_hash stores nothing else.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", ErrEmptyPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), PasswordCost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword reports whether password matches hash. An empty hash, an
// empty password and a malformed hash all return false, so a caller cannot
// tell "no password set" from "wrong password".
func VerifyPassword(hash, password string) bool {
	if hash == "" || password == "" {
		return false
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
