package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"traccar-server_go/go_server/internal/codec"
)

// datagramIdle is how long the state of a datagram peer is kept after its last
// packet. A datagram protocol has no connection, so the peer address is the
// only thing that says "this is still the same device".
const datagramIdle = 5 * time.Minute

// ServePacket serves a datagram listener. Every packet is split into frames the
// same way ServeConn splits a byte stream, with the decoder and the session
// kept per remote address.
func (s *Server) ServePacket(ctx context.Context, conn net.PacketConn) error {
	stop := make(chan struct{})
	defer close(stop)

	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	states := newDatagramStates(datagramIdle)
	buffer := make([]byte, 65535)
	for {
		read, addr, err := conn.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("%s: read: %w", s.protocolName, err)
		}
		if read == 0 {
			continue
		}

		state := states.get(addr, func() *datagramState {
			protocol := s.factory(s.base)
			return &datagramState{
				conn:    &deviceConn{conn: &datagramConn{conn: conn, addr: addr}},
				frames:  protocol.NewFrameDecoder(),
				decoder: protocol.NewDecoder(),
			}
		})
		// The read buffer is reused, so the packet is copied before the next
		// one overwrites it. Frames left over from a partial packet stay in the
		// state buffer and are completed by the next packet.
		s.serveDatagram(ctx, state, append([]byte(nil), buffer[:read]...))
	}
}

// serveDatagram appends one packet to the peer's buffer and handles every frame
// it completes.
func (s *Server) serveDatagram(ctx context.Context, state *datagramState, packet []byte) {
	state.buffer = append(state.buffer, packet...)
	if len(state.buffer) > s.maxFrameSize {
		s.logf("%s: datagram exceeded %d bytes", s.protocolName, s.maxFrameSize)
		state.buffer = nil
		return
	}

	if state.frames == nil {
		frame := state.buffer
		state.buffer = nil
		s.handleDatagramFrame(ctx, state, frame)
		return
	}

	for {
		frame, consumed := state.frames.DecodeFrame(state.buffer)
		if consumed > 0 {
			state.buffer = state.buffer[consumed:]
		}
		if frame == nil {
			break
		}
		if !s.handleDatagramFrame(ctx, state, frame) {
			state.buffer = nil
			return
		}
		if len(state.buffer) == 0 {
			break
		}
	}
}

// handleDatagramFrame reports whether the peer state may keep being used. A
// decode error only drops the frame: a datagram protocol has no connection to
// tear down, so the next packet gets another chance.
func (s *Server) handleDatagramFrame(ctx context.Context, state *datagramState, frame []byte) bool {
	if err := s.handleFrame(ctx, state.conn, state.decoder, frame, &state.session); err != nil {
		s.logf("%s: %v", s.protocolName, err)
		return false
	}
	return true
}

// datagramState is the per-peer decoder state of a datagram listener.
type datagramState struct {
	conn    *deviceConn
	frames  codec.FrameDecoder
	decoder codec.Decoder
	session *codec.DeviceSession
	buffer  []byte
	used    time.Time
}

// datagramStates holds the per-peer state and expires the peers that stopped
// sending. It is only touched by the ServePacket read loop, so it needs no lock.
type datagramStates struct {
	idle      time.Duration
	items     map[string]*datagramState
	lastPrune time.Time
}

func newDatagramStates(idle time.Duration) *datagramStates {
	return &datagramStates{
		idle:      idle,
		items:     make(map[string]*datagramState),
		lastPrune: time.Now(),
	}
}

// get returns the state of a peer, creating it with create on first sight.
func (m *datagramStates) get(addr net.Addr, create func() *datagramState) *datagramState {
	m.prune()

	key := addr.String()
	state, ok := m.items[key]
	if !ok {
		state = create()
		m.items[key] = state
	}
	state.used = time.Now()
	return state
}

// prune forgets the peers that have been idle for longer than the timeout.
func (m *datagramStates) prune() {
	now := time.Now()
	if m.idle <= 0 || now.Sub(m.lastPrune) < m.idle {
		return
	}
	m.lastPrune = now
	for key, state := range m.items {
		if now.Sub(state.used) > m.idle {
			delete(m.items, key)
		}
	}
}

// datagramConn adapts a datagram peer to net.Conn so the shared frame handling
// can write the protocol responses back to the address the packet came from.
// Close is a no-op: the listener is shared by every peer.
type datagramConn struct {
	conn net.PacketConn
	addr net.Addr
}

func (c *datagramConn) Read([]byte) (int, error) { return 0, io.EOF }

func (c *datagramConn) Write(payload []byte) (int, error) {
	return c.conn.WriteTo(payload, c.addr)
}

func (c *datagramConn) Close() error { return nil }

func (c *datagramConn) LocalAddr() net.Addr { return c.conn.LocalAddr() }

func (c *datagramConn) RemoteAddr() net.Addr { return c.addr }

func (c *datagramConn) SetDeadline(time.Time) error { return nil }

func (c *datagramConn) SetReadDeadline(time.Time) error { return nil }

func (c *datagramConn) SetWriteDeadline(time.Time) error { return nil }
