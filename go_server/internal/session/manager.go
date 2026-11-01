// Package session keeps what the gateway knows about a device between frames:
// whether it holds a connection, when it last reported, and whether it is
// moving. It is the Go counterpart of the Java server's ConnectionManager, its
// DeviceSession and its motion state processor.
package session

import (
	"context"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// Device status values, matching the Java server's Device.STATUS_*.
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
	StatusUnknown = "unknown"
)

const (
	// defaultSweep is how often the session timeout is checked.
	defaultSweep = time.Second
	// defaultWriteTimeout bounds one status or event write.
	defaultWriteTimeout = 5 * time.Second
	// transitionQueue bounds the writes waiting for the database. A device that
	// flaps faster than the database can write loses a transition, never the
	// read loop.
	transitionQueue = 256
)

// Store records the connection state of a device.
type Store interface {
	SetDeviceStatus(ctx context.Context, deviceID int64, status string, at time.Time) error
}

// Events records the deviceOnline and deviceOffline rows. A manager without one
// keeps its own state only, which is what the run without a database does.
type Events interface {
	InsertEvents(ctx context.Context, events []*model.Event) error
}

// Config configures a Manager.
type Config struct {
	Store  Store
	Events Events
	// Timeout expires a session that has not sent a frame for this long and
	// declares the device offline, the way the Java server's server.timeout
	// does. Zero keeps sessions open until the connection closes.
	Timeout time.Duration
	// Sweep is how often the timeout is checked; defaults to one second.
	Sweep time.Duration
	// Close drops the connection of a session that timed out, so the gateway
	// stops holding a socket for a device it has already declared offline. The
	// protocol is needed because only the server of that protocol owns the
	// connection.
	Close func(protocol string, deviceID int64)
	// Now overrides the clock, for tests.
	Now func() time.Time
	// OnStatus observes every transition the manager records, so the API can
	// push the new device state to the open sockets. It runs on the writer
	// goroutine and must not block.
	OnStatus func(ctx context.Context, deviceID int64, status string)
	// Logger reports dropped transitions and failed writes.
	Logger func(format string, args ...any)
}

// Session is one device the gateway knows about.
type Session struct {
	DeviceID int64
	Protocol string
	// Remote is the address the connection came from, for the log.
	Remote string
	// OpenedAt is when the current connection was bound.
	OpenedAt time.Time
	// LastSeen is when the last frame arrived.
	LastSeen time.Time
	// Status is StatusOnline while the connection is bound and StatusOffline
	// after it went away or timed out.
	Status string
}

// transition is one status change on its way to the database.
type transition struct {
	deviceID int64
	status   string
	at       time.Time
	event    *model.Event
}

// Manager tracks the device sessions. Every method is safe for concurrent use
// and none of them waits for the database.
type Manager struct {
	store    Store
	events   Events
	timeout  time.Duration
	sweep    time.Duration
	closeFn  func(protocol string, deviceID int64)
	now      func() time.Time
	onStatus func(ctx context.Context, deviceID int64, status string)
	logf     func(format string, args ...any)

	mu       sync.RWMutex
	sessions map[int64]*Session

	queue chan transition

	statsMu sync.Mutex
	dropped int64

	wg sync.WaitGroup
}

// New creates a manager. Run has to be called for the transitions to reach the
// database and for the timeout to take effect.
func New(config Config) *Manager {
	manager := &Manager{
		store:    config.Store,
		events:   config.Events,
		timeout:  config.Timeout,
		sweep:    config.Sweep,
		closeFn:  config.Close,
		now:      config.Now,
		onStatus: config.OnStatus,
		logf:     config.Logger,
		sessions: make(map[int64]*Session),
		queue:    make(chan transition, transitionQueue),
	}
	if manager.sweep <= 0 {
		manager.sweep = defaultSweep
	}
	if manager.now == nil {
		manager.now = time.Now
	}
	if manager.logf == nil {
		manager.logf = func(string, ...any) {}
	}
	return manager
}

// Run serves the write queue and the timeout sweep until ctx is cancelled, then
// writes what is still queued.
func (m *Manager) Run(ctx context.Context) {
	m.wg.Add(2)
	go func() {
		defer m.wg.Done()
		m.writeLoop(ctx)
	}()
	go func() {
		defer m.wg.Done()
		m.sweepLoop(ctx)
	}()
	m.wg.Wait()
}

// Connect records a freshly bound session. A device that reconnects before its
// disconnect was seen keeps its one session, so a flapping link does not produce
// an online / offline pair per attempt.
func (m *Manager) Connect(protocol string, deviceID int64, remote string) {
	if deviceID == 0 {
		return
	}
	now := m.now()

	m.mu.Lock()
	if session := m.sessions[deviceID]; session != nil {
		session.LastSeen = now
		session.Protocol = protocol
		session.Remote = remote
		if session.Status == StatusOnline {
			m.mu.Unlock()
			return
		}
	}
	m.sessions[deviceID] = &Session{
		DeviceID: deviceID,
		Protocol: protocol,
		Remote:   remote,
		OpenedAt: now,
		LastSeen: now,
		Status:   StatusOnline,
	}
	m.mu.Unlock()

	event := model.NewEvent(model.EventDeviceOnline, deviceID, now)
	event.Set("protocol", protocol)
	m.enqueue(transition{deviceID: deviceID, status: StatusOnline, at: now, event: event})
}

