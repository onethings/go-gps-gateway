package gateway

import (
	"errors"
	"sync"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
)

// ErrUnknownDevice is returned when a unique id has no device record.
var ErrUnknownDevice = errors.New("gateway: unknown device")

// DeviceDirectory maps protocol identifiers to device ids and back. It is the
// only thing the gateway needs from the persistence layer.
type DeviceDirectory interface {
	codec.DeviceResolver
	codec.UniqueIDProvider
}

// DeviceRegistrar is optionally implemented by a directory that can create
// devices for unknown identifiers on the fly. The protocol is recorded on the
// device row so that downlink commands know which encoder to use.
type DeviceRegistrar interface {
	Register(uniqueID, protocol string) (int64, error)
}

// registeringResolver resolves an identifier and creates the device when the
// directory does not know it yet.
//
// It exists because two protocols resolve the identifier inside the decoder
// instead of reporting it on the result: H02 has no handshake frame at all, and
// JT808 resolves the ten digit terminal id from the frame header before it can
// decide anything. Without this wrapper -auto-register only worked for the
// protocols that hand the identifier to the gateway, so a H02 or JT808 device
// connecting to an empty database was dropped on its first frame and never
// registered.
type registeringResolver struct {
	resolver  codec.DeviceResolver
	registrar DeviceRegistrar
	protocol  string
}

// Resolve returns the device session, registering the identifier first when it
// is unknown.
func (r *registeringResolver) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	session, err := r.resolver.Resolve(uniqueID)
	if err != nil || session != nil {
		return session, err
	}

	deviceID, err := r.registrar.Register(uniqueID, r.protocol)
	if err != nil {
		return nil, err
	}
	return &codec.DeviceSession{DeviceID: deviceID, UniqueID: uniqueID}, nil
}

// MemoryDirectory is an in-memory directory, useful for tests and for running
// the gateway without a database.
type MemoryDirectory struct {
	mu         sync.RWMutex
	byUniqueID map[string]int64
	byID       map[int64]string
	models     map[string]string
	nextID     int64
}

// NewMemoryDirectory creates an empty directory.
func NewMemoryDirectory() *MemoryDirectory {
	return &MemoryDirectory{
		byUniqueID: make(map[string]int64),
		byID:       make(map[int64]string),
		models:     make(map[string]string),
	}
}

// Register adds a device and returns its id. Registering an existing
// identifier returns the previous id. The protocol is ignored here: the
// in-memory directory exists for tests and for running without a database,
// where the REST API is disabled anyway.
func (d *MemoryDirectory) Register(uniqueID, _ string) (int64, error) {
	if uniqueID == "" {
		return 0, ErrUnknownDevice
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if id, ok := d.byUniqueID[uniqueID]; ok {
		return id, nil
	}

	d.nextID++
	d.byUniqueID[uniqueID] = d.nextID
	d.byID[d.nextID] = uniqueID
	return d.nextID, nil
}

// Resolve implements codec.DeviceResolver.
func (d *MemoryDirectory) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	id, ok := d.byUniqueID[uniqueID]
	if !ok {
		return nil, nil
	}
	return &codec.DeviceSession{DeviceID: id, UniqueID: uniqueID, Model: d.models[uniqueID]}, nil
}

// SetModel stores the model reported for a device. It exists for tests and for
// the database-less setup, where nothing else can edit the device row.
func (d *MemoryDirectory) SetModel(uniqueID, model string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.byUniqueID[uniqueID]; !ok {
		return ErrUnknownDevice
	}
	d.models[uniqueID] = model
	return nil
}

// UniqueID implements codec.UniqueIDProvider.
func (d *MemoryDirectory) UniqueID(deviceID int64) (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	uniqueID, ok := d.byID[deviceID]
	if !ok {
		return "", ErrUnknownDevice
	}
	return uniqueID, nil
}

// LocationCache keeps the most recent position per device, which the decoders
// use for messages that only carry status or cell information.
type LocationCache struct {
	mu        sync.RWMutex
	locations map[int64]*model.Position
}

// NewLocationCache creates an empty cache.
func NewLocationCache() *LocationCache {
	return &LocationCache{locations: make(map[int64]*model.Position)}
}

// LastLocation implements codec.LastLocationProvider.
func (c *LocationCache) LastLocation(deviceID int64) *model.Position {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.locations[deviceID]
}

// Store records a position as the latest known one for its device.
func (c *LocationCache) Store(position *model.Position) {
	if position == nil || position.DeviceID == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.locations[position.DeviceID] = position
}

// Sink receives every decoded position.
type Sink interface {
	Handle(position *model.Position) error
}

// SinkFunc adapts a function to the Sink interface.
type SinkFunc func(position *model.Position) error

// Handle implements Sink.
func (f SinkFunc) Handle(position *model.Position) error { return f(position) }
