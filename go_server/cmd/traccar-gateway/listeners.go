package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/session"
)

// Per-protocol keys of the poll transport. A polled device is dialled by the
// gateway, so the protocol section carries the device endpoint instead of a
// listen port.
const (
	pollAddressKey  = "poll.address"
	pollIntervalKey = "poll.interval"
	pollCommandKey  = "poll.command"
	pollDeviceIDKey = "poll.deviceId"
	pollUniqueIDKey = "poll.uniqueId"
)

// listenerSet owns the protocol listeners so an administrator can move one to a
// different port without restarting the process. It is the Go counterpart of
// the Java ServerManager's per-protocol start / stop.
type listenerSet struct {
	// parent is the process context; a listener must not be tied to the HTTP
	// request that started it.
	parent    context.Context
	registry  *gateway.Registry
	directory gateway.DeviceDirectory
	sink      gateway.Sink
	router    *gateway.Router
	meters    *metrics.Set
	commands  *gateway.Dispatcher
	sessions  *session.Manager
	logf      func(format string, args ...any)
	// config is only read for the per-protocol poll keys.
	config *config.Config

	tlsOnce sync.Once
	tls     *tls.Config
	tlsErr  error

	mu      sync.Mutex
	running map[string]*protocolListener
	configs map[string]config.ProtocolSettings
}

// protocolListener is one running protocol server with the cancel function that
// closes it and the channel that reports the goroutine ended.
type protocolListener struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func newListenerSet(
	parent context.Context, registry *gateway.Registry, directory gateway.DeviceDirectory, sink gateway.Sink,
	router *gateway.Router, meters *metrics.Set, commands *gateway.Dispatcher,
	sessions *session.Manager, cfg *config.Config, logf func(format string, args ...any),
) *listenerSet {
	return &listenerSet{
		config:    cfg,
		parent:    parent,
		registry:  registry,
		directory: directory,
		sink:      sink,
		router:    router,
		meters:    meters,
		commands:  commands,
		sessions:  sessions,
		logf:      logf,
		running:   make(map[string]*protocolListener),
		configs:   make(map[string]config.ProtocolSettings),
	}
}

// start opens the listener described by protocol. An empty address disables the
// protocol, except for the poll transport, which has no listener and dials the
// device instead.
func (l *listenerSet) start(protocol config.ProtocolSettings) error {
	transport := protocol.Transport
	if transport == "" {
		transport = model.TransportTCP
	}
	return l.startAt(protocol, protocol.Address, transport)
}

func (l *listenerSet) startAt(
	protocol config.ProtocolSettings, address, transport string,
) error {
	if address == "" && transport != model.TransportPoll {
		return nil
	}

	server, err := l.newServer(protocol)
	if err != nil {
		return fmt.Errorf("%s: %w", protocol.Name, err)
	}

	switch transport {
	case model.TransportTCP, model.TransportTLS:
		return l.listenStream(protocol, address, transport, server)
	case model.TransportUDP:
		return l.listenPacket(protocol, address, transport, server)
	case model.TransportHTTP:
		return l.listenHTTP(protocol, address, transport, server)
	case model.TransportPoll:
		return l.startPoll(protocol, transport, server)
	default:
		return fmt.Errorf("%s: transport %q is not supported", protocol.Name, transport)
	}
}

// newServer builds the protocol server shared by every transport.
func (l *listenerSet) newServer(protocol config.ProtocolSettings) (*gateway.Server, error) {
	options := []gateway.Option{
		gateway.WithReadTimeout(protocol.ReadTimeout),
		gateway.WithMetrics(l.meters),
	}
	if protocol.AutoRegister {
		options = append(options, gateway.WithAutoRegister())
	}
	if l.commands != nil {
		options = append(options, gateway.WithCommandTracker(l.commands))
	}
	if l.sessions != nil {
		options = append(options, gateway.WithSessionTracker(l.sessions))
	}
	return gateway.NewServer(l.registry, protocol.Name, l.directory, l.sink, options...)
}

