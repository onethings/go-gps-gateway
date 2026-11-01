package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/websocket"
)

const (
	// socketSendQueue is how many updates may wait for one connection. A client
	// that falls further behind loses updates instead of slowing the device
	// goroutine that published them.
	socketSendQueue = 64
	// socketKeepalive is how often an empty object goes out, which is what the
	// Java server's AsyncSocket sends on keepalive.
	socketKeepalive = 30 * time.Second
)

// LatestPositionQuerier seeds a socket connection with the newest position of
// every device the caller can see. A nil device list means every device.
type LatestPositionQuerier interface {
	LatestPositions(ctx context.Context, deviceIDs []int64) ([]model.Position, error)
}

// WithSocket enables GET /api/socket: the WebSocket the front end keeps open to
// receive position, event and device changes. Without it the route reports 501.
func WithSocket(hub *SocketHub, latest LatestPositionQuerier) Option {
	return func(s *Server) {
		s.socketHub = hub
		s.latestPositions = latest
	}
}

// socketChannel names one kind of update the hub broadcasts. A connection only
// receives the kinds its token carries a read scope for.
type socketChannel int

const (
	socketPositions socketChannel = iota
	socketEvents
	socketDevices
)

// socketChannels records which update kinds one connection wants.
type socketChannels struct {
	positions bool
	events    bool
	devices   bool
}

func socketChannelsFor(principal *auth.Principal) socketChannels {
	return socketChannels{
		positions: principal.Has(auth.ScopeReadPositions),
		events:    principal.Has(auth.ScopeReadAlerts),
		devices:   principal.Has(auth.ScopeReadDevices),
	}
}

func (c socketChannels) enabled(channel socketChannel) bool {
	switch channel {
	case socketPositions:
		return c.positions
	case socketEvents:
		return c.events
	case socketDevices:
		return c.devices
	default:
		return false
	}
}

func (c socketChannels) any() bool { return c.positions || c.events || c.devices }

// SocketHub fans position, event and device updates out to the open socket
// connections. Publishers run on device and session goroutines, so publishing
// never blocks: a slow connection loses updates instead.
type SocketHub struct {
	mu      sync.Mutex
	clients map[*socketClient]struct{}
}

// NewSocketHub creates an empty hub. The gateway creates it before the sinks so
// they can publish while the API server is still being assembled.
func NewSocketHub() *SocketHub {
	return &SocketHub{clients: make(map[*socketClient]struct{})}
}

// PublishPosition sends a position to every connection that may see its device.
func (h *SocketHub) PublishPosition(position *model.Position) {
	if h == nil || position == nil || h.empty() {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"positions": []positionResponse{newPositionResponse(*position)},
	})
	if err != nil {
		return
	}
	h.broadcast(socketPositions, position.DeviceID, payload)
}

// PublishEvent sends an event to every connection that may see its device.
func (h *SocketHub) PublishEvent(event *model.Event) {
	if h == nil || event == nil || h.empty() {
		return
	}
	payload, err := json.Marshal(map[string]any{"events": []eventResponse{newEventResponse(*event)}})
	if err != nil {
		return
	}
	h.broadcast(socketEvents, event.DeviceID, payload)
}

// PublishDevice sends a device snapshot to every connection that may see it.
func (h *SocketHub) PublishDevice(device *model.Device) {
	if h == nil || device == nil || h.empty() {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"devices": []deviceResponse{newDeviceResponse(*device, true)},
	})
	if err != nil {
		return
	}
	h.broadcast(socketDevices, device.ID, payload)
}

// empty reports whether no connection is open. The publishers use it to skip
// the JSON encoding entirely on a gateway without a dashboard attached.
func (h *SocketHub) empty() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients) == 0
}

func (h *SocketHub) broadcast(channel socketChannel, deviceID int64, payload []byte) {
	h.mu.Lock()
	clients := make([]*socketClient, 0, len(h.clients))
	for client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.Unlock()

	for _, client := range clients {
		client.deliver(channel, deviceID, payload)
	}
}

func (h *SocketHub) subscribe(conn *websocket.Conn, devices []int64, channels socketChannels) *socketClient {
	client := &socketClient{
		hub:        h,
		conn:       conn,
		all:        devices == nil,
		channels:   channels,
		send:       make(chan []byte, socketSendQueue),
		done:       make(chan struct{}),
		writerDone: make(chan struct{}),
	}
	if devices != nil {
		client.devices = make(map[int64]struct{}, len(devices))
		for _, deviceID := range devices {
			client.devices[deviceID] = struct{}{}
		}
	}

	h.mu.Lock()
	h.clients[client] = struct{}{}
	h.mu.Unlock()
	return client
}

func (h *SocketHub) remove(client *socketClient) {
	h.mu.Lock()
	delete(h.clients, client)
	h.mu.Unlock()
}

