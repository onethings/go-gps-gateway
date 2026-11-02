// Package ldap implements the small part of LDAP an authentication backend
// needs: bind as a service account, search for the user, and bind again with
// the user's password to prove it. It speaks BER directly, so the gateway does
// not need an LDAP driver for three operations.
package ldap

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config describes one directory.
type Config struct {
	// URL is ldap:// or ldaps://host:port.
	URL string
	// BindDN and BindPassword are the service account used for the search.
	// Both empty means the search runs anonymously.
	BindDN       string
	BindPassword string
	// BaseDN is where the search starts.
	BaseDN string
	// UserFilter is the search filter. {0} is replaced by the escaped login.
	// Empty means (|(uid={0})(mail={0})).
	UserFilter string
	// NameAttribute and MailAttribute map the entry onto the account. Empty
	// uses cn and mail.
	NameAttribute string
	MailAttribute string
	// Timeout bounds one authentication. Zero uses 10 seconds.
	Timeout time.Duration
	// Dial defaults to net.Dialer.DialContext; tests pass net.Pipe.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// Identity is the account the directory confirmed.
type Identity struct {
	DN    string
	Email string
	Name  string
}

// ErrInvalidCredentials reports a wrong password or an unknown user. The API
// turns it into the same 401 a local failure gets.
var ErrInvalidCredentials = errors.New("ldap: invalid credentials")

// Authenticate resolves username, then proves the password by binding as the
// entry. It returns ErrInvalidCredentials for both an unknown user and a wrong
// password, so a caller cannot enumerate the directory.
func Authenticate(ctx context.Context, config Config, username, password string) (*Identity, error) {
	if config.URL == "" {
		return nil, errors.New("ldap: no URL configured")
	}
	if username == "" || password == "" {
		return nil, ErrInvalidCredentials
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	identity, err := search(ctx, config, username)
	if err != nil {
		return nil, err
	}
	if !bind(ctx, config, identity.DN, password) {
		return nil, ErrInvalidCredentials
	}
	return identity, nil
}

// search reads the first matching entry.
func search(ctx context.Context, config Config, username string) (*Identity, error) {
	conn, err := dial(ctx, config)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if config.BindDN != "" {
		if !bindOn(ctx, conn, config.BindDN, config.BindPassword) {
			return nil, errors.New("ldap: service account bind failed")
		}
	}

	filter := userFilter(config.UserFilter, username)
	request := message(1, applicationTag(3,
		octetString(config.BaseDN),
		enumerated(2), // whole subtree
		enumerated(0), // never dereference aliases
		integer(0),
		integer(0),
		boolean(false),
		filter,
		sequence(), // no attribute restriction: read all user attributes
	))
	conn.SetDeadline(deadline(ctx))
	if _, err := conn.Write(request); err != nil {
		return nil, fmt.Errorf("ldap: search: %w", err)
	}
	reader := bufio.NewReader(conn)

	for {
		response, err := readMessage(reader)
		if err != nil {
			return nil, fmt.Errorf("ldap: search response: %w", err)
		}
		switch response.tag {
		case applicationTagValue(4): // searchResEntry
			return parseEntry(config, response.content)
		case applicationTagValue(5): // searchResDone
			return nil, ErrInvalidCredentials
		}
	}
}

// bind proves one DN and password on a fresh connection.
func bind(ctx context.Context, config Config, dn, password string) bool {
	conn, err := dial(ctx, config)
	if err != nil {
		return false
	}
	defer conn.Close()
	return bindOn(ctx, conn, dn, password)
}

func bindOn(ctx context.Context, conn net.Conn, dn, password string) bool {
	request := message(1, applicationTag(0,
		integer(3),
		octetString(dn),
		contextPrimitive(0, []byte(password)),
	))
	conn.SetDeadline(deadline(ctx))
	if _, err := conn.Write(request); err != nil {
		return false
	}
	response, err := readMessage(bufio.NewReader(conn))
	if err != nil || response.tag != applicationTagValue(1) {
		return false
	}
	code, err := firstEnumerated(response.content)
	return err == nil && code == 0
}

func dial(ctx context.Context, config Config) (net.Conn, error) {
	parsed, err := url.Parse(config.URL)
	if err != nil {
		return nil, fmt.Errorf("ldap: parse URL: %w", err)
	}
	address := parsed.Host
	if !strings.Contains(address, ":") {
		if parsed.Scheme == "ldaps" {
			address += ":636"
		} else {
			address += ":389"
		}
	}
	dial := config.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		dial = dialer.DialContext
	}
	conn, err := dial(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("ldap: dial %s: %w", address, err)
	}
	if parsed.Scheme == "ldaps" {
		host, _, _ := net.SplitHostPort(address)
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("ldap: tls: %w", err)
		}
		return tlsConn, nil
	}
	return conn, nil
}

