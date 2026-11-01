// Package api exposes the REST endpoints of the gateway on top of net/http:
// alerts, positions, devices, notification presets and subscriptions, a
// liveness probe, device commands and Prometheus metrics. Authentication is
// either the legacy shared API key (admin) or a database token with scopes and
// device ownership.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

const (
	defaultQueryTimeout = 5 * time.Second
	maxAlertLimit       = 1000
	maxPositionLimit    = 1000
	maxDeviceLimit      = 1000
	maxCommandLimit     = 1000
)

// EventQuerier is the storage contract the alerts handler needs.
type EventQuerier interface {
	QueryEvents(ctx context.Context, filter model.EventFilter) ([]model.Event, error)
}

// CommandRouter encodes a command and delivers it to the device connection.
type CommandRouter interface {
	EncodeCommand(protocol string, command *model.Command) ([]byte, error)
	Send(protocol string, deviceID int64, payload []byte) error
}

// CommandStore records the lifecycle of every command and serves the command
// history. Without it commands are delivered but not recorded.
type CommandStore interface {
	CreateCommand(ctx context.Context, command *model.Command) error
	MarkCommandSent(ctx context.Context, commandID int64) error
	MarkCommandFailed(ctx context.Context, commandID int64) error
	QueryCommands(ctx context.Context, filter model.CommandFilter) ([]model.Command, error)
}

// DeviceProtocols resolves the protocol a device speaks.
type DeviceProtocols interface {
	DeviceProtocol(ctx context.Context, deviceID int64) (protocol string, found bool, err error)
}

// CommandDeliverer hands an encoded command to the gateway, which writes it to
// the device or keeps it queued until the device reconnects. It owns the status
// of the row once the row exists.
type CommandDeliverer interface {
	Deliver(ctx context.Context, protocol string, command *model.Command, payload []byte) (bool, error)
}

// Pinger reports database reachability for the health probe.
type Pinger interface {
	Ping(ctx context.Context) error
}

// PositionQuerier is the storage contract the positions handler needs.
type PositionQuerier interface {
	QueryPositions(ctx context.Context, filter model.PositionFilter) ([]model.Position, error)
}

// GeofenceLookup resolves one geofence for the geofenceId filter and the
// exports. It is separate from ResourceStore so a deployment can serve
// positions without exposing the geofence CRUD.
type GeofenceLookup interface {
	QueryGeofences(ctx context.Context, filter model.ResourceFilter) ([]*geofence.Geofence, error)
}

// DeviceStore is the storage contract the devices handlers need.
type DeviceStore interface {
	Devices(ctx context.Context, filter model.DeviceFilter) ([]model.Device, error)
	Device(ctx context.Context, deviceID int64) (*model.Device, error)
	CreateDevice(ctx context.Context, create model.DeviceCreate) (*model.Device, error)
	UpdateDevice(ctx context.Context, deviceID int64, update model.DeviceUpdate) (*model.Device, error)
	DeleteDevice(ctx context.Context, deviceID int64) error
}

// DeviceOwnerStore is the optional capability that links a newly created
// device to its owner. Without it only admins may create devices, because a
// non-admin would otherwise create a device it cannot see.
type DeviceOwnerStore interface {
	AssignDevice(ctx context.Context, userID, deviceID int64) error
}

// Server holds the handlers and their configuration.
type Server struct {
	alerts        EventQuerier
	positions     PositionQuerier
	devices       DeviceStore
	geofences     GeofenceLookup
	users         UserStore
	notifications NotificationStore
	router        CommandRouter
	commands      CommandStore
	deliverer     CommandDeliverer
	protocols     DeviceProtocols
	pinger        Pinger
	meters        *metrics.Set
	logf          func(format string, args ...any)
	queryTimeout  time.Duration
	now           func() time.Time
	apiKey        string
	tokens        TokenLookup
	// passwordReset delivers reset codes; nil leaves the forgot route at 501.
	passwordReset PasswordResetMailer
	// permissionStore and permissions back the /api/permissions and
	// /api/share routes; shareDisabled refuses the share routes outright.
	permissionStore PermissionStore
	permissions     *permissions.Service
	shareDisabled   bool
	// resources backs the configuration routes (groups, geofences, calendars,
	// drivers, maintenances, orders, reports, computed attributes).
	resources ResourceStore
	// Administrative resources: protocol settings, the server settings row,
	// statistics, the audit trail, reverse geocoding and the optional reboot
	// hook.
	protocolSettings ProtocolSettingsStore
	protocolApplier  ProtocolApplier
	serverSettings   ServerSettingsStore
	statistics       StatisticsStore
	auditStore       AuditStore
	geocoder         Geocoder
	reboot           func()
	notificators     []string
	version          string
	webPath          string
	textEnabled      bool
	// socketHub and latestPositions back GET /api/socket.
	socketHub       *SocketHub
	latestPositions LatestPositionQuerier
	// loginMaxFailures counts consecutive wrong passwords per account; zero
	// disables the lockout.
	loginMaxFailures  int
	loginLockDuration time.Duration
}

// Option customises a Server.
type Option func(*Server)

// WithLogger reports query failures.
func WithLogger(logf func(format string, args ...any)) Option {
	return func(s *Server) {
		if logf != nil {
			s.logf = logf
		}
	}
}

// WithQueryTimeout caps how long one query may take.
func WithQueryTimeout(timeout time.Duration) Option {
	return func(s *Server) {
		if timeout > 0 {
			s.queryTimeout = timeout
		}
	}
}

// WithCommands enables POST /api/commands. Without it the route reports 501.
func WithCommands(router CommandRouter, protocols DeviceProtocols) Option {
	return func(s *Server) {
		s.router = router
		s.protocols = protocols
	}
}

