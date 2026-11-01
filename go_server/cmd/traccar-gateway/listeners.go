package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"

	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/session"
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
	sessions *session.Manager, logf func(format string, args ...any),
) *listenerSet {
	return &listenerSet{
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
// protocol; a transport the gateway cannot serve yet is an error.
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
	if address == "" {
		return nil
	}
	if transport != model.TransportTCP {
		return fmt.Errorf("%s: transport %q is not supported yet", protocol.Name, transport)
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("%s: listen %s: %w", protocol.Name, address, err)
	}

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

	server, err := gateway.NewServer(l.registry, protocol.Name, l.directory, l.sink, options...)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("%s: %w", protocol.Name, err)
	}

	ctx, cancel := context.WithCancel(l.parent)
	done := make(chan struct{})

	l.mu.Lock()
	l.running[protocol.Name] = &protocolListener{cancel: cancel, done: done}
	protocol.Address = address
	l.configs[protocol.Name] = protocol
	l.mu.Unlock()

	l.router.Register(protocol.Name, server)
	l.logf("%s: listening on %s", protocol.Name, listener.Addr())

	go func() {
		defer close(done)
		if err := server.Serve(ctx, listener); err != nil {
			l.logf("%s: %v", protocol.Name, err)
		}
	}()
	return nil
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
	if transport != model.TransportTCP {
		return fmt.Errorf("%s: transport %q is not supported yet", settings.Protocol, transport)
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
		if restoreErr := l.startAt(protocol, protocol.Address, model.TransportTCP); restoreErr != nil {
			l.logf("%s: restore %s: %v", settings.Protocol, protocol.Address, restoreErr)
		}
		return err
	}
	return nil
}