func deadline(ctx context.Context) time.Time {
	if value, ok := ctx.Deadline(); ok {
		return value
	}
	return time.Now().Add(10 * time.Second)
}

// userFilter builds the search filter. An escaped login keeps a caller from
// injecting a filter through the username field.
func userFilter(template, username string) []byte {
	escaped := escape(username)
	if template == "" {
		template = "(|(uid={0})(mail={0}))"
	}
	expression := strings.ReplaceAll(template, "{0}", escaped)
	return parseFilter(expression)
}

// escape applies RFC 4515 escaping to a filter value.
func escape(value string) string {
	replacer := strings.NewReplacer(
		"\\", `\5c`,
		"*", `\2a`,
		"(", `\28`,
		")", `\29`,
		"\x00", `\00`,
	)
	return replacer.Replace(value)
}

// parseFilter handles the subset of RFC 4515 the configuration uses: and, or,
// not and a single equality. An unparsable template degrades to a filter that
// matches nothing, so a typo fails closed.
func parseFilter(text string) []byte {
	parser := &filterParser{input: strings.TrimSpace(text)}
	filter, ok := parser.parse()
	if !ok {
		return contextConstructed(3, octetString("1.1"), octetString("\x00"))
	}
	return filter
}

type filterParser struct {
	input string
	index int
}

func (p *filterParser) parse() ([]byte, bool) {
	if !p.consume('(') {
		return nil, false
	}
	var result []byte
	switch {
	case p.consume('&'), p.consume('|'), p.consume('!'):
		operator := p.input[p.index-1]
		var children []byte
		for p.peek() == '(' {
			child, ok := p.parse()
			if !ok {
				return nil, false
			}
			children = append(children, child...)
		}
		tag := 0
		switch operator {
		case '&':
			tag = 0
		case '|':
			tag = 1
		case '!':
			tag = 2
		}
		result = contextConstructed(tag, children)
	default:
		start := p.index
		for p.index < len(p.input) && p.input[p.index] != '=' && p.input[p.index] != ')' {
			p.index++
		}
		if p.index >= len(p.input) || p.input[p.index] != '=' {
			return nil, false
		}
		attribute := p.input[start:p.index]
		p.index++
		start = p.index
		for p.index < len(p.input) && p.input[p.index] != ')' {
			p.index++
		}
		value := unescape(p.input[start:p.index])
		result = contextConstructed(3, octetString(attribute), octetString(value))
	}
	if !p.consume(')') {
		return nil, false
	}
	return result, true
}

func (p *filterParser) peek() byte {
	if p.index >= len(p.input) {
		return 0
	}
	return p.input[p.index]
}

func (p *filterParser) consume(character byte) bool {
	if p.peek() == character {
		p.index++
		return true
	}
	return false
}

func unescape(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] == '\\' && index+2 < len(value) {
			if parsed, err := strconv.ParseUint(value[index+1:index+3], 16, 8); err == nil {
				builder.WriteByte(byte(parsed))
				index += 2
				continue
			}
		}
		builder.WriteByte(value[index])
	}
	return builder.String()
}

// Authenticator binds one directory configuration to the api.LoginAuthenticator
// shape.
type Authenticator struct {
	Config Config
}

// Authenticate implements the authenticator the login handler calls.
func (a Authenticator) Authenticate(ctx context.Context, username, password string) (*Identity, error) {
	return Authenticate(ctx, a.Config, username, password)
}
