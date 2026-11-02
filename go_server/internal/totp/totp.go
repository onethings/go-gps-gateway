// Package totp implements the time based one time password the two factor
// routes use. It follows RFC 6238 with the RFC 4226 defaults the authenticator
// apps expect: HMAC-SHA1, six digits, a thirty second step.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// Digits is the code length the authenticator apps default to.
	Digits = 6
	// Period is the length of one step.
	Period = 30 * time.Second
	// secretBytes is the size of the generated shared secret: 160 bits, the
	// size RFC 4226 recommends for HMAC-SHA1.
	secretBytes = 20
	// skew accepts one step before and after the current one, so a phone whose
	// clock drifted a little still logs in.
	skew = 1
)

// GenerateSecret creates a new base32 secret without padding, which is the
// form an authenticator app and the otpauth URL expect.
func GenerateSecret() (string, error) {
	random := make([]byte, secretBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("totp: read random secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random), nil
}

// Code returns the six digit code of a secret at one moment.
func Code(secret string, at time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return hotp(key, uint64(at.Unix()/int64(Period/time.Second))), nil
}

// Validate reports whether code is the current code of secret, or the one of
// the step before or after it.
func Validate(secret, code string, at time.Time) bool {
	if secret == "" {
		return false
	}
	normalized := strings.TrimSpace(code)
	if len(normalized) != Digits {
		return false
	}
	for _, digit := range normalized {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return false
	}
	counter := uint64(at.Unix() / int64(Period/time.Second))
	for offset := -skew; offset <= skew; offset++ {
		candidate := hotp(key, uint64(int64(counter)+int64(offset)))
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(normalized)) == 1 {
			return true
		}
	}
	return false
}

// ProvisioningURL builds the otpauth:// URI an authenticator app scans.
func ProvisioningURL(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	query := url.Values{}
	query.Set("secret", secret)
	query.Set("issuer", issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", fmt.Sprint(Digits))
	query.Set("period", fmt.Sprint(int(Period/time.Second)))
	return "otpauth://totp/" + label + "?" + query.Encode()
}

func decodeSecret(secret string) ([]byte, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	// Base32 without padding is what GenerateSecret returns; re-adding the
	// padding lets the decoder accept both spellings.
	if remainder := len(normalized) % 8; remainder != 0 {
		normalized += strings.Repeat("=", 8-remainder)
	}
	key, err := base32.StdEncoding.DecodeString(normalized)
	if err != nil {
		return nil, fmt.Errorf("totp: invalid secret: %w", err)
	}
	return key, nil
}

// hotp is the value of one counter, truncated as RFC 4226 specifies.
func hotp(key []byte, counter uint64) string {
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(message[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}