// WithCommandStore records every command and enables GET /api/commands.
// Without it commands are still delivered, but their history is empty and the
// list route reports 501.
func WithCommandStore(commands CommandStore) Option {
	return func(s *Server) { s.commands = commands }
}

// WithCommandDeliverer hands delivery to the dispatcher, which keeps a command
// queued while its device is away and retransmits one that goes unanswered.
// Without it a command is written once and reported failed when the device is
// offline.
func WithCommandDeliverer(deliverer CommandDeliverer) Option {
	return func(s *Server) { s.deliverer = deliverer }
}

// WithPositions enables GET /api/positions. Without it the route reports 501.
func WithPositions(positions PositionQuerier) Option {
	return func(s *Server) { s.positions = positions }
}

// WithGeofenceLookup enables the geofenceId filter of /api/positions and the
// KML / GPX exports. Without it those requests report 501.
func WithGeofenceLookup(lookup GeofenceLookup) Option {
	return func(s *Server) { s.geofences = lookup }
}

// WithDevices enables the device routes. Without it they report 501.
func WithDevices(devices DeviceStore) Option {
	return func(s *Server) { s.devices = devices }
}

// WithUsers enables the account administration routes under /api/users.
// Without it they report 501.
func WithUsers(users UserStore) Option {
	return func(s *Server) { s.users = users }
}

// WithNotifications enables the notification preset routes and the per-user
// subscription routes. Without it they report 501.
func WithNotifications(notifications NotificationStore) Option {
	return func(s *Server) { s.notifications = notifications }
}

// WithHealthPinger makes GET /api/health check the database and answer 503
// while it is unreachable.
func WithHealthPinger(pinger Pinger) Option {
	return func(s *Server) { s.pinger = pinger }
}

// WithAPIKey requires every request except /api/health to present a
// credential. An empty key disables the legacy shared key; database tokens
// (WithTokens) may still authenticate. The key is an admin credential: it
// bypasses scope and device checks.
func WithAPIKey(key string) Option {
	return func(s *Server) { s.apiKey = key }
}

// WithTokens enables per-user bearer tokens. A token carries scopes and the
// devices of its user; see auth.Scope and the users/user_devices/api_tokens
// tables.
func WithTokens(tokens TokenLookup) Option {
	return func(s *Server) { s.tokens = tokens }
}

// WithMetrics enables GET /metrics. Without it the route reports 501.
func WithMetrics(set *metrics.Set) Option {
	return func(s *Server) { s.meters = set }
}

// NewServer creates the API handlers.
func NewServer(alerts EventQuerier, options ...Option) *Server {
	server := &Server{
		alerts:            alerts,
		logf:              func(string, ...any) {},
		queryTimeout:      defaultQueryTimeout,
		now:               time.Now,
		loginMaxFailures:  DefaultLoginMaxFailures,
		loginLockDuration: DefaultLoginLockDuration,
	}
	for _, option := range options {
		option(server)
	}
	return server
}

// Handler returns the route table. Method patterns need Go 1.22, which the
// ServeMux in this module already provides.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// /api/alerts is the previous name of /api/events and stays as an alias.
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/events/{id}", s.handleEvent)
	mux.HandleFunc("GET /api/alerts", s.handleAlerts)
	mux.HandleFunc("GET /api/devices", s.handleDevices)
	mux.HandleFunc("GET /api/devices/{id}", s.handleDevice)
	mux.HandleFunc("POST /api/devices", s.handleCreateDevice)
	mux.HandleFunc("PUT /api/devices/{id}", s.handleUpdateDevice)
	mux.HandleFunc("DELETE /api/devices/{id}", s.handleDeleteDevice)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/password/forgot", s.handleForgotPassword)
	mux.HandleFunc("POST /api/password/reset", s.handleResetPassword)
	mux.HandleFunc("GET /api/positions", s.handlePositions)
	mux.HandleFunc("GET /api/positions/kml", s.handlePositionExport(exportKML))
	mux.HandleFunc("GET /api/positions/kmz", s.handlePositionExport(exportKMZ))
	mux.HandleFunc("GET /api/positions/csv", s.handlePositionExport(exportCSV))
	mux.HandleFunc("GET /api/positions/gpx", s.handlePositionExport(exportGPX))
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/socket", s.handleSocket)
	mux.HandleFunc("GET /api/commands", s.handleCommands)
	mux.HandleFunc("POST /api/commands", s.handleCommand)

	// Account administration. These routes answer 501 when no UserStore is
	// configured and 403 for callers that are not admins.
	mux.HandleFunc("GET /api/users", s.handleUsers)
	mux.HandleFunc("POST /api/users", s.handleCreateUser)
	mux.HandleFunc("PUT /api/users/{id}", s.handleUpdateUser)
	mux.HandleFunc("DELETE /api/users/{id}", s.handleDeleteUser)
	mux.HandleFunc("GET /api/users/{id}/devices", s.handleUserDevices)
	mux.HandleFunc("PUT /api/users/{id}/devices/{deviceId}", s.handleAssignDevice)
	mux.HandleFunc("DELETE /api/users/{id}/devices/{deviceId}", s.handleUnassignDevice)
	mux.HandleFunc("GET /api/users/{id}/tokens", s.handleTokens)
	mux.HandleFunc("POST /api/users/{id}/tokens", s.handleIssueToken)
	mux.HandleFunc("DELETE /api/users/{id}/tokens/{tokenId}", s.handleRevokeToken)
	// A caller changes its own password here; admins set anyone's through
	// PUT /api/users/{id}.
	mux.HandleFunc("PUT /api/users/me/password", s.handleChangePassword)

	// Notification presets are shared definitions; any authenticated caller may
	// list them, only an admin may change them. Subscriptions are managed by
	// the account itself or an admin.
	mux.HandleFunc("GET /api/notifications", s.handleNotifications)
	mux.HandleFunc("POST /api/notifications", s.handleCreateNotification)
	mux.HandleFunc("PUT /api/notifications/{id}", s.handleUpdateNotification)
	mux.HandleFunc("DELETE /api/notifications/{id}", s.handleDeleteNotification)
	mux.HandleFunc("GET /api/users/{id}/notifications", s.handleUserNotifications)
	mux.HandleFunc("PUT /api/users/{id}/notifications/{notificationId}", s.handleAssignNotification)
	mux.HandleFunc("DELETE /api/users/{id}/notifications/{notificationId}", s.handleUnassignNotification)

	// Object permissions. The handler switches on the method so GET, POST and
	// DELETE share one route and one 501 when no store is configured.
	mux.HandleFunc("/api/permissions", s.handlePermissions)
	mux.HandleFunc("POST /api/share/device", s.handleShareDevice)
	mux.HandleFunc("POST /api/share/group", s.handleShareGroup)

	// Configuration resources, the Go counterpart of the Java
	// SimpleObjectResource / ExtendedObjectResource routes.
	s.registerResourceRoutes(mux)

	s.registerAdminRoutes(mux)

	if s.apiKey == "" && s.tokens == nil {
		return mux
	}
	return RequireAuth(s.apiKey, s.tokens, s.logf, mux)
}