// listenStream serves a byte stream, plain or over TLS.
func (l *listenerSet) listenStream(
	protocol config.ProtocolSettings, address, transport string, server *gateway.Server,
) error {
	var (
		listener net.Listener
		err      error
	)
	if transport == model.TransportTLS {
		tlsConfig, tlsErr := l.tlsConfig()
		if tlsErr != nil {
			return fmt.Errorf("%s: %w", protocol.Name, tlsErr)
		}
		listener, err = tls.Listen("tcp", address, tlsConfig)
	} else {
		listener, err = net.Listen("tcp", address)
	}
	if err != nil {
		return fmt.Errorf("%s: listen %s: %w", protocol.Name, address, err)
	}

	l.launch(protocol, transport, listener.Addr().String(), server, func(ctx context.Context) error {
		return server.Serve(ctx, listener)
	})
	return nil
}

// listenPacket serves a datagram protocol.
func (l *listenerSet) listenPacket(
	protocol config.ProtocolSettings, address, transport string, server *gateway.Server,
) error {
	conn, err := net.ListenPacket("udp", address)
	if err != nil {
		return fmt.Errorf("%s: listen udp %s: %w", protocol.Name, address, err)
	}

	l.launch(protocol, transport, conn.LocalAddr().String(), server, func(ctx context.Context) error {
		return server.ServePacket(ctx, conn)
	})
	return nil
}

// listenHTTP serves a protocol the device reaches over HTTP.
func (l *listenerSet) listenHTTP(
	protocol config.ProtocolSettings, address, transport string, server *gateway.Server,
) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("%s: listen %s: %w", protocol.Name, address, err)
	}

	l.launch(protocol, transport, listener.Addr().String(), server, func(ctx context.Context) error {
		return server.ServeHTTP(ctx, listener)
	})
	return nil
}

// startPoll runs the client-mode side of a protocol: the gateway dials the
// device and asks for a position on an interval.
func (l *listenerSet) startPoll(
	protocol config.ProtocolSettings, transport string, server *gateway.Server,
) error {
	if l.config == nil {
		return fmt.Errorf("%s: poll needs configuration", protocol.Name)
	}
	section := l.config.Protocol(protocol.Name)
	target := section.String(pollAddressKey)
	if target == "" {
		return fmt.Errorf("%s: transport poll needs %s", protocol.Name, pollAddressKey)
	}

	interval := time.Duration(0)
	if raw := section.String(pollIntervalKey); raw != "" {
		parsed, err := config.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("%s: %s: %w", protocol.Name, pollIntervalKey, err)
		}
		interval = parsed
	}
	commandType := section.String(pollCommandKey)
	if commandType == "" {
		commandType = model.CommandPositionSingle
	}
	deviceID, err := l.pollDeviceID(section.String(pollDeviceIDKey), section.String(pollUniqueIDKey))
	if err != nil {
		return fmt.Errorf("%s: %w", protocol.Name, err)
	}

	// A polled protocol has no listen address, so the log line names the device
	// endpoint instead.
	polled := config.ProtocolSettings{Name: protocol.Name, Transport: transport}
	l.launch(polled, transport, target, server, func(ctx context.Context) error {
		return server.Poll(ctx, gateway.PollConfig{
			Protocol: protocol.Name,
			Address:  target,
			Interval: interval,
			Command:  &model.Command{Type: commandType, DeviceID: deviceID},
		})
	})
	return nil
}

// pollDeviceID finds the device a poller speaks for, either by id or by the
// unique identifier the protocol knows it by.
func (l *listenerSet) pollDeviceID(rawID, uniqueID string) (int64, error) {
	if rawID != "" {
		parsed, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", pollDeviceIDKey, err)
		}
		return parsed, nil
	}
	if uniqueID == "" || l.directory == nil {
		return 0, nil
	}
	session, err := l.directory.Resolve(uniqueID)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", pollUniqueIDKey, uniqueID, err)
	}
	if session == nil {
		return 0, nil
	}
	return session.DeviceID, nil
}

// launch records the server as the live one for its protocol and starts serving.
func (l *listenerSet) launch(
	protocol config.ProtocolSettings, transport, target string, server *gateway.Server,
	serve func(context.Context) error,
) {
	ctx, cancel := context.WithCancel(l.parent)
	done := make(chan struct{})

	l.mu.Lock()
	l.running[protocol.Name] = &protocolListener{cancel: cancel, done: done}
	protocol.Transport = transport
	l.configs[protocol.Name] = protocol
	l.mu.Unlock()

	l.router.Register(protocol.Name, server)
	l.logf("%s: listening on %s (%s)", protocol.Name, target, transport)

	go func() {
		defer close(done)
		if err := serve(ctx); err != nil {
			l.logf("%s: %v", protocol.Name, err)
		}
	}()
}

