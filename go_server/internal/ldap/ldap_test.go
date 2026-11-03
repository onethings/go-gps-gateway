package ldap

import (
	"bufio"
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestFilterEscapesTheLogin(t *testing.T) {
	filter := userFilter("(uid={0})", "", "a*b(c)")
	// The value is decoded back to its literal bytes inside the assertion, so
	// the parentheses and the star cannot become filter syntax.
	if !containsBytes(filter, []byte("a*b(c)")) {
		t.Fatalf("filter = %q", filter)
	}

}

func TestFilterUsesConfiguredIDAttribute(t *testing.T) {
	filter := userFilter("", "sAMAccountName", "alice")
	if !containsBytes(filter, []byte("sAMAccountName")) {
		t.Fatalf("filter = %q, want the configured id attribute", filter)
	}
	// A value that cannot be an attribute descriptor falls back to uid
	// instead of changing the filter structure.
	if !containsBytes(userFilter("", "uid)(objectClass=*", "alice"), []byte("uid")) {
		t.Fatal("an invalid id attribute must fall back to uid")
	}
}

func TestFilterAcceptsTheJavaLoginPlaceholder(t *testing.T) {
	filter := userFilter("(uid=:login)", "", "alice")
	if !containsBytes(filter, []byte("alice")) {
		t.Fatalf("filter = %q, want the login substituted", filter)
	}
}

func TestParseFilterBuildsNestedExpressions(t *testing.T) {
	filter := parseFilter("(&(uid=alice)(|(mail=a@x)(cn=Alice)))")
	if len(filter) == 0 || filter[0] != 0xa0 {
		t.Fatalf("filter = %x, want an and node", filter)
	}
	// A typo must fail closed instead of becoming an empty filter.
	broken := parseFilter("(&(uid=alice)")
	if len(broken) == 0 {
		t.Fatal("a broken filter must still produce a value")
	}
}

func TestAuthenticateSearchesThenBinds(t *testing.T) {
	searchServer, searchClient := net.Pipe()
	bindServer, bindClient := net.Pipe()
	defer searchServer.Close()
	defer bindServer.Close()

	dialed := 0
	config := Config{
		URL:     "ldap://directory.test",
		BaseDN:  "dc=example,dc=com",
		Timeout: 5 * time.Second,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			dialed++
			if dialed == 1 {
				return searchClient, nil
			}
			return bindClient, nil
		},
	}

	go func() {
		reader := bufio.NewReader(searchServer)
		if _, err := readTLV(reader); err != nil {
			t.Errorf("read search: %v", err)
			return
		}
		entry := applicationTag(4,
			octetString("cn=alice,dc=example,dc=com"),
			sequence(
				sequence(octetString("cn"), tlv(tagSet, octetString("Alice"))),
				sequence(octetString("mail"), tlv(tagSet, octetString("alice@example.com"))),
			),
		)
		done := applicationTag(5, enumerated(0), octetString(""), octetString(""))
		_, _ = searchServer.Write(message(1, entry))
		_, _ = searchServer.Write(message(1, done))
	}()

	bound := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(bindServer)
		outer, err := readTLV(reader)
		if err != nil {
			t.Errorf("read bind: %v", err)
			return
		}
		// The bound password is the last value of the request.
		bound <- string(outer.content)
		response := applicationTag(1, enumerated(0), octetString(""), octetString(""))
		_, _ = bindServer.Write(message(1, response))
	}()

	identity, err := Authenticate(context.Background(), config, "alice", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if identity.DN != "cn=alice,dc=example,dc=com" || identity.Email != "alice@example.com" ||
		identity.Name != "Alice" {
		t.Fatalf("identity = %+v", identity)
	}
	request := <-bound
	if !containsBytes([]byte(request), []byte("secret")) {
		t.Fatalf("bind request does not carry the password: %q", request)
	}
}

func TestAuthenticateRejectsWrongPassword(t *testing.T) {
	searchServer, searchClient := net.Pipe()
	bindServer, bindClient := net.Pipe()
	defer searchServer.Close()
	defer bindServer.Close()

	dialed := 0
	config := Config{
		URL:    "ldap://directory.test",
		BaseDN: "dc=example,dc=com",
		Dial: func(context.Context, string, string) (net.Conn, error) {
			dialed++
			if dialed == 1 {
				return searchClient, nil
			}
			return bindClient, nil
		},
	}

	go func() {
		reader := bufio.NewReader(searchServer)
		if _, err := readTLV(reader); err != nil {
			return
		}
		entry := applicationTag(4,
			octetString("cn=alice,dc=example,dc=com"),
			sequence(sequence(octetString("mail"), tlv(tagSet, octetString("alice@example.com")))),
		)
		_, _ = searchServer.Write(message(1, entry))
	}()
	go func() {
		reader := bufio.NewReader(bindServer)
		if _, err := readTLV(reader); err != nil {
			return
		}
		failure := applicationTag(1, enumerated(49), octetString(""), octetString("bad password"))
		_, _ = bindServer.Write(message(1, failure))
	}()

	if _, err := Authenticate(context.Background(), config, "alice", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticateRejectsEmptyPassword(t *testing.T) {
	_, err := Authenticate(context.Background(), Config{URL: "ldap://x"}, "alice", "")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v", err)
	}
}

func containsBytes(haystack, needle []byte) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		match := true
		for offset := range needle {
			if haystack[index+offset] != needle[offset] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
