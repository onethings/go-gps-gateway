package forward

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// NetworkConfig configures the plain TCP or UDP forwarder. It is the Go
// counterpart of the Java NetworkForwarder: every position or event is written
// as one JSON line to the destination.
type NetworkConfig struct {
	// Address is host:port of the destination.
	Address string
	// Protocol is "tcp" or "udp". Empty means tcp.
	Protocol string
	// Dial defaults to net.Dialer.DialContext; tests pass net.Pipe.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// NetworkTransport writes the message JSON to a socket.
type NetworkTransport struct {
	config NetworkConfig
	dial   func(ctx context.Context, network, address string) (net.Conn, error)

	mu   sync.Mutex
	conn net.Conn
}

// NewNetwork creates the forwarder.
func NewNetwork(config NetworkConfig) (*NetworkTransport, error) {
	if config.Address == "" {
		return nil, fmt.Errorf("forward network: empty address")
	}
	switch strings.ToLower(config.Protocol) {
	case "", "tcp":
		config.Protocol = "tcp"
	case "udp":
		config.Protocol = "udp"
	default:
		return nil, fmt.Errorf("forward network: unknown protocol %q", config.Protocol)
	}
	dial := config.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		dial = dialer.DialContext
	}
	return &NetworkTransport{config: config, dial: dial}, nil
}

// Name implements Transport.
func (t *NetworkTransport) Name() string { return "network" }

// Send implements Transport. A UDP message is a single datagram; TCP keeps the
// connection open and frames one JSON object per line.
func (t *NetworkTransport) Send(ctx context.Context, message Message) error {
	encoded, err := json.Marshal(payload(message))
	if err != nil {
		return fmt.Errorf("forward network: encode: %w", err)
	}
	encoded = append(encoded, '\n')

	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.connect(ctx); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = t.conn.SetWriteDeadline(deadline)
	}
	if _, err := t.conn.Write(encoded); err != nil {
		t.reset()
		return fmt.Errorf("forward network: write to %s: %w", t.config.Address, err)
	}
	return nil
}

// connect opens the socket when there is none. A UDP socket is connectionless
// and is kept the same way; a lost datagram is not worth reconnecting for.
func (t *NetworkTransport) connect(ctx context.Context) error {
	if t.conn != nil {
		return nil
	}
	conn, err := t.dial(ctx, t.config.Protocol, t.config.Address)
	if err != nil {
		return fmt.Errorf("forward network: dial %s: %w", t.config.Address, err)
	}
	t.conn = conn
	return nil
}

func (t *NetworkTransport) reset() {
	if t.conn != nil {
		_ = t.conn.Close()
		t.conn = nil
	}
}

// Close implements Transport.
func (t *NetworkTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn == nil {
		return nil
	}
	err := t.conn.Close()
	t.conn = nil
	return err
}
