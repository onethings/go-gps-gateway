// Package redisbus connects the gateway nodes with a Redis pub/sub channel.
// It exists so a browser connected to one node sees the positions and events
// that another node ingested; the API itself is stateless because its tokens
// live in the database.
package redisbus

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config configures a Bus.
type Config struct {
	// URL is redis://[:password@]host:port[/db].
	URL string
	// Channel is the pub/sub channel. Empty means "traccar".
	Channel string
	// Dial defaults to net.Dialer.DialContext; tests pass net.Pipe.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// Logger reports a reconnect or a dropped message.
	Logger func(format string, args ...any)
}

// Bus is a minimal RESP client: it only needs PUBLISH and SUBSCRIBE, so it
// implements those instead of pulling in a Redis driver.
type Bus struct {
	config Config
	dial   func(ctx context.Context, network, address string) (net.Conn, error)

	mu     sync.Mutex
	conn   net.Conn
	reader *bufio.Reader
}

// New creates the bus. It does not connect until the first publish or
// subscribe, so an unreachable Redis delays nothing at startup.
func New(config Config) (*Bus, error) {
	if config.URL == "" {
		return nil, errors.New("redisbus: empty URL")
	}
	parsed, err := url.Parse(config.URL)
	if err != nil {
		return nil, fmt.Errorf("redisbus: parse URL: %w", err)
	}
	if parsed.Scheme != "redis" && parsed.Scheme != "rediss" {
		return nil, fmt.Errorf("redisbus: unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, errors.New("redisbus: URL has no host")
	}
	if config.Channel == "" {
		config.Channel = "traccar"
	}
	dial := config.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		dial = dialer.DialContext
	}
	if config.Logger == nil {
		config.Logger = func(string, ...any) {}
	}
	return &Bus{config: config, dial: dial}, nil
}

// Publish sends one payload to the channel. A failure drops the message and
// reconnects on the next call; a live view is not worth blocking the ingest
// path for.
func (b *Bus) Publish(ctx context.Context, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.connect(ctx); err != nil {
		return err
	}
	command := buildRESP("PUBLISH", b.config.Channel, string(payload))
	if deadline, ok := ctx.Deadline(); ok {
		_ = b.conn.SetWriteDeadline(deadline)
	}
	if _, err := b.conn.Write(command); err != nil {
		b.reset()
		return fmt.Errorf("redisbus: publish: %w", err)
	}
	if _, err := readReply(b.reader); err != nil {
		b.reset()
		return fmt.Errorf("redisbus: publish reply: %w", err)
	}
	return nil
}

// Subscribe reads messages until ctx is cancelled, reconnecting with a backoff
// when Redis goes away. The handler runs on the subscribing goroutine.
func (b *Bus) Subscribe(ctx context.Context, handler func(payload []byte)) error {
	if handler == nil {
		return errors.New("redisbus: nil handler")
	}
	backoff := time.Second
	for ctx.Err() == nil {
		conn, reader, err := b.dialAndLogin(ctx)
		if err != nil {
			b.config.Logger("redisbus: subscribe: %v", err)
		} else {
			err = b.subscribeOnce(ctx, conn, reader, handler)
			_ = conn.Close()
			if err != nil && ctx.Err() == nil {
				b.config.Logger("redisbus: subscribe: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return nil
}

func (b *Bus) subscribeOnce(ctx context.Context, conn net.Conn, reader *bufio.Reader,
	handler func([]byte)) error {
	if _, err := conn.Write(buildRESP("SUBSCRIBE", b.config.Channel)); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		reply, err := readReply(reader)
		if err != nil {
			return err
		}
		values, ok := reply.([]any)
		if !ok || len(values) != 3 {
			continue
		}
		kind, _ := values[0].(string)
		if kind != "message" {
			continue
		}
		switch payload := values[2].(type) {
		case string:
			handler([]byte(payload))
		case []byte:
			handler(payload)
		}
	}
}

func (b *Bus) connect(ctx context.Context) error {
	if b.conn != nil {
		return nil
	}
	conn, reader, err := b.dialAndLogin(ctx)
	if err != nil {
		return err
	}
	b.conn = conn
	b.reader = reader
	return nil
}

func (b *Bus) dialAndLogin(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	parsed, err := url.Parse(b.config.URL)
	if err != nil {
		return nil, nil, err
	}
	address := parsed.Host
	if !strings.Contains(address, ":") {
		address += ":6379"
	}
	conn, err := b.dial(ctx, "tcp", address)
	if err != nil {
		return nil, nil, fmt.Errorf("redisbus: dial %s: %w", address, err)
	}
	reader := bufio.NewReader(conn)
	if parsed.User != nil {
		password, _ := parsed.User.Password()
		if password == "" {
			password = parsed.User.Username()
		}
		if _, err := conn.Write(buildRESP("AUTH", password)); err != nil {
			_ = conn.Close()
			return nil, nil, err
		}
		if _, err := readReply(reader); err != nil {
			_ = conn.Close()
			return nil, nil, err
		}
	}
	if database := strings.TrimPrefix(parsed.Path, "/"); database != "" {
		if _, err := conn.Write(buildRESP("SELECT", database)); err != nil {
			_ = conn.Close()
			return nil, nil, err
		}
		if _, err := readReply(reader); err != nil {
			_ = conn.Close()
			return nil, nil, err
		}
	}
	return conn, reader, nil
}

func (b *Bus) reset() {
	if b.conn != nil {
		_ = b.conn.Close()
		b.conn = nil
		b.reader = nil
	}
}

// buildRESP encodes one command as a RESP array of bulk strings.
func buildRESP(values ...string) []byte {
	var builder strings.Builder
	builder.WriteString("*" + strconv.Itoa(len(values)) + "\r\n")
	for _, value := range values {
		builder.WriteString("$" + strconv.Itoa(len(value)) + "\r\n")
		builder.WriteString(value)
		builder.WriteString("\r\n")
	}
	return []byte(builder.String())
}

// readReply decodes one RESP value. Only the shapes PUBLISH, SUBSCRIBE and
// AUTH answer with are needed: simple strings, errors, integers, bulk strings
// and arrays.
func readReply(reader *bufio.Reader) (any, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	line := func() (string, error) {
		text, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r"), nil
	}
	switch prefix {
	case '+', '-':
		text, err := line()
		if err != nil {
			return nil, err
		}
		if prefix == '-' {
			return nil, errors.New(text)
		}
		return text, nil
	case ':':
		text, err := line()
		if err != nil {
			return nil, err
		}
		return strconv.ParseInt(text, 10, 64)
	case '$':
		text, err := line()
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(text)
		if err != nil {
			return nil, err
		}
		if length < 0 {
			return nil, nil
		}
		payload := make([]byte, length+2)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, err
		}
		return string(payload[:length]), nil
	case '*':
		text, err := line()
		if err != nil {
			return nil, err
		}
		count, err := strconv.Atoi(text)
		if err != nil {
			return nil, err
		}
		values := make([]any, 0, count)
		for index := 0; index < count; index++ {
			value, err := readReply(reader)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("redisbus: unexpected reply %q", prefix)
	}
}
