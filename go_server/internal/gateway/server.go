package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
)

// defaultMaxFrameSize caps the per connection reassembly buffer.
const defaultMaxFrameSize = 1 << 20

// CommandTracker records the device's answer to a command this server sent.
// The storage layer implements it, so the gateway never imports it. A nil
// tracker leaves acknowledgements unrecorded.
type CommandTracker interface {
	// AcknowledgeCommand closes the command of device that the answer belongs
	// to. An unknown device or an empty queue is a no-op, not an error.
	AcknowledgeCommand(ctx context.Context, deviceID int64, ack codec.CommandAck) error
}

// DeviceConnectHook is implemented by a command tracker that also delivers the
// commands queued for a device once that device holds a connection again.
type DeviceConnectHook interface {
	// DeviceConnected reports a freshly bound session together with the
	// protocol it arrived on, which is what an undelivered command needs to be
	// encoded again.
	DeviceConnected(protocol string, deviceID int64)
}

// Server accepts connections for a single protocol.
type Server struct {
	protocolName string
	base         codec.Base
	factory      Factory
	directory    DeviceDirectory
	sink         Sink
	locations    *LocationCache
	conns        *connectionSet
	commands     CommandTracker
	logf         func(format string, args ...any)
	maxFrameSize int
	readTimeout  time.Duration
	autoRegister bool

	// Pre-resolved instruments keep the per frame path lock free.
	connections  *metrics.Gauge
	frames       *metrics.Counter
	decodeErrors *metrics.Counter
	positions    *metrics.Counter
}

// Option customises a Server.
type Option func(*Server)

// WithLogger overrides the log sink.
func WithLogger(logf func(format string, args ...any)) Option {
	return func(s *Server) {
		if logf != nil {
			s.logf = logf
		}
	}
}

// WithMaxFrameSize overrides the reassembly limit in bytes.
func WithMaxFrameSize(size int) Option {
	return func(s *Server) {
		if size > 0 {
			s.maxFrameSize = size
		}
	}
}

// WithReadTimeout closes idle connections after the given duration.
func WithReadTimeout(timeout time.Duration) Option {
	return func(s *Server) { s.readTimeout = timeout }
}

// WithAutoRegister creates devices for unknown identifiers (needs a directory
// implementing DeviceRegistrar).
func WithAutoRegister() Option {
	return func(s *Server) { s.autoRegister = true }
}

// WithCommandTracker records device acknowledgements as commands ending.
func WithCommandTracker(tracker CommandTracker) Option {
	return func(s *Server) {
		if tracker != nil {
			s.commands = tracker
		}
	}
}

// WithMetrics publishes connection, frame and position counters.
func WithMetrics(set *metrics.Set) Option {
	return func(s *Server) {
		if set == nil {
			return
		}
		s.connections = set.ConnectionsActive.With(s.protocolName)
		s.frames = set.Frames.With(s.protocolName)
		s.decodeErrors = set.DecodeErrors.With(s.protocolName)
		s.positions = set.Positions.With(s.protocolName)
	}
}

// NewServer wires a protocol from the registry. directory and sink may be nil;
// without a directory no session is bound to a connection.
func NewServer(
	registry *Registry, protocolName string, directory DeviceDirectory, sink Sink,
	options ...Option,
) (*Server, error) {

	factory, err := registry.Get(protocolName)
	if err != nil {
		return nil, err
	}

	s := &Server{
		protocolName: protocolName,
		factory:      factory,
		directory:    directory,
		sink:         sink,
		locations:    NewLocationCache(),
		conns:        newConnectionSet(),
		logf:         log.Printf,
		maxFrameSize: defaultMaxFrameSize,
	}
	for _, option := range options {
		option(s)
	}

	s.base = codec.Base{
		Resolver:     directory,
		LastLocation: s.locations,
		UniqueIDs:    directory,
	}
	return s, nil
}

// Serve accepts connections until the listener fails or ctx is cancelled.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	stop := make(chan struct{})
	defer close(stop)

	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-stop:
		}
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("%s: accept: %w", s.protocolName, err)
		}
		go s.ServeConn(ctx, conn)
	}
}

// ServeConn handles one already accepted connection until it closes.
func (s *Server) ServeConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	dc := &deviceConn{conn: conn}
	if s.connections != nil {
		s.connections.Inc()
		defer s.connections.Dec()
	}
	protocol := s.factory(s.base)
	frameDecoder := protocol.NewFrameDecoder()
	decoder := protocol.NewDecoder()

	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}

	var (
		session *codec.DeviceSession
		buffer  = make([]byte, 0, 4096)
		readBuf = make([]byte, 4096)
	)
	// Registered after conn.Close, so the device is unregistered before the
	// socket goes away and Send cannot target a dead connection for long.
	defer func() {
		if session != nil {
			s.conns.detach(session.DeviceID, dc)
		}
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		if s.readTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(s.readTimeout))
		}

		n, readErr := conn.Read(readBuf)
		if n > 0 {
			buffer = append(buffer, readBuf[:n]...)
			if len(buffer) > s.maxFrameSize {
				s.logf("%s: frame buffer exceeded %d bytes", s.protocolName, s.maxFrameSize)
				return
			}

			for {
				frame, consumed := frameDecoder.DecodeFrame(buffer)
				if consumed > 0 {
					buffer = buffer[consumed:]
				}
				if frame == nil {
					break
				}
				if err := s.handleFrame(ctx, dc, decoder, frame, &session); err != nil {
					s.logf("%s: %v", s.protocolName, err)
					return
				}
				if len(buffer) == 0 {
					break
				}
			}
		}

		if readErr != nil {
			var netErr net.Error
			if !errors.Is(readErr, net.ErrClosed) && ctx.Err() == nil &&
				(!errors.As(readErr, &netErr) || !netErr.Timeout()) {
				s.logf("%s: read: %v", s.protocolName, readErr)
			}
			return
		}
	}
}

