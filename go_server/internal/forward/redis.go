package forward

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RedisConfig configures the Redis forwarder. URL is redis://[:password@]host:port
// and KeyPrefix defaults to "positions.".
type RedisConfig struct {
	URL       string
	KeyPrefix string
	// Dial defaults to net.Dialer.DialContext; tests pass net.Pipe.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// RedisTransport pushes the position JSON onto a Redis list, which is what the
// Java PositionForwarderRedis does with Jedis LPUSH.
type RedisTransport struct {
	config RedisConfig
	dial   func(ctx context.Context, network, address string) (net.Conn, error)

	mu     sync.Mutex
	conn   net.Conn
	reader *bufio.Reader
}

// NewRedis creates the forwarder.
func NewRedis(config RedisConfig) (*RedisTransport, error) {
	if config.URL == "" {
		return nil, fmt.Errorf("forward redis: empty URL")
	}
	if config.KeyPrefix == "" {
		config.KeyPrefix = "positions."
	}
	dial := config.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		dial = dialer.DialContext
	}
	return &RedisTransport{config: config, dial: dial}, nil
}

// Name implements Transport.
func (t *RedisTransport) Name() string { return "redis" }

// Send implements Transport.
func (t *RedisTransport) Send(ctx context.Context, message Message) error {
	if message.Position == nil || message.Device == nil {
		return fmt.Errorf("forward redis: needs a position and a device")
	}
	encoded, err := json.Marshal(positionMap(*message.Position))
	if err != nil {
		return fmt.Errorf("forward redis: encode: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.connect(ctx); err != nil {
		return err
	}

	key := t.config.KeyPrefix + message.Device.UniqueID
	command := buildRESP("LPUSH", key, string(encoded))
	if _, err := t.conn.Write(command); err != nil {
		t.reset()
		return fmt.Errorf("forward redis: write: %w", err)
	}
	reply, err := t.reader.ReadString('\n')
	if err != nil {
		t.reset()
		return fmt.Errorf("forward redis: read: %w", err)
	}
	if strings.HasPrefix(reply, "-") {
		return fmt.Errorf("forward redis: %s", strings.TrimSpace(reply))
	}
	return nil
}

// connect opens the connection when there is none.
func (t *RedisTransport) connect(ctx context.Context) error {
	if t.conn != nil {
		return nil
	}
	target, err := url.Parse(t.config.URL)
	if err != nil {
		return fmt.Errorf("forward redis: url %q: %w", t.config.URL, err)
	}
	address := target.Host
	if !strings.Contains(address, ":") {
		address += ":6379"
	}
	conn, err := t.dial(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("forward redis: dial %s: %w", address, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	if password, ok := target.User.Password(); ok && password != "" {
		if _, err := conn.Write(buildRESP("AUTH", password)); err != nil {
			_ = conn.Close()
			return fmt.Errorf("forward redis: auth: %w", err)
		}
		reader := bufio.NewReader(conn)
		reply, err := reader.ReadString('\n')
		if err != nil || strings.HasPrefix(reply, "-") {
			_ = conn.Close()
			return fmt.Errorf("forward redis: auth rejected: %s", strings.TrimSpace(reply))
		}
		t.reader = reader
	} else {
		t.reader = bufio.NewReader(conn)
	}
	t.conn = conn
	return nil
}

// reset drops a broken connection so the next send reconnects.
func (t *RedisTransport) reset() {
	if t.conn != nil {
		_ = t.conn.Close()
	}
	t.conn = nil
	t.reader = nil
}

// Close implements Transport.
func (t *RedisTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reset()
	return nil
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