// Touch records that a frame arrived. It never touches the database: it runs on
// the device read loop.
func (m *Manager) Touch(deviceID int64) {
	if deviceID == 0 {
		return
	}
	now := m.now()

	m.mu.Lock()
	if session := m.sessions[deviceID]; session != nil {
		session.LastSeen = now
	}
	m.mu.Unlock()
}

// Disconnect records that the connection of a device ended.
func (m *Manager) Disconnect(deviceID int64) {
	if deviceID == 0 {
		return
	}
	now := m.now()

	m.mu.Lock()
	session := m.sessions[deviceID]
	if session == nil || session.Status != StatusOnline {
		m.mu.Unlock()
		return
	}
	session.Status = StatusOffline
	m.mu.Unlock()

	m.enqueue(transition{
		deviceID: deviceID,
		status:   StatusOffline,
		at:       now,
		event:    model.NewEvent(model.EventDeviceOffline, deviceID, now),
	})
}

// SessionOpened implements gateway.SessionTracker, which is how the protocol
// servers tell the manager that a connection bound to a device.
func (m *Manager) SessionOpened(protocol string, deviceID int64, remote string) {
	m.Connect(protocol, deviceID, remote)
}

// SessionData implements gateway.SessionTracker: a frame arrived.
func (m *Manager) SessionData(deviceID int64) { m.Touch(deviceID) }

// SessionClosed implements gateway.SessionTracker: the connection ended.
func (m *Manager) SessionClosed(deviceID int64) { m.Disconnect(deviceID) }

// Get returns the session of a device.
func (m *Manager) Get(deviceID int64) (Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, ok := m.sessions[deviceID]
	if !ok {
		return Session{}, false
	}
	return *session, true
}

// Online reports whether the device holds a session.
func (m *Manager) Online(deviceID int64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, ok := m.sessions[deviceID]
	return ok && session.Status == StatusOnline
}

// OnlineCount reports how many devices hold a session.
func (m *Manager) OnlineCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, session := range m.sessions {
		if session.Status == StatusOnline {
			count++
		}
	}
	return count
}

// Sessions returns a copy of every session, online or not, so a caller cannot
// mutate what the manager tracks.
func (m *Manager) Sessions() []Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, *session)
	}
	return sessions
}

// Dropped reports how many transitions were lost because the queue was full.
func (m *Manager) Dropped() int64 {
	m.statsMu.Lock()
	defer m.statsMu.Unlock()
	return m.dropped
}

// enqueue hands a transition to the writer without blocking the read loop.
func (m *Manager) enqueue(change transition) {
	select {
	case m.queue <- change:
		return
	default:
	}

	m.statsMu.Lock()
	m.dropped++
	m.statsMu.Unlock()
	m.logf("session: queue full, dropped %s for device %d", change.status, change.deviceID)
}

// writeLoop writes the queued transitions until ctx is cancelled.
func (m *Manager) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			m.drain()
			return
		case change := <-m.queue:
			m.write(change)
		}
	}
}

// drain writes the transitions still queued at shutdown, so a device that
// disconnects while the gateway stops is not left looking online forever.
func (m *Manager) drain() {
	for {
		select {
		case change := <-m.queue:
			m.write(change)
		default:
			return
		}
	}
}

// write persists one transition.
func (m *Manager) write(change transition) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultWriteTimeout)
	defer cancel()

	if m.store != nil {
		if err := m.store.SetDeviceStatus(ctx, change.deviceID, change.status, change.at); err != nil {
			m.logf("session: status of device %d: %v", change.deviceID, err)
		}
	}
	if m.events != nil && change.event != nil {
		if err := m.events.InsertEvents(ctx, []*model.Event{change.event}); err != nil {
			m.logf("session: event for device %d: %v", change.deviceID, err)
		}
	}
	if m.onStatus != nil {
		m.onStatus(ctx, change.deviceID, change.status)
	}
}

// sweepLoop expires silent sessions until ctx is cancelled.
func (m *Manager) sweepLoop(ctx context.Context) {
	if m.timeout <= 0 {
		<-ctx.Done()
		return
	}

	ticker := time.NewTicker(m.sweep)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.expire()
		}
	}
}

// expire takes the sessions that have been silent for longer than the timeout
// offline. The connection is closed through the Close hook: a device that is not
// heard must not keep a socket that the gateway would only notice at shutdown.
func (m *Manager) expire() {
	now := m.now()

	type expiredSession struct {
		deviceID int64
		protocol string
	}
	var expired []expiredSession

	m.mu.Lock()
	for deviceID, session := range m.sessions {
		if session.Status != StatusOnline || now.Sub(session.LastSeen) <= m.timeout {
			continue
		}
		session.Status = StatusOffline
		expired = append(expired, expiredSession{deviceID: deviceID, protocol: session.Protocol})
	}
	m.mu.Unlock()

	for _, session := range expired {
		event := model.NewEvent(model.EventDeviceOffline, session.deviceID, now)
		event.Set("reason", "timeout")
		m.enqueue(transition{
			deviceID: session.deviceID,
			status:   StatusOffline,
			at:       now,
			event:    event,
		})

		m.logf("session: device %d timed out after %s", session.deviceID, m.timeout)
		if m.closeFn != nil {
			m.closeFn(session.protocol, session.deviceID)
		}
	}
}