// Count reports the open connections, which the tests and operators use.
func (h *SocketHub) Count() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// socketClient is one open connection.
type socketClient struct {
	hub      *SocketHub
	conn     *websocket.Conn
	all      bool
	devices  map[int64]struct{}
	channels socketChannels
	send     chan []byte
	done     chan struct{}
	// writerDone closes when the write loop has stopped, so the reader can wait
	// for it before the handler returns.
	writerDone chan struct{}
	closeOnce  sync.Once
}

func (c *socketClient) deliver(channel socketChannel, deviceID int64, payload []byte) {
	if !c.channels.enabled(channel) {
		return
	}
	if !c.all {
		if _, ok := c.devices[deviceID]; !ok {
			return
		}
	}
	select {
	case c.send <- payload:
	default:
		// The connection is behind: dropping an update keeps the publisher
		// (a device read loop) from waiting on a slow browser.
	}
}

// seed queues the first message, which is always the latest positions.
func (c *socketClient) seed(positions []model.Position) {
	response := make([]positionResponse, 0, len(positions))
	for _, position := range positions {
		response = append(response, newPositionResponse(position))
	}
	payload, err := json.Marshal(map[string]any{"positions": response})
	if err != nil {
		return
	}
	select {
	case c.send <- payload:
	default:
	}
}

// close unsubscribes and drops the connection. Calling it twice is safe.
func (c *socketClient) close() {
	c.closeOnce.Do(func() {
		c.hub.remove(c)
		close(c.done)
		_ = c.conn.Close()
	})
}

// writeLoop sends queued updates and the periodic keepalive.
func (c *socketClient) writeLoop() {
	defer close(c.writerDone)

	keepalive := time.NewTicker(socketKeepalive)
	defer keepalive.Stop()

	for {
		select {
		case <-c.done:
			return
		case payload := <-c.send:
			if err := c.conn.WriteText(payload); err != nil {
				c.close()
				return
			}
		case <-keepalive.C:
			if err := c.conn.WriteText([]byte("{}")); err != nil {
				c.close()
				return
			}
		}
	}
}

// run serves the connection until the peer closes it. The only message the
// client may send is the Java server's {"logs":true}; there is no log stream
// here, so it is parsed and ignored.
func (c *socketClient) run() {
	go c.writeLoop()

	for {
		messageType, payload, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		if messageType == websocket.TextMessage {
			var message struct {
				Logs *bool `json:"logs"`
			}
			_ = json.Unmarshal(payload, &message)
		}
	}

	c.close()
	<-c.writerDone
}

// handleSocket serves GET /api/socket. The route is exempt from RequireAuth
// because a browser cannot set headers on a WebSocket; the token travels in the
// query string, which is what the Java server and traccar-web expect.
func (s *Server) handleSocket(w http.ResponseWriter, r *http.Request) {
	if s.socketHub == nil || s.latestPositions == nil {
		writeError(w, http.StatusNotImplemented, errors.New("the socket is not configured"))
		return
	}

	principal, err := s.socketPrincipal(r)
	if err != nil {
		if errors.Is(err, errUnauthorized) {
			writeUnauthorized(w)
			return
		}
		s.logf("api: socket token lookup: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	channels := socketChannelsFor(principal)
	if !channels.any() {
		writeError(w, http.StatusForbidden,
			errors.New("missing a read scope for positions, alerts or devices"))
		return
	}

	conn, err := websocket.Accept(w, r)
	if err != nil {
		if errors.Is(err, websocket.ErrBadHandshake) {
			w.Header().Set("Sec-WebSocket-Version", "13")
			writeError(w, http.StatusBadRequest, err)
			return
		}
		// The connection is already hijacked when writing the handshake failed;
		// the log is the only place left to report it.
		s.logf("api: socket upgrade: %v", err)
		return
	}

	client := s.socketHub.subscribe(conn, principal.DeviceIDs(), channels)

	if channels.positions {
		ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
		positions, err := s.latestPositions.LatestPositions(ctx, principal.DeviceIDs())
		cancel()
		if err != nil {
			s.logf("api: socket latest positions: %v", err)
		} else {
			client.seed(positions)
		}
	}

	client.run()
}

// errUnauthorized is the sentinel the socket token check reports.
var errUnauthorized = errors.New("invalid API key")

// socketPrincipal resolves ?token= the way RequireAuth resolves a header.
func (s *Server) socketPrincipal(r *http.Request) (*auth.Principal, error) {
	if !s.authEnabled() {
		return auth.AdminPrincipal(), nil
	}

	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		return nil, errUnauthorized
	}
	if s.apiKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.apiKey)) == 1 {
		return auth.AdminPrincipal(), nil
	}
	if s.tokens != nil {
		principal, err := s.tokens.LookupToken(r.Context(), auth.HashToken(token))
		if err != nil {
			return nil, fmt.Errorf("lookup token: %w", err)
		}
		if principal != nil {
			return principal, nil
		}
	}
	return nil, errUnauthorized
}