// authEnabled reports whether the API is protected at all.
func (s *Server) authEnabled() bool {
	return s.apiKey != "" || s.tokens != nil
}

// authorize checks the scope a handler needs. It writes the response itself
// and returns false when the caller must stop.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, scope auth.Scope) bool {
	if !s.authEnabled() {
		return true
	}
	principal := auth.FromContext(r.Context())
	if principal == nil {
		// The middleware attaches a principal to every authenticated request;
		// this is only reachable if a handler is mounted outside it.
		writeUnauthorized(w)
		return false
	}
	if !principal.Has(scope) {
		writeError(w, http.StatusForbidden, fmt.Errorf("missing scope %q", scope))
		return false
	}
	return true
}

// isAdmin reports whether the caller may see every device. When authentication
// is disabled every request is treated as admin.
func (s *Server) isAdmin(r *http.Request) bool {
	if !s.authEnabled() {
		return true
	}
	principal := auth.FromContext(r.Context())
	return principal != nil && principal.Admin
}

// deviceScope returns the device ids a non-admin may see, or nil when the
// result is unrestricted.
func (s *Server) deviceScope(r *http.Request) []int64 {
	if !s.authEnabled() {
		return nil
	}
	principal := auth.FromContext(r.Context())
	if principal == nil || principal.Admin {
		return nil
	}
	return principal.DeviceIDs()
}

// canAccessDevice reports whether the caller owns the device.
func (s *Server) canAccessDevice(r *http.Request, deviceID int64) bool {
	if !s.authEnabled() {
		return true
	}
	principal := auth.FromContext(r.Context())
	if principal == nil {
		return false
	}
	return principal.CanAccessDevice(deviceID)
}

// forbiddenDevice writes the 403 used when a device exists but is not granted
// to the caller. It deliberately does not distinguish "unknown" from "not
// yours" when the caller has no device at all.
func writeForbiddenDevice(w http.ResponseWriter, deviceID int64) {
	writeError(w, http.StatusForbidden, fmt.Errorf("device %d is not accessible", deviceID))
}

// eventResponse is one row of /api/events.
type eventResponse struct {
	ID         int64          `json:"id"`
	DeviceID   int64          `json:"deviceId"`
	PositionID int64          `json:"positionId,omitempty"`
	Type       string         `json:"type"`
	EventTime  time.Time      `json:"eventTime"`
	GeofenceID int64          `json:"geofenceId,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type eventsResponse struct {
	Events []eventResponse `json:"events"`
	Count  int             `json:"count"`
}

// alertResponse is the same row in the vocabulary /api/alerts has always used.
// The route is now an alias of /api/events, kept because the front end reads it.
type alertResponse struct {
	ID         int64          `json:"id"`
	DeviceID   int64          `json:"deviceId"`
	PositionID int64          `json:"positionId,omitempty"`
	Type       string         `json:"type"`
	AlertTime  time.Time      `json:"alertTime"`
	GeofenceID int64          `json:"geofenceId,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type alertsResponse struct {
	Alerts []alertResponse `json:"alerts"`
	Count  int             `json:"count"`
}

