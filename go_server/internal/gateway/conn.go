package gateway

import (
	"errors"
	"net"
	"sync"
)

// ErrDeviceOffline is returned when a command targets a device that holds no
// live connection on the protocol server that was asked.
var ErrDeviceOffline = errors.New("gateway: device offline")

// deviceConn serialises every write to one connection: decoders answer frames
// from the reader goroutine while the REST layer pushes commands from another.
type deviceConn struct {
	conn    net.Conn
	writeMu sync.Mutex
}

func (c *deviceConn) write(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	_, err := c.conn.Write(payload)
	return err
}

// connectionSet maps device ids to their live connection on one protocol
// server.
type connectionSet struct {
	mu      sync.RWMutex
	devices map[int64]*deviceConn
}

func newConnectionSet() *connectionSet {
	return &connectionSet{devices: make(map[int64]*deviceConn)}
}

// attach binds the connection to a device and returns the connection it
// replaced so the caller can close it.
func (s *connectionSet) attach(deviceID int64, conn *deviceConn) *deviceConn {
	s.mu.Lock()
	defer s.mu.Unlock()

	previous := s.devices[deviceID]
	s.devices[deviceID] = conn
	return previous
}

// detach unhooks the binding only when it still points at conn, so a late
// return from an old connection cannot unhook the new one.
func (s *connectionSet) detach(deviceID int64, conn *deviceConn) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.devices[deviceID] == conn {
		delete(s.devices, deviceID)
	}
}

func (s *connectionSet) get(deviceID int64) *deviceConn {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.devices[deviceID]
}