// tlsConfig loads the certificate once. A TLS protocol without one is a
// configuration error the caller reports, not a panic.
func (l *listenerSet) tlsConfig() (*tls.Config, error) {
	l.tlsOnce.Do(func() {
		if l.config == nil {
			l.tlsErr = fmt.Errorf("transport tls needs %s and %s", config.ServerTLSCertificate, config.ServerTLSKey)
			return
		}
		certificate := l.config.String(config.ServerTLSCertificate)
		key := l.config.String(config.ServerTLSKey)
		if certificate == "" || key == "" {
			l.tlsErr = fmt.Errorf("transport tls needs %s and %s", config.ServerTLSCertificate, config.ServerTLSKey)
			return
		}
		loaded, err := tls.LoadX509KeyPair(certificate, key)
		if err != nil {
			l.tlsErr = fmt.Errorf("transport tls: %w", err)
			return
		}
		l.tls = &tls.Config{Certificates: []tls.Certificate{loaded}, MinVersion: tls.VersionTLS12}
	})
	return l.tls, l.tlsErr
}

// stop closes the listener of one protocol and waits for its accept loop to
// end.
func (l *listenerSet) stop(name string) {
	l.mu.Lock()
	running := l.running[name]
	delete(l.running, name)
	l.mu.Unlock()

	if running == nil {
		return
	}
	running.cancel()
	<-running.done
	l.router.Unregister(name)
}

// waitAll stops every listener. It is called on shutdown, where the parent
// context is already cancelled, so it only has to wait.
func (l *listenerSet) waitAll() {
	l.mu.Lock()
	names := make([]string, 0, len(l.running))
	for name := range l.running {
		names = append(names, name)
	}
	l.mu.Unlock()

	for _, name := range names {
		l.stop(name)
	}
}

// applyStoredProtocolSettings overrides the configured addresses with the rows
// an administrator stored, so a port changed from the web interface survives a
// restart. A zero port disables the protocol.
func applyStoredProtocolSettings(protocols []config.ProtocolSettings, stored []model.ProtocolSettings) {
	rows := make(map[string]model.ProtocolSettings, len(stored))
	for _, row := range stored {
		rows[row.Protocol] = row
	}
	for index := range protocols {
		row, ok := rows[protocols[index].Name]
		if !ok {
			continue
		}
		if row.Port <= 0 {
			protocols[index].Address = ""
			continue
		}
		host := "0.0.0.0"
		if parsedHost, _, err := net.SplitHostPort(protocols[index].Address); err == nil && parsedHost != "" {
			host = parsedHost
		}
		protocols[index].Address = net.JoinHostPort(host, strconv.Itoa(row.Port))
		if row.Transport != "" {
			protocols[index].Transport = row.Transport
		}
	}
}

// ApplyProtocolSettings moves one protocol to the port and transport an
// administrator stored. The old listener is stopped first because the new one
// may want the same port; when the new bind fails the old address is restored.
func (l *listenerSet) ApplyProtocolSettings(ctx context.Context, settings model.ProtocolSettings) error {
	l.mu.Lock()
	protocol, known := l.configs[settings.Protocol]
	transport := settings.Transport
	if transport == "" {
		transport = model.TransportTCP
	}
	l.mu.Unlock()

	if !known {
		return fmt.Errorf("unknown protocol %q", settings.Protocol)
	}
	if !model.ValidTransport(transport) {
		return fmt.Errorf("%s: unknown transport %q", settings.Protocol, transport)
	}
	previous := config.ProtocolSettings{Name: protocol.Name, Transport: protocol.Transport}
	previous.Address = protocol.Address
	if previous.Transport == "" {
		previous.Transport = model.TransportTCP
	}

	host := "0.0.0.0"
	if protocol.Address != "" {
		if parsedHost, _, err := net.SplitHostPort(protocol.Address); err == nil && parsedHost != "" {
			host = parsedHost
		}
	}

	l.stop(settings.Protocol)
	if settings.Port <= 0 {
		// Port zero means the protocol is disabled, which is how the Java
		// server turns a listener off.
		return nil
	}

	address := net.JoinHostPort(host, strconv.Itoa(settings.Port))
	if err := l.startAt(protocol, address, transport); err != nil {
		if restoreErr := l.startAt(previous, previous.Address, previous.Transport); restoreErr != nil {
			l.logf("%s: restore %s: %v", settings.Protocol, protocol.Address, restoreErr)
		}
		return err
	}
	return nil
}
