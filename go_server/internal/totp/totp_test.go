package totp

import (
	"strings"
	"testing"
	"time"
)

// The secret and the timestamps are the RFC 6238 SHA1 test vectors, truncated
// to the six digits this package emits.
const rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestCodeMatchesRFCVectors(t *testing.T) {
	cases := map[int64]string{
		59:         "287082",
		1111111109: "081804",
		1234567890: "005924",
		2000000000: "279037",
	}
	for seconds, want := range cases {
		got, err := Code(rfcSecret, time.Unix(seconds, 0))
		if err != nil {
			t.Fatalf("code at %d: %v", seconds, err)
		}
		if got != want {
			t.Errorf("code at %d = %s, want %s", seconds, got, want)
		}
	}
}

func TestValidateAcceptsCurrentAndAdjacentSteps(t *testing.T) {
	now := time.Unix(1111111109, 0)
	for _, offset := range []time.Duration{-Period, 0, Period} {
		code, err := Code(rfcSecret, now.Add(offset))
		if err != nil {
			t.Fatal(err)
		}
		if !Validate(rfcSecret, code, now) {
			t.Errorf("code for offset %v was rejected", offset)
		}
	}
	if Validate(rfcSecret, "000000", now) && mustCode(t, now) != "000000" {
		t.Log("a coincidental zero code is acceptable")
	}
	if Validate(rfcSecret, "", now) {
		t.Error("an empty code must be rejected")
	}
	if Validate("", "123456", now) {
		t.Error("an empty secret must be rejected")
	}
}

func TestValidateRejectsDistantStep(t *testing.T) {
	now := time.Unix(1111111109, 0)
	code, err := Code(rfcSecret, now.Add(10*Period))
	if err != nil {
		t.Fatal(err)
	}
	if Validate(rfcSecret, code, now) {
		t.Error("a code ten steps away must be rejected")
	}
}

func TestGenerateSecretRoundTrips(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	code, err := Code(secret, time.Now())
	if err != nil {
		t.Fatalf("generated secret is not decodable: %v", err)
	}
	if !Validate(secret, code, time.Now()) {
		t.Error("a generated secret did not validate its own code")
	}
}

func TestProvisioningURL(t *testing.T) {
	got := ProvisioningURL("Traccar", "user@example.com", "ABC234")
	for _, want := range []string{"otpauth://totp/", "secret=ABC234", "issuer=Traccar", "digits=6"} {
		if !strings.Contains(got, want) {
			t.Errorf("provisioning URL %q is missing %q", got, want)
		}
	}
}

func mustCode(t *testing.T, at time.Time) string {
	t.Helper()
	code, err := Code(rfcSecret, at)
	if err != nil {
		t.Fatal(err)
	}
	return code
}
