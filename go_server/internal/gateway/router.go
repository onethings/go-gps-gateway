package gateway

import (
	"errors"
	"fmt"
	"sync"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
)

var (
	// ErrCommandUnsupported wraps every encoder rejection.
	ErrCommandUnsupported = errors.New("gateway: command not supported by protocol")
	// ErrUnknownProtocol is returned for a protocol that has no server.
	ErrUnknownProtocol = errors.New("gateway: unknown protocol")
)

// Router fans commands out to the protocol server that owns the device, which
// keeps the REST layer free of protocol knowledge.
type Router struct {
	registry *Registry
	base     codec.Base

	mu      sync.RWMutex
	servers map[string]*Server
}

// NewRouter builds a router. The registry supplies the encoders and the
// directory supplies the unique id every encoder needs to address a device.
func NewRouter(registry *Registry, directory DeviceDirectory) *Router {
	return &Router{
		registry: registry,
		base:     codec.Base{Resolver: directory, UniqueIDs: directory},
		servers:  make(map[string]*Server),
	}
}

// Register attaches a running protocol server.
func (r *Router) Register(protocol string, server *Server) {
	if server == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.servers[protocol] = server
}

// EncodeCommand turns a command into the payload the device expects.
func (r *Router) EncodeCommand(protocol string, command *model.Command) ([]byte, error) {
	if command == nil {
		return nil, fmt.Errorf("%w: nil command", ErrCommandUnsupported)
	}

	factory, err := r.registry.Get(protocol)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownProtocol, protocol)
	}

	encoder := factory(r.base).NewEncoder()
	if encoder == nil {
		return nil, fmt.Errorf("%w: %s has no encoder", ErrCommandUnsupported, protocol)
	}

	payload, err := encoder.EncodeCommand(command)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCommandUnsupported, err)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: %s produced no payload", ErrCommandUnsupported, protocol)
	}
	return payload, nil
}

// Send writes an encoded payload to the device connection.
func (r *Router) Send(protocol string, deviceID int64, payload []byte) error {
	server := r.server(protocol)
	if server == nil {
		return fmt.Errorf("%w: %s is not listening", ErrUnknownProtocol, protocol)
	}
	return server.Send(deviceID, payload)
}

// Disconnect closes the connection of a device on the protocol it connected
// with, which is what the session timeout needs.
func (r *Router) Disconnect(protocol string, deviceID int64) error {
	server := r.server(protocol)
	if server == nil {
		return fmt.Errorf("%w: %s is not listening", ErrUnknownProtocol, protocol)
	}
	return server.Disconnect(deviceID)
}

// Online reports whether the device is connected on protocol.
func (r *Router) Online(protocol string, deviceID int64) bool {
	server := r.server(protocol)
	return server != nil && server.Online(deviceID)
}

func (r *Router) server(protocol string) *Server {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.servers[protocol]
}
