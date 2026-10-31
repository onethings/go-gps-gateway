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
// devices for unknown identifiers on the fly.
type DeviceRegistrar interface {
	Register(uniqueID string) (int64, error)
}

// MemoryDirectory is an in-memory directory, useful for tests and for running
// the gateway without a database.
type MemoryDirectory struct {
	mu         sync.RWMutex
	byUniqueID map[string]int64
	byID       map[int64]string
	nextID     int64
}

// NewMemoryDirectory creates an empty directory.
func NewMemoryDirectory() *MemoryDirectory {
	return &MemoryDirectory{
		byUniqueID: make(map[string]int64),
		byID:       make(map[int64]string),
	}
}

// Register adds a device and returns its id. Registering an existing
// identifier returns the previous id.
func (d *MemoryDirectory) Register(uniqueID string) (int64, error) {
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
	return &codec.DeviceSession{DeviceID: id, UniqueID: uniqueID}, nil
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