func (s *Server) handleFrame(
	ctx context.Context, conn *deviceConn, decoder codec.Decoder, frame []byte,
	session **codec.DeviceSession,
) error {

	result, err := decodeFrame(decoder, frame, *session)
	if err != nil {
		if s.decodeErrors != nil {
			s.decodeErrors.Inc()
		}
		return fmt.Errorf("decode: %w", err)
	}
	if result == nil {
		return nil
	}
	if s.frames != nil && result.Consumed {
		s.frames.Inc()
	}

	if result.UniqueID != "" && *session == nil {
		*session = s.bindSession(result.UniqueID)
		s.trackConnection(conn, *session)
		s.notifyConnected(*session)
	}

	for _, position := range result.Positions {
		if position.DeviceID == 0 && *session != nil {
			position.DeviceID = (*session).DeviceID
		}
		if *session == nil && position.DeviceID != 0 {
			// Protocols without a handshake (H02) identify the device in every
			// frame, so the first position is what binds the connection.
			*session = &codec.DeviceSession{DeviceID: position.DeviceID}
			s.trackConnection(conn, *session)
			s.notifyConnected(*session)
		}
		s.locations.Store(position)
		if s.positions != nil {
			s.positions.Inc()
		}
		if s.sink != nil {
			if err := s.sink.Handle(position); err != nil {
				s.logf("%s: sink: %v", s.protocolName, err)
			}
		}
	}

	for _, response := range result.Responses {
		if err := conn.write(response); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}

	// An answer to a command is a database write: log a failure instead of
	// dropping the connection the device is still using for positions.
	if result.CommandAck != nil && s.commands != nil && *session != nil {
		if err := s.commands.AcknowledgeCommand(
			ctx, (*session).DeviceID, *result.CommandAck,
		); err != nil {
			s.logf("%s: acknowledge command: %v", s.protocolName, err)
		}
	}

	return nil
}

func decodeFrame(decoder codec.Decoder, frame []byte, session *codec.DeviceSession) (*codec.Result, error) {
	if sessionDecoder, ok := decoder.(codec.SessionDecoder); ok {
		return sessionDecoder.DecodeWithSession(frame, session)
	}
	return decoder.Decode(frame)
}

// trackConnection records dc as the live connection of the session's device and
// closes the connection it replaces, so a reconnecting device never leaves a
// zombie goroutine holding the same device id.
func (s *Server) trackConnection(conn *deviceConn, session *codec.DeviceSession) {
	if session == nil {
		return
	}

	previous := s.conns.attach(session.DeviceID, conn)
	if previous == nil || previous == conn {
		return
	}

	s.logf("%s: device %d reconnected, closing previous connection",
		s.protocolName, session.DeviceID)
	_ = previous.conn.Close()
}

// notifyConnected tells a queue-owning tracker that the device is reachable
// again. It runs off the read loop because it may write queued commands back
// through the same connection, which must not delay the frame after this one.
func (s *Server) notifyConnected(session *codec.DeviceSession) {
	if session == nil {
		return
	}
	hook, ok := s.commands.(DeviceConnectHook)
	if !ok {
		return
	}
	go hook.DeviceConnected(s.protocolName, session.DeviceID)
}

func (s *Server) bindSession(uniqueID string) *codec.DeviceSession {
	if s.directory == nil {
		return nil
	}

	if resolved, err := s.directory.Resolve(uniqueID); err == nil && resolved != nil {
		return resolved
	}

	if registrar, ok := s.directory.(DeviceRegistrar); ok && s.autoRegister {
		deviceID, err := registrar.Register(uniqueID, s.protocolName)
		if err != nil {
			s.logf("%s: register %q: %v", s.protocolName, uniqueID, err)
			return nil
		}
		return &codec.DeviceSession{DeviceID: deviceID, UniqueID: uniqueID}
	}

	return nil
}

// LastLocation exposes the position cache, e.g. for REST handlers.
func (s *Server) LastLocation(deviceID int64) *model.Position {
	return s.locations.LastLocation(deviceID)
}

// Send writes an already encoded payload to the live connection of deviceID.
// It returns ErrDeviceOffline when the device is not connected to this server.
func (s *Server) Send(deviceID int64, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}

	conn := s.conns.get(deviceID)
	if conn == nil {
		return ErrDeviceOffline
	}
	return conn.write(payload)
}

// Online reports whether the device currently holds a connection.
func (s *Server) Online(deviceID int64) bool {
	return s.conns.get(deviceID) != nil
}
