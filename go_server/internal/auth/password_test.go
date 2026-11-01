package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestHashPasswordVerifies(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$2") {
		t.Fatalf("hash %q is not bcrypt", hash)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Error("VerifyPassword rejected the password it hashed")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Error("VerifyPassword accepted a wrong password")
	}
	if VerifyPassword(hash, "") {
		t.Error("VerifyPassword accepted an empty password")
	}

	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("bcrypt.Cost: %v", err)
	}
	if cost != PasswordCost {
		t.Errorf("cost = %d, want %d", cost, PasswordCost)
	}
}

func TestHashPasswordIsSalted(t *testing.T) {
	first, err := HashPassword("same password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword("same password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if first == second {
		t.Error("two hashes of one password are identical, so the salt is not random")
	}
}

func TestHashPasswordRejectsEmpty(t *testing.T) {
	if _, err := HashPassword(""); !errors.Is(err, ErrEmptyPassword) {
		t.Fatalf("err = %v, want %v", err, ErrEmptyPassword)
	}
}

// TestVerifyPasswordEmptyHashStillCosts guards the login timing property: an
// account with no password must not answer faster than a wrong password, or the
// response time lists every account that has none. The threshold is far below
// one bcrypt at cost 12 and far above a bare return.
func TestVerifyPasswordEmptyHashStillCosts(t *testing.T) {
	start := time.Now()
	if VerifyPassword("", "anything") {
		t.Fatal("VerifyPassword accepted an empty hash")
	}
	if elapsed := time.Since(start); elapsed < 5*time.Millisecond {
		t.Errorf("empty hash answered in %s, so the bcrypt was skipped", elapsed)
	}
}

func TestHashPasswordRejectsTooLong(t *testing.T) {
	// bcrypt only reads the first 72 bytes, so a longer password has to be
	// refused rather than silently cut down to a shorter one.
	if _, err := HashPassword(strings.Repeat("a", 72)); err != nil {
		t.Fatalf("72 bytes should be accepted: %v", err)
	}
	if _, err := HashPassword(strings.Repeat("a", 73)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("73 bytes: err = %v, want %v", err, ErrPasswordTooLong)
	}
}

func TestVerifyPasswordRejectsBrokenHash(t *testing.T) {
	cases := map[string]string{
		"no hash":   "",
		"garbage":   "not-a-bcrypt-hash",
		"truncated": "$2a$12$abcdefghijklmnopqrstuv",
	}
	for name, hash := range cases {
		t.Run(name, func(t *testing.T) {
			if VerifyPassword(hash, "password") {
				t.Errorf("VerifyPassword accepted hash %q", hash)
			}
		})
	}
}