// handleAlerts serves the deprecated route: the same query as /api/events in the
// envelope an alert client expects.
func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	events, ok := s.queryEvents(w, r)
	if !ok {
		return
	}

	response := alertsResponse{Alerts: make([]alertResponse, 0, len(events)), Count: len(events)}
	for _, event := range events {
		response.Alerts = append(response.Alerts, alertResponse{
			ID:         event.ID,
			DeviceID:   event.DeviceID,
			PositionID: event.PositionID,
			Type:       event.Type,
			AlertTime:  event.Time.UTC(),
			GeofenceID: event.GeofenceID,
			Attributes: event.Attributes,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

// handleEvents serves /api/events, which replaced /api/alerts.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	events, ok := s.queryEvents(w, r)
	if !ok {
		return
	}

	response := eventsResponse{Events: make([]eventResponse, 0, len(events)), Count: len(events)}
	for _, event := range events {
		response.Events = append(response.Events, newEventResponse(event))
	}
	writeJSON(w, http.StatusOK, response)
}

// newEventResponse renders one event, the shape both the list and the single
// row use.
func newEventResponse(event model.Event) eventResponse {
	return eventResponse{
		ID:         event.ID,
		DeviceID:   event.DeviceID,
		PositionID: event.PositionID,
		Type:       event.Type,
		EventTime:  event.Time.UTC(),
		GeofenceID: event.GeofenceID,
		Attributes: event.Attributes,
	}
}

// handleEvent serves GET /api/events/{id}. A row the caller cannot reach is a
// 403 for the same reason the device routes answer 403: the id must not tell
// an outsider whether an event exists.
func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	if s.alerts == nil {
		writeError(w, http.StatusNotImplemented, errors.New("events are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeReadAlerts) {
		return
	}
	eventID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	events, err := s.alerts.QueryEvents(ctx, model.EventFilter{ID: eventID, Limit: 1})
	if err != nil {
		s.logf("api: get event %d: %v", eventID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if len(events) == 0 {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown event %d", eventID))
		return
	}
	if !s.canAccessDevice(r, events[0].DeviceID) {
		writeForbiddenDevice(w, events[0].DeviceID)
		return
	}
	writeJSON(w, http.StatusOK, newEventResponse(events[0]))
}

// queryEvents authorises and runs the shared event query. It reports false when
// it has already written the response.
func (s *Server) queryEvents(w http.ResponseWriter, r *http.Request) ([]model.Event, bool) {
	if !s.authorize(w, r, auth.ScopeReadAlerts) {
		return nil, false
	}
	if s.alerts == nil {
		writeError(w, http.StatusNotImplemented, errors.New("events are not configured"))
		return nil, false
	}
	filter, err := alertFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return nil, false
	}
	groupId, err := idQuery(r.URL.Query(), "groupId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return nil, false
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	filter.DeviceIDs = s.deviceScope(r)
	if groupId > 0 {
		devices, err := s.groupDeviceIDs(ctx, r, groupId)
		if err != nil {
			s.writeResourceError(w, err)
			return nil, false
		}
		filter.DeviceIDs = intersectDeviceIDs(filter.DeviceIDs, devices)
	}

	events, err := s.alerts.QueryEvents(ctx, filter)
	if err != nil {
		// The database error stays in the log; the client only learns that the
		// request failed.
		s.logf("api: query events: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return nil, false
	}
	return events, true
}

// groupDeviceIDs resolves the groupId filter: the devices sitting in the group
// or one of its descendants, which is what the Java report expands before it
// queries the events.
func (s *Server) groupDeviceIDs(ctx context.Context, r *http.Request, groupID int64) ([]int64, error) {
	if err := s.checkObjectAccess(ctx, r, permissions.ObjectGroup, groupID); err != nil {
		return nil, err
	}
	if s.permissions == nil {
		return nil, errors.New("permissions are not configured")
	}
	return s.permissions.GroupDeviceIDs(ctx, groupID)
}

// intersectDeviceIDs narrows two device sets. A nil set means "no restriction";
// the result is nil only when both inputs are unrestricted.
func intersectDeviceIDs(current, extra []int64) []int64 {
	if current == nil {
		return append([]int64{}, extra...)
	}
	if extra == nil {
		return current
	}
	wanted := make(map[int64]struct{}, len(extra))
	for _, deviceID := range extra {
		wanted[deviceID] = struct{}{}
	}
	kept := make([]int64, 0, len(current))
	for _, deviceID := range current {
		if _, ok := wanted[deviceID]; ok {
			kept = append(kept, deviceID)
		}
	}
	return kept
}

type positionResponse struct {
	ID         int64          `json:"id"`
	DeviceID   int64          `json:"deviceId"`
	Protocol   string         `json:"protocol"`
	DeviceTime time.Time      `json:"deviceTime"`
	ServerTime time.Time      `json:"serverTime"`
	Valid      bool           `json:"valid"`
	Latitude   float64        `json:"latitude"`
	Longitude  float64        `json:"longitude"`
	Altitude   float64        `json:"altitude"`
	Speed      float64        `json:"speed"`
	Course     float64        `json:"course"`
	Accuracy   float64        `json:"accuracy"`
	Attributes map[string]any `json:"attributes"`
}

type positionsResponse struct {
	Positions []positionResponse `json:"positions"`
	Count     int                `json:"count"`
}

func (s *Server) handlePositions(w http.ResponseWriter, r *http.Request) {
	if s.positions == nil {
		writeError(w, http.StatusNotImplemented, errors.New("positions are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeReadPositions) {
		return
	}

	filter, err := positionFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	geofenceID, err := idQuery(r.URL.Query(), "geofenceId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if geofenceID > 0 && s.geofences == nil {
		writeError(w, http.StatusNotImplemented, errors.New("geofences are not configured"))
		return
	}
	filter.DeviceIDs = s.deviceScope(r)

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	fence, err := s.geofenceFilter(ctx, geofenceID)
	if errors.Is(err, model.ErrResourceNotFound) {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown geofence %d", geofenceID))
		return
	}
	if err != nil {
		s.logf("api: load geofence %d: %v", geofenceID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	positions, err := s.positions.QueryPositions(ctx, filter)
	if err != nil {
		s.logf("api: query positions: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	positions = insideGeofence(positions, fence)

	response := positionsResponse{
		Positions: make([]positionResponse, 0, len(positions)),
		Count:     len(positions),
	}
	for _, position := range positions {
		response.Positions = append(response.Positions, newPositionResponse(position))
	}
	writeJSON(w, http.StatusOK, response)
}

// newPositionResponse renders one position, the shape the list and the socket
// both use.
func newPositionResponse(position model.Position) positionResponse {
	attributes := position.Attributes
	if attributes == nil {
		attributes = map[string]any{}
	}
	return positionResponse{
		ID:         position.ID,
		DeviceID:   position.DeviceID,
		Protocol:   position.Protocol,
		DeviceTime: position.Time.UTC(),
		ServerTime: position.ServerTime.UTC(),
		Valid:      position.Valid,
		Latitude:   position.Latitude,
		Longitude:  position.Longitude,
		Altitude:   position.Altitude,
		Speed:      position.Speed,
		Course:     position.Course,
		Accuracy:   position.Accuracy,
		Attributes: attributes,
	}
}

// geofenceFilter loads the geofence named by geofenceId. A zero id means "no
// filter" and returns a nil fence.
func (s *Server) geofenceFilter(ctx context.Context, geofenceID int64) (*geofence.Geofence, error) {
	if geofenceID <= 0 || s.geofences == nil {
		return nil, nil
	}
	values, err := s.geofences.QueryGeofences(ctx,
		model.ResourceFilter{IDs: []int64{geofenceID}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, model.ErrResourceNotFound
	}
	return values[0], nil
}

// insideGeofence keeps the positions the fence contains. A nil fence keeps
// every position, so the caller does not need a branch.
func insideGeofence(positions []model.Position, fence *geofence.Geofence) []model.Position {
	if fence == nil {
		return positions
	}
	kept := make([]model.Position, 0, len(positions))
	for index := range positions {
		if fence.Contains(&positions[index]) {
			kept = append(kept, positions[index])
		}
	}
	return kept
}

type deviceResponse struct {
	ID         int64             `json:"id"`
	UniqueID   string            `json:"uniqueId"`
	Name       string            `json:"name"`
	Model      string            `json:"model,omitempty"`
	Protocol   string            `json:"protocol"`
	Attributes *model.Attributes `json:"attributes,omitempty"`
	// LastUpdate stays absent until the device has reported a position.
	LastUpdate *time.Time `json:"lastUpdate,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type devicesResponse struct {
	Devices []deviceResponse `json:"devices"`
	Count   int              `json:"count"`
}

// deviceQueryOptions holds the parameters that decide which devices a caller
// may see, as opposed to the ones that filter the rows.
type deviceQueryOptions struct {
	// All is accepted for compatibility with the Java resource. It only widens
	// the answer for an admin there, and an admin already sees every device
	// here, so the value changes nothing.
	All               bool
	UserID            int64
	ExcludeAttributes bool
}

func parseDeviceQueryOptions(r *http.Request) (deviceQueryOptions, error) {
	values := r.URL.Query()
	all, err := boolQuery(values, "all")
	if err != nil {
		return deviceQueryOptions{}, err
	}
	excludeAttributes, err := boolQuery(values, "excludeAttributes")
	if err != nil {
		return deviceQueryOptions{}, err
	}
	userID, err := idQuery(values, "userId")
	if err != nil {
		return deviceQueryOptions{}, err
	}
	return deviceQueryOptions{All: all, UserID: userID, ExcludeAttributes: excludeAttributes}, nil
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	if s.devices == nil {
		writeError(w, http.StatusNotImplemented, errors.New("devices are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeReadDevices) {
		return
	}

	filter, err := deviceFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	options, err := parseDeviceQueryOptions(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	filter.DeviceIDs = s.deviceScope(r)

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	if options.UserID > 0 {
		direct, applicable, err := s.userDeviceGrants(ctx, r, options.UserID)
		if err != nil {
			s.writeResourceError(w, err)
			return
		}
		if applicable {
			filter.DeviceIDs = intersectDeviceIDs(filter.DeviceIDs, direct)
		}
	}

	devices, err := s.devices.Devices(ctx, filter)
	if err != nil {
		s.logf("api: query devices: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	includeAttributes := !options.ExcludeAttributes
	response := devicesResponse{
		Devices: make([]deviceResponse, 0, len(devices)),
		Count:   len(devices),
	}
	for _, device := range devices {
		response.Devices = append(response.Devices, newDeviceResponse(device, includeAttributes))
	}
	writeJSON(w, http.StatusOK, response)
}

// userDeviceGrants returns the devices granted to an account directly. Only an
// admin may ask about somebody else, and a non-admin gets its own direct grants,
// which is what the Java resource's excludeGroups condition answers. The second
// result is false when the caller is not an account at all (the shared API key
// or an unauthenticated request): the parameter then cannot narrow anything and
// is ignored.
func (s *Server) userDeviceGrants(ctx context.Context, r *http.Request, userID int64) ([]int64, bool, error) {
	if !s.authEnabled() {
		return nil, false, nil
	}
	principal := auth.FromContext(r.Context())
	if principal == nil || (principal.Admin && principal.UserID == 0) {
		return nil, false, nil
	}
	if !principal.Admin && principal.UserID != userID {
		return nil, false, fmt.Errorf("%w: user %d", permissions.ErrAccessDenied, userID)
	}
	if s.permissions == nil {
		return nil, false, errors.New("permissions are not configured")
	}
	ids, err := s.permissions.DirectIDs(ctx, userID, permissions.ObjectDevice)
	return ids, true, err
}

// handleDevice serves GET /api/devices/{id}.
func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	if s.devices == nil {
		writeError(w, http.StatusNotImplemented, errors.New("devices are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeReadDevices) {
		return
	}
	deviceID, err := deviceIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.canAccessDevice(r, deviceID) {
		writeForbiddenDevice(w, deviceID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	device, err := s.devices.Device(ctx, deviceID)
	if errors.Is(err, model.ErrDeviceNotFound) {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown device %d", deviceID))
		return
	}
	if err != nil {
		s.logf("api: get device %d: %v", deviceID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, newDeviceResponse(*device, true))
}

// newDeviceResponse renders one device. lastUpdate and model stay absent while
// empty so clients do not have to special case zero values; attributes are
// absent only when the caller asked for excludeAttributes.
func newDeviceResponse(device model.Device, includeAttributes bool) deviceResponse {
	response := deviceResponse{
		ID:         device.ID,
		UniqueID:   device.UniqueID,
		Name:       device.Name,
		Model:      device.Model,
		Protocol:   device.Protocol,
		Attributes: optionalAttributes(device.Attributes, includeAttributes),
		CreatedAt:  device.CreatedAt.UTC(),
	}
	if !device.LastUpdate.IsZero() {
		lastUpdate := device.LastUpdate.UTC()
		response.LastUpdate = &lastUpdate
	}
	return response
}

const maxDeviceBody = 1 << 16

type deviceCreateRequest struct {
	UniqueID string `json:"uniqueId"`
	Name     string `json:"name"`
	Model    string `json:"model"`
	Protocol string `json:"protocol"`
}

// deviceUpdateRequest is a replace of the mutable columns. Name and model fall
// back to empty when omitted, like before; attributes are only replaced when
// the field is present so a rename does not wipe the thresholds the alert
// engine reads from the device.
type deviceUpdateRequest struct {
	Name       string            `json:"name"`
	Model      string            `json:"model"`
	Attributes *model.Attributes `json:"attributes"`
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	if s.devices == nil {
		writeError(w, http.StatusNotImplemented, errors.New("devices are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeWriteDevices) {
		return
	}

	var request deviceCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDeviceBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	request.UniqueID = strings.TrimSpace(request.UniqueID)
	if request.UniqueID == "" {
		writeError(w, http.StatusBadRequest, errors.New("uniqueId is required"))
		return
	}

	ownerStore, ownsDevices := s.devices.(DeviceOwnerStore)
	assignOwner := ownsDevices && !s.isAdmin(r)
	if !s.isAdmin(r) && !ownsDevices {
		writeError(w, http.StatusForbidden,
			errors.New("only an admin token may create devices without ownership support"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	device, err := s.devices.CreateDevice(ctx, model.DeviceCreate{
		UniqueID: request.UniqueID,
		Name:     strings.TrimSpace(request.Name),
		Model:    strings.TrimSpace(request.Model),
		Protocol: strings.TrimSpace(request.Protocol),
	})
	if errors.Is(err, model.ErrDuplicateDevice) {
		writeError(w, http.StatusConflict, fmt.Errorf("device %q already exists", request.UniqueID))
		return
	}
	if err != nil {
		s.logf("api: create device %q: %v", request.UniqueID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	if assignOwner {
		principal := auth.FromContext(r.Context())
		if err := ownerStore.AssignDevice(ctx, principal.UserID, device.ID); err != nil {
			s.logf("api: assign device %d to user %d: %v", device.ID, principal.UserID, err)
			// Without the ownership row the creator could not manage the
			// device, so undo the create instead of leaving it orphaned.
			if cleanupErr := s.devices.DeleteDevice(ctx, device.ID); cleanupErr != nil {
				s.logf("api: rollback device %d: %v", device.ID, cleanupErr)
			}
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
	}

	s.audit(r, model.ActionCreate, "device", device.ID)
	writeJSON(w, http.StatusCreated, newDeviceResponse(*device, true))
}

func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	if s.devices == nil {
		writeError(w, http.StatusNotImplemented, errors.New("devices are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeWriteDevices) {
		return
	}

	deviceID, err := deviceIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.canAccessDevice(r, deviceID) {
		writeForbiddenDevice(w, deviceID)
		return
	}

	var request deviceUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDeviceBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	device, err := s.devices.UpdateDevice(ctx, deviceID, model.DeviceUpdate{
		Name:       strings.TrimSpace(request.Name),
		Model:      strings.TrimSpace(request.Model),
		Attributes: request.Attributes,
	})
	if errors.Is(err, model.ErrDeviceNotFound) {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown device %d", deviceID))
		return
	}
	if err != nil {
		s.logf("api: update device %d: %v", deviceID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	s.audit(r, model.ActionEdit, "device", device.ID)
	writeJSON(w, http.StatusOK, newDeviceResponse(*device, true))
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	if s.devices == nil {
		writeError(w, http.StatusNotImplemented, errors.New("devices are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeWriteDevices) {
		return
	}

	deviceID, err := deviceIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.canAccessDevice(r, deviceID) {
		writeForbiddenDevice(w, deviceID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	if err := s.devices.DeleteDevice(ctx, deviceID); err != nil {
		if errors.Is(err, model.ErrDeviceNotFound) {
			writeError(w, http.StatusNotFound, fmt.Errorf("unknown device %d", deviceID))
			return
		}
		s.logf("api: delete device %d: %v", deviceID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	s.audit(r, model.ActionRemove, "device", deviceID)

	w.WriteHeader(http.StatusNoContent)
}

// deviceIDFromPath reads the {id} path segment.
func deviceIDFromPath(r *http.Request) (int64, error) {
	deviceID, err := positiveID(r, "id")
	if err != nil {
		return 0, fmt.Errorf("invalid device id %q", r.PathValue("id"))
	}
	return deviceID, nil
}

// handleMetrics serves the Prometheus text format. It is subject to the API key
// like every other route except /api/health.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.meters == nil {
		writeError(w, http.StatusNotImplemented, errors.New("metrics are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeReadMetrics) {
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.meters.Render(w)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	response := map[string]any{
		"status": "ok",
		"time":   s.now().UTC().Format(time.RFC3339),
	}
	if s.pinger != nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := s.pinger.Ping(ctx); err != nil {
			// The probe stays unauthenticated, so the driver error only goes
			// to the log.
			s.logf("api: health: database: %v", err)
			response["status"] = "unavailable"
			response["database"] = "unavailable"
			writeJSON(w, http.StatusServiceUnavailable, response)
			return
		}
		response["database"] = "ok"
	}
	writeJSON(w, http.StatusOK, response)
}

const maxCommandBody = 1 << 16

type commandRequest struct {
	DeviceID   int64          `json:"deviceId"`
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes"`
}

type commandResponse struct {
	ID       int64  `json:"id,omitempty"`
	DeviceID int64  `json:"deviceId"`
	Type     string `json:"type"`
	Protocol string `json:"protocol"`
	Queued   bool   `json:"queued"`
}

// handleCommand encodes a device command, records it and writes it to the
// device connection. Status codes are stable: 404 unknown device, 409 offline
// or unknown protocol, 400 unsupported command, 501 commands not configured.
func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if s.router == nil || s.protocols == nil {
		writeError(w, http.StatusNotImplemented, errors.New("commands are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeWriteCommands) {
		return
	}

	// Extra fields are tolerated on purpose: Traccar clients send more than
	// deviceId/type/attributes.
	var request commandRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}
	if request.DeviceID <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("deviceId must be positive"))
		return
	}
	if request.Type == "" {
		writeError(w, http.StatusBadRequest, errors.New("type is required"))
		return
	}
	if !s.canAccessDevice(r, request.DeviceID) {
		writeForbiddenDevice(w, request.DeviceID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	protocol, found, err := s.protocols.DeviceProtocol(ctx, request.DeviceID)
	if err != nil {
		s.logf("api: device protocol %d: %v", request.DeviceID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown device %d", request.DeviceID))
		return
	}
	if protocol == "" {
		writeError(w, http.StatusConflict,
			errors.New("device protocol unknown; reconnect the device once"))
		return
	}

	command := model.NewCommand(request.DeviceID, request.Type)
	for key, value := range request.Attributes {
		command.SetAttribute(key, value)
	}

	payload, err := s.router.EncodeCommand(protocol, command)
	if err != nil {
		if errors.Is(err, gateway.ErrUnknownProtocol) {
			s.logf("api: command %s for device %d: %v", request.Type, request.DeviceID, err)
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// The row exists before the payload does, so a crash between the two
	// leaves a pending command that was never sent rather than an untraceable
	// send. Nothing is written when the row cannot be created: a caller that
	// gets 202 can then always find the command afterwards.
	if s.commands != nil {
		if err := s.commands.CreateCommand(ctx, command); err != nil {
			s.logf("api: record command %s for device %d: %v", request.Type, request.DeviceID, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
	}

	delivered := true
	if s.deliverer != nil {
		// The dispatcher owns every status change from here: it writes the
		// payload, retransmits it and records how the command ended.
		delivered, err = s.deliverer.Deliver(ctx, protocol, command, payload)
	} else if sendErr := s.router.Send(protocol, request.DeviceID, payload); sendErr != nil {
		if s.commands != nil {
			s.markCommand(ctx, command.ID, false)
		}
		err = sendErr
	}

	if err != nil {
		switch {
		case errors.Is(err, gateway.ErrDeviceOffline):
			writeError(w, http.StatusConflict, fmt.Errorf("device %d is offline", request.DeviceID))
		case errors.Is(err, gateway.ErrCommandQueueFull):
			writeError(w, http.StatusTooManyRequests,
				fmt.Errorf("device %d holds the maximum number of queued commands", request.DeviceID))
		case errors.Is(err, gateway.ErrUnknownProtocol):
			s.logf("api: command %s for device %d: %v", request.Type, request.DeviceID, err)
			writeError(w, http.StatusConflict, err)
		default:
			s.logf("api: command %s for device %d: %v", request.Type, request.DeviceID, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		}
		return
	}

	if s.deliverer == nil && s.commands != nil {
		s.markCommand(ctx, command.ID, true)
	}

	writeJSON(w, http.StatusAccepted, commandResponse{
		ID:       command.ID,
		DeviceID: request.DeviceID,
		Type:     request.Type,
		Protocol: protocol,
		Queued:   !delivered,
	})
}

// markCommand closes the send half of a command record. A failure here cannot
// change the answer to the caller: the payload either reached the device or it
// did not, and the row keeps its previous status.
func (s *Server) markCommand(ctx context.Context, commandID int64, sent bool) {
	var err error
	if sent {
		err = s.commands.MarkCommandSent(ctx, commandID)
	} else {
		err = s.commands.MarkCommandFailed(ctx, commandID)
	}
	if err != nil {
		s.logf("api: finish command %d (sent=%v): %v", commandID, sent, err)
	}
}

type commandView struct {
	ID         int64          `json:"id"`
	DeviceID   int64          `json:"deviceId"`
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes"`
	Status     string         `json:"status"`
	ResultCode *int           `json:"resultCode,omitempty"`
	Serial     int            `json:"serial,omitempty"`
	Attempts   int            `json:"attempts"`
	CreatedAt  time.Time      `json:"createdAt"`
	SentAt     *time.Time     `json:"sentAt,omitempty"`
	DoneAt     *time.Time     `json:"doneAt,omitempty"`
}

type commandsResponse struct {
	Commands []commandView `json:"commands"`
	Count    int           `json:"count"`
}

// handleCommands lists the commands of the devices the caller may see.
func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	if s.commands == nil {
		writeError(w, http.StatusNotImplemented, errors.New("commands are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeReadCommands) {
		return
	}

	filter, err := commandFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	filter.DeviceIDs = s.deviceScope(r)

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	commands, err := s.commands.QueryCommands(ctx, filter)
	if err != nil {
		s.logf("api: query commands: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	response := commandsResponse{
		Commands: make([]commandView, 0, len(commands)),
		Count:    len(commands),
	}
	for _, command := range commands {
		attributes := command.Attributes
		if attributes == nil {
			attributes = map[string]any{}
		}
		view := commandView{
			ID:         command.ID,
			DeviceID:   command.DeviceID,
			Type:       command.Type,
			Attributes: attributes,
			Status:     command.Status,
			ResultCode: command.ResultCode,
			Serial:     command.Serial,
			Attempts:   command.Attempts,
			CreatedAt:  command.CreatedAt.UTC(),
		}
		if !command.SentAt.IsZero() {
			sentAt := command.SentAt.UTC()
			view.SentAt = &sentAt
		}
		if !command.DoneAt.IsZero() {
			doneAt := command.DoneAt.UTC()
			view.DoneAt = &doneAt
		}
		response.Commands = append(response.Commands, view)
	}
	writeJSON(w, http.StatusOK, response)
}

// commandFilter parses and validates the query string:
//
//	deviceId=7&limit=50&offset=0
func commandFilter(r *http.Request) (model.CommandFilter, error) {
	query := r.URL.Query()
	var filter model.CommandFilter

	deviceID, err := parseDeviceID(query)
	if err != nil {
		return filter, err
	}
	filter.DeviceID = deviceID

	if filter.Limit, err = parseLimit(query, maxCommandLimit); err != nil {
		return filter, err
	}
	if filter.Offset, err = parseOffset(query); err != nil {
		return filter, err
	}

	return filter, nil
}

// alertFilter parses and validates the query string:
//
//	deviceId=7&type=deviceOverspeed,geofenceEnter
//	from=2026-10-31T00:00:00Z&to=1780000000000&limit=50&offset=0
func alertFilter(r *http.Request) (model.EventFilter, error) {
	query := r.URL.Query()
	var filter model.EventFilter

	deviceID, err := parseDeviceID(query)
	if err != nil {
		return filter, err
	}
	filter.DeviceID = deviceID

	for _, raw := range query["type"] {
		for _, alertType := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(alertType); trimmed != "" {
				filter.Types = append(filter.Types, trimmed)
			}
		}
	}

	if filter.From, filter.To, err = parseTimeRange(query); err != nil {
		return filter, err
	}
	if filter.Limit, err = parseLimit(query, maxAlertLimit); err != nil {
		return filter, err
	}
	if filter.Offset, err = parseOffset(query); err != nil {
		return filter, err
	}

	return filter, nil
}

// positionFilter parses and validates the query string:
//
//	deviceId=7&from=2026-10-31T00:00:00Z&to=1780000000000&limit=50&offset=0
func positionFilter(r *http.Request) (model.PositionFilter, error) {
	query := r.URL.Query()
	var filter model.PositionFilter

	deviceID, err := parseDeviceID(query)
	if err != nil {
		return filter, err
	}
	filter.DeviceID = deviceID

	if filter.From, filter.To, err = parseTimeRange(query); err != nil {
		return filter, err
	}
	if filter.Limit, err = parseLimit(query, maxPositionLimit); err != nil {
		return filter, err
	}
	if filter.Offset, err = parseOffset(query); err != nil {
		return filter, err
	}

	return filter, nil
}

// deviceFilter parses and validates the query string:
//
//	protocol=gt06&keyword=van&uniqueId=A&uniqueId=B&id=7&limit=50&offset=0
func deviceFilter(r *http.Request) (model.DeviceFilter, error) {
	query := r.URL.Query()
	var filter model.DeviceFilter

	filter.Protocol = strings.TrimSpace(query.Get("protocol"))
	filter.Keyword = strings.TrimSpace(query.Get("keyword"))

	for _, raw := range query["uniqueId"] {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			filter.UniqueIDs = append(filter.UniqueIDs, trimmed)
		}
	}
	for _, raw := range query["id"] {
		if raw == "" {
			continue
		}
		deviceID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || deviceID <= 0 {
			return filter, fmt.Errorf("invalid id %q", raw)
		}
		filter.IDs = append(filter.IDs, deviceID)
	}

	var err error
	if filter.Limit, err = parseLimit(query, maxDeviceLimit); err != nil {
		return filter, err
	}
	if filter.Offset, err = parseOffset(query); err != nil {
		return filter, err
	}

	return filter, nil
}

// parseDeviceID accepts an optional positive device id.
func parseDeviceID(query url.Values) (int64, error) {
	raw := query.Get("deviceId")
	if raw == "" {
		return 0, nil
	}
	deviceID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || deviceID <= 0 {
		return 0, fmt.Errorf("invalid deviceId %q", raw)
	}
	return deviceID, nil
}

// parseTimeRange accepts RFC 3339 and Unix milliseconds and rejects an
// inverted range.
func parseTimeRange(query url.Values) (time.Time, time.Time, error) {
	from, err := parseTime(query.Get("from"))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid from: %w", err)
	}
	to, err := parseTime(query.Get("to"))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid to: %w", err)
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return time.Time{}, time.Time{}, errors.New("from must not be after to")
	}
	return from, to, nil
}

// parseLimit returns the requested limit, or zero when the parameter is absent.
func parseLimit(query url.Values, max int) (int, error) {
	raw := query.Get("limit")
	if raw == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 || limit > max {
		return 0, fmt.Errorf("limit must be between 1 and %d", max)
	}
	return limit, nil
}

// parseOffset returns the requested offset, or zero when the parameter is absent.
func parseOffset(query url.Values) (int, error) {
	raw := query.Get("offset")
	if raw == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(raw)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid offset %q", raw)
	}
	return offset, nil
}

// parseTime accepts RFC 3339 and Unix milliseconds, which is what mobile
// clients tend to send. An empty value means "no restriction".
func parseTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if millis, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.UnixMilli(millis).UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is neither RFC3339 nor epoch milliseconds", raw)
	}
	return parsed.UTC(), nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
