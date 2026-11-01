package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

const (
	maxServerBody    = 1 << 20
	maxUploadBody    = 64 << 20
	maxAuditLimit    = 1000
	maxStatistics24H = 24 * time.Hour
)

// ProtocolSettingsStore is the persistence the protocol routes need.
type ProtocolSettingsStore interface {
	ProtocolSettings(ctx context.Context) ([]model.ProtocolSettings, error)
	UpdateProtocolSettings(ctx context.Context, id int64, port int, transport string) (*model.ProtocolSettings, error)
}

// ProtocolApplier rebinds a listener after an administrator changed its port or
// transport. The gateway implements it; a nil applier stores the change and
// leaves the running listeners untouched until the next start.
type ProtocolApplier interface {
	ApplyProtocolSettings(ctx context.Context, settings model.ProtocolSettings) error
}

// ServerSettingsStore is the persistence the /api/server routes need.
type ServerSettingsStore interface {
	ServerSettings(ctx context.Context) (*model.Server, error)
	UpdateServerSettings(ctx context.Context, settings *model.Server) (*model.Server, error)
}

// StatisticsStore serves the statistics report and the per-device position
// counts. *postgres.Store implements it.
type StatisticsStore interface {
	QueryStatistics(ctx context.Context, from, to time.Time) ([]*model.Statistics, error)
	PositionCounts(ctx context.Context, deviceIDs []int64, from, to time.Time) (map[int64]int64, error)
}

// AuditStore is the audit trail.
type AuditStore interface {
	RecordAction(ctx context.Context, action *model.Action) error
	QueryActions(ctx context.Context, from, to time.Time, limit, offset int) ([]model.Action, error)
}

// Geocoder resolves a coordinate to an address for GET /api/server/geocode. It
// is wired by the geographic services of M6-3.
type Geocoder interface {
	Address(ctx context.Context, latitude, longitude float64) (string, error)
}

// WithProtocolSettings enables the protocol routes. applier may be nil.
func WithProtocolSettings(store ProtocolSettingsStore, applier ProtocolApplier) Option {
	return func(s *Server) {
		s.protocolSettings = store
		s.protocolApplier = applier
	}
}

// WithServerSettings enables GET/PUT /api/server.
func WithServerSettings(store ServerSettingsStore) Option {
	return func(s *Server) { s.serverSettings = store }
}

// WithStatistics enables GET /api/statistics and /api/statistics/devices.
func WithStatistics(store StatisticsStore) Option {
	return func(s *Server) { s.statistics = store }
}

// WithAudit records the administrative actions and enables GET /api/audit.
func WithAudit(store AuditStore) Option {
	return func(s *Server) { s.auditStore = store }
}

// WithGeocoder enables GET /api/server/geocode.
func WithGeocoder(geocoder Geocoder) Option {
	return func(s *Server) { s.geocoder = geocoder }
}

// WithReboot installs the callback POST /api/server/reboot calls. Without it the
// route reports 501.
func WithReboot(reboot func()) Option {
	return func(s *Server) { s.reboot = reboot }
}

// WithNotificators lists the notification channels the deployment can deliver
// on. It backs /api/notificators and /api/notifications/notificators.
func WithNotificators(names []string) Option {
	return func(s *Server) { s.notificators = append([]string(nil), names...) }
}

// WithVersion sets the version string /api/server reports.
func WithVersion(version string) Option {
	return func(s *Server) { s.version = version }
}

// WithWebPath enables POST /api/server/file/{path}, which writes an uploaded
// file below this directory (the web root the Java server serves).
func WithWebPath(path string) Option {
	return func(s *Server) { s.webPath = path }
}

// WithTextEnabled tells /api/server whether an SMS channel is configured.
func WithTextEnabled(enabled bool) Option {
	return func(s *Server) { s.textEnabled = enabled }
}

// audit records one action unless no audit store is configured. The write uses// its own context so a client that disconnects does not lose the row.
func (s *Server) audit(r *http.Request, actionType, objectType string, objectID int64) {
	s.auditWithAttributes(r, actionType, objectType, objectID, nil)
}

func (s *Server) auditWithAttributes(
	r *http.Request, actionType, objectType string, objectID int64, attributes model.Attributes,
) {
	if s.auditStore == nil || actionType == "" {
		return
	}
	var userID int64
	if principal := auth.FromContext(r.Context()); principal != nil {
		userID = principal.UserID
	}
	action := &model.Action{
		ActionTime: s.now(),
		Address:    requestAddress(r),
		UserID:     userID,
		ActionType: actionType,
		ObjectType: objectType,
		ObjectID:   objectID,
		Attributes: attributes,
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.queryTimeout)
	defer cancel()
	if err := s.auditStore.RecordAction(ctx, action); err != nil {
		s.logf("api: record %s action: %v", actionType, err)
	}
}

// requestAddress strips the ephemeral port, which is what the Java server
// stores in an action row.
func requestAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type protocolResponse struct {
	ID        int64  `json:"id"`
	Protocol  string `json:"protocol"`
	Port      int    `json:"port"`
	Transport string `json:"transport"`
}

func newProtocolResponse(settings model.ProtocolSettings) protocolResponse {
	return protocolResponse{
		ID:        settings.ID,
		Protocol:  settings.Protocol,
		Port:      settings.Port,
		Transport: settings.Transport,
	}
}

func (s *Server) handleProtocols(w http.ResponseWriter, r *http.Request) {
	if s.protocolSettings == nil {
		writeError(w, http.StatusNotImplemented, errors.New("protocol settings are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeReadDevices) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	settings, err := s.protocolSettings.ProtocolSettings(ctx)
	if err != nil {
		s.logf("api: query protocol settings: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	response := make([]protocolResponse, 0, len(settings))
	for _, row := range settings {
		response = append(response, newProtocolResponse(row))
	}
	writeJSON(w, http.StatusOK, response)
}

type protocolUpdateRequest struct {
	Port      *int    `json:"port"`
	Transport *string `json:"transport"`
}

func (s *Server) handleUpdateProtocol(w http.ResponseWriter, r *http.Request) {
	if s.protocolSettings == nil {
		writeError(w, http.StatusNotImplemented, errors.New("protocol settings are not configured"))
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	var request protocolUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxServerBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	current, err := s.protocolSettings.ProtocolSettings(ctx)
	if err != nil {
		s.logf("api: query protocol settings: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	var found *model.ProtocolSettings
	for index := range current {
		if current[index].ID == id {
			found = &current[index]
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown protocol settings %d", id))
		return
	}

	port := found.Port
	if request.Port != nil {
		port = *request.Port
	}
	if port < 0 || port > 65535 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("port %d is out of range", port))
		return
	}
	transport := found.Transport
	if request.Transport != nil {
		transport = strings.ToLower(strings.TrimSpace(*request.Transport))
	}
	if !model.ValidTransport(transport) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown transport %q", transport))
		return
	}

	updated, err := s.protocolSettings.UpdateProtocolSettings(ctx, id, port, transport)
	if errors.Is(err, model.ErrProtocolNotFound) {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown protocol settings %d", id))
		return
	}
	if err != nil {
		s.logf("api: update protocol settings %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	if s.protocolApplier != nil {
		if err := s.protocolApplier.ApplyProtocolSettings(ctx, *updated); err != nil {
			s.logf("api: apply protocol settings %s: %v", updated.Protocol, err)
			writeError(w, http.StatusConflict,
				fmt.Errorf("stored the settings but could not rebind %s: %w", updated.Protocol, err))
			return
		}
	}

	s.audit(r, model.ActionEdit, "protocol", updated.ID)
	writeJSON(w, http.StatusOK, newProtocolResponse(*updated))
}

type serverResponse struct {
	ID               int64          `json:"id"`
	Registration     bool           `json:"registration"`
	Readonly         bool           `json:"readonly"`
	DeviceReadonly   bool           `json:"deviceReadonly"`
	Map              string         `json:"map"`
	BingKey          string         `json:"bingKey"`
	MapURL           string         `json:"mapUrl"`
	OverlayURL       string         `json:"overlayUrl"`
	Latitude         float64        `json:"latitude"`
	Longitude        float64        `json:"longitude"`
	Zoom             int            `json:"zoom"`
	ForceSettings    bool           `json:"forceSettings"`
	CoordinateFormat string         `json:"coordinateFormat"`
	LimitCommands    bool           `json:"limitCommands"`
	DisableReports   bool           `json:"disableReports"`
	FixedEmail       bool           `json:"fixedEmail"`
	PoiLayer         string         `json:"poiLayer"`
	Announcement     string         `json:"announcement"`
	EmailEnabled     bool           `json:"emailEnabled"`
	GeocoderEnabled  bool           `json:"geocoderEnabled"`
	TextEnabled      bool           `json:"textEnabled"`
	NewServer        bool           `json:"newServer"`
	OpenIDEnabled    bool           `json:"openIdEnabled"`
	OpenIDForce      bool           `json:"openIdForce"`
	Version          string         `json:"version"`
	Attributes       map[string]any `json:"attributes"`
}

func (s *Server) serverResponse(settings *model.Server, newServer bool) serverResponse {
	attributes := map[string]any(settings.Attributes)
	if attributes == nil {
		attributes = map[string]any{}
	}
	return serverResponse{
		ID:               settings.ID,
		Registration:     settings.Registration,
		Readonly:         settings.Readonly,
		DeviceReadonly:   settings.DeviceReadonly,
		Map:              settings.Map,
		BingKey:          settings.BingKey,
		MapURL:           settings.MapURL,
		OverlayURL:       settings.OverlayURL,
		Latitude:         settings.Latitude,
		Longitude:        settings.Longitude,
		Zoom:             settings.Zoom,
		ForceSettings:    settings.ForceSettings,
		CoordinateFormat: settings.CoordinateFormat,
		LimitCommands:    settings.LimitCommands,
		DisableReports:   settings.DisableReports,
		FixedEmail:       settings.FixedEmail,
		PoiLayer:         settings.PoiLayer,
		Announcement:     settings.Announcement,
		EmailEnabled:     s.passwordReset != nil,
		GeocoderEnabled:  s.geocoder != nil,
		TextEnabled:      s.textEnabled,
		NewServer:        newServer,
		Version:          s.version,
		Attributes:       attributes,
	}
}

// handleServer answers the settings query the login page needs, so the route is
// exempt from authentication. It still hides nothing: the Java server serves the
// same row to an anonymous caller.
func (s *Server) handleServer(w http.ResponseWriter, r *http.Request) {
	if s.serverSettings == nil {
		writeError(w, http.StatusNotImplemented, errors.New("server settings are not configured"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	settings, err := s.serverSettings.ServerSettings(ctx)
	if err != nil {
		s.logf("api: query server settings: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, s.serverResponse(settings, s.newServer(ctx)))
}

// newServer reports an instance without any account, which makes the web
// interface show the registration form instead of the login form.
func (s *Server) newServer(ctx context.Context) bool {
	if s.users == nil {
		return false
	}
	users, err := s.users.Users(ctx, model.UserFilter{Limit: 1})
	if err != nil {
		s.logf("api: check for accounts: %v", err)
		return false
	}
	return len(users) == 0
}

type serverUpdateRequest struct {
	Registration     *bool           `json:"registration"`
	Readonly         *bool           `json:"readonly"`
	DeviceReadonly   *bool           `json:"deviceReadonly"`
	Map              *string         `json:"map"`
	BingKey          *string         `json:"bingKey"`
	MapURL           *string         `json:"mapUrl"`
	OverlayURL       *string         `json:"overlayUrl"`
	Latitude         *float64        `json:"latitude"`
	Longitude        *float64        `json:"longitude"`
	Zoom             *int            `json:"zoom"`
	ForceSettings    *bool           `json:"forceSettings"`
	CoordinateFormat *string         `json:"coordinateFormat"`
	LimitCommands    *bool           `json:"limitCommands"`
	DisableReports   *bool           `json:"disableReports"`
	FixedEmail       *bool           `json:"fixedEmail"`
	PoiLayer         *string         `json:"poiLayer"`
	Announcement     *string         `json:"announcement"`
	Attributes       *map[string]any `json:"attributes"`
}

func (s *Server) handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	if s.serverSettings == nil {
		writeError(w, http.StatusNotImplemented, errors.New("server settings are not configured"))
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}

	var request serverUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxServerBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	settings, err := s.serverSettings.ServerSettings(ctx)
	if err != nil {
		s.logf("api: query server settings: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	applyBool(&settings.Registration, request.Registration)
	applyBool(&settings.Readonly, request.Readonly)
	applyBool(&settings.DeviceReadonly, request.DeviceReadonly)
	applyBool(&settings.ForceSettings, request.ForceSettings)
	applyBool(&settings.LimitCommands, request.LimitCommands)
	applyBool(&settings.DisableReports, request.DisableReports)
	applyBool(&settings.FixedEmail, request.FixedEmail)
	applyString(&settings.Map, request.Map)
	applyString(&settings.BingKey, request.BingKey)
	applyString(&settings.MapURL, request.MapURL)
	applyString(&settings.OverlayURL, request.OverlayURL)
	applyString(&settings.CoordinateFormat, request.CoordinateFormat)
	applyString(&settings.PoiLayer, request.PoiLayer)
	applyString(&settings.Announcement, request.Announcement)
	applyFloat(&settings.Latitude, request.Latitude)
	applyFloat(&settings.Longitude, request.Longitude)
	applyInt(&settings.Zoom, request.Zoom)
	if request.Attributes != nil {
		settings.Attributes = model.Attributes(*request.Attributes)
	}

	updated, err := s.serverSettings.UpdateServerSettings(ctx, settings)
	if err != nil {
		s.logf("api: update server settings: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	s.audit(r, model.ActionEdit, "server", updated.ID)
	writeJSON(w, http.StatusOK, s.serverResponse(updated, s.newServer(ctx)))
}

func applyBool(target *bool, value *bool) {
	if value != nil {
		*target = *value
	}
}

func applyString(target *string, value *string) {
	if value != nil {
		*target = *value
	}
}

func applyFloat(target *float64, value *float64) {
	if value != nil {
		*target = *value
	}
}

func applyInt(target *int, value *int) {
	if value != nil {
		*target = *value
	}
}

// handleServerTimezones returns the zone names an account may pick. It reads
// the system tz database when it is there and falls back to a short embedded
// list, because Go has no API to enumerate the zones.
func (s *Server) handleServerTimezones(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeReadDevices) {
		return
	}
	writeJSON(w, http.StatusOK, availableTimezones())
}

var fallbackTimezones = []string{
	"UTC", "Africa/Cairo", "Africa/Johannesburg", "Africa/Lagos", "Africa/Nairobi",
	"America/Anchorage", "America/Argentina/Buenos_Aires", "America/Bogota",
	"America/Chicago", "America/Denver", "America/Los_Angeles", "America/Mexico_City",
	"America/New_York", "America/Phoenix", "America/Sao_Paulo", "America/Toronto",
	"Asia/Bangkok", "Asia/Dubai", "Asia/Hong_Kong", "Asia/Jakarta", "Asia/Jerusalem",
	"Asia/Kolkata", "Asia/Kuala_Lumpur", "Asia/Manila", "Asia/Riyadh", "Asia/Seoul",
	"Asia/Shanghai", "Asia/Singapore", "Asia/Taipei", "Asia/Tehran", "Asia/Tokyo",
	"Australia/Adelaide", "Australia/Brisbane", "Australia/Melbourne", "Australia/Perth",
	"Australia/Sydney", "Europe/Amsterdam", "Europe/Athens", "Europe/Berlin",
	"Europe/Brussels", "Europe/Bucharest", "Europe/Budapest", "Europe/Copenhagen",
	"Europe/Dublin", "Europe/Helsinki", "Europe/Istanbul", "Europe/Lisbon",
	"Europe/London", "Europe/Madrid", "Europe/Moscow", "Europe/Oslo", "Europe/Paris",
	"Europe/Prague", "Europe/Rome", "Europe/Stockholm", "Europe/Vienna",
	"Europe/Warsaw", "Europe/Zurich", "Pacific/Auckland", "Pacific/Honolulu",
}

func availableTimezones() []string {
	for _, path := range []string{
		"/usr/share/zoneinfo/zone1970.tab",
		"/usr/share/zoneinfo/zone.tab",
	} {
		names := readTimezoneTable(path)
		if len(names) > 0 {
			return names
		}
	}
	names := append([]string(nil), fallbackTimezones...)
	sort.Strings(names)
	return names
}

func readTimezoneTable(path string) []string {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var names []string
	for _, line := range strings.Split(string(content), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		name := strings.TrimSpace(fields[2])
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// handleServerGeocode resolves one coordinate when a geocoder is configured.
func (s *Server) handleServerGeocode(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeReadPositions) {
		return
	}
	if s.geocoder == nil {
		writeError(w, http.StatusNotImplemented, errors.New("reverse geocoding is not enabled"))
		return
	}
	query := r.URL.Query()
	latitude, err := floatQuery(query, "latitude")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	longitude, err := floatQuery(query, "longitude")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	address, err := s.geocoder.Address(ctx, latitude, longitude)
	if err != nil {
		s.logf("api: geocode %f,%f: %v", latitude, longitude, err)
		writeError(w, http.StatusBadGateway, errors.New("geocoder is unavailable"))
		return
	}
	writeJSON(w, http.StatusOK, address)
}

func floatQuery(values url.Values, name string) (float64, error) {
	raw := values.Get(name)
	if raw == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q", name, raw)
	}
	return value, nil
}

// handleServerFile stores an uploaded web asset below the configured web root.
// The path is resolved against the root and refused when it would escape it.
func (s *Server) handleServerFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.webPath == "" {
		writeError(w, http.StatusNotImplemented, errors.New("web root is not configured"))
		return
	}
	name := r.PathValue("path")
	if name == "" {
		writeError(w, http.StatusBadRequest, errors.New("a file path is required"))
		return
	}

	root, err := filepath.Abs(s.webPath)
	if err != nil {
		s.logf("api: resolve web root: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil || !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		writeError(w, http.StatusBadRequest, errors.New("the path escapes the web root"))
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		s.logf("api: create upload directory: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	file, err := os.Create(target)
	if err != nil {
		s.logf("api: create upload %s: %v", target, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	defer file.Close()
	if _, err := io.Copy(file, http.MaxBytesReader(w, r.Body, maxUploadBody)); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("read upload: %w", err))
		return
	}
	s.audit(r, model.ActionCreate, "file", 0)
	w.WriteHeader(http.StatusOK)
}

// handleServerGC triggers a garbage collection, the Go counterpart of the
// Java server's /api/server/gc.
func (s *Server) handleServerGC(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	runtime.GC()
	w.WriteHeader(http.StatusOK)
}

// handleServerReboot asks the process to shut down. The callback is installed by
// the command so tests can observe the request without exiting.
func (s *Server) handleServerReboot(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.reboot == nil {
		writeError(w, http.StatusNotImplemented, errors.New("reboot is not configured"))
		return
	}
	s.audit(r, model.ActionCommand, "server", 0)
	w.WriteHeader(http.StatusOK)
	s.reboot()
}

type statisticsResponse struct {
	ID                  int64          `json:"id"`
	CaptureTime         time.Time      `json:"captureTime"`
	ActiveUsers         int            `json:"activeUsers"`
	ActiveDevices       int            `json:"activeDevices"`
	Requests            int            `json:"requests"`
	MessagesReceived    int            `json:"messagesReceived"`
	MessagesStored      int            `json:"messagesStored"`
	MailSent            int            `json:"mailSent"`
	SmsSent             int            `json:"smsSent"`
	GeocoderRequests    int            `json:"geocoderRequests"`
	GeolocationRequests int            `json:"geolocationRequests"`
	Protocols           map[string]int `json:"protocols"`
	Attributes          map[string]any `json:"attributes"`
}

func (s *Server) handleStatistics(w http.ResponseWriter, r *http.Request) {
	if s.statistics == nil {
		writeError(w, http.StatusNotImplemented, errors.New("statistics are not configured"))
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	from, to, err := parseTimeRange(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	snapshots, err := s.statistics.QueryStatistics(ctx, from, to)
	if err != nil {
		s.logf("api: query statistics: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	response := make([]statisticsResponse, 0, len(snapshots))
	for _, snapshot := range snapshots {
		protocols := snapshot.Protocols
		if protocols == nil {
			protocols = map[string]int{}
		}
		attributes := map[string]any(snapshot.Attributes)
		if attributes == nil {
			attributes = map[string]any{}
		}
		response = append(response, statisticsResponse{
			ID:                  snapshot.ID,
			CaptureTime:         snapshot.CaptureTime.UTC(),
			ActiveUsers:         snapshot.ActiveUsers,
			ActiveDevices:       snapshot.ActiveDevices,
			Requests:            snapshot.Requests,
			MessagesReceived:    snapshot.MessagesReceived,
			MessagesStored:      snapshot.MessagesStored,
			MailSent:            snapshot.MailSent,
			SmsSent:             snapshot.SmsSent,
			GeocoderRequests:    snapshot.GeocoderRequests,
			GeolocationRequests: snapshot.GeolocationRequests,
			Protocols:           protocols,
			Attributes:          attributes,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

type deviceStatisticsResponse struct {
	DeviceID       int64  `json:"deviceId"`
	DeviceName     string `json:"deviceName"`
	MessagesStored int64  `json:"messagesStored"`
}

func (s *Server) handleStatisticsDevices(w http.ResponseWriter, r *http.Request) {
	if s.statistics == nil || s.devices == nil {
		writeError(w, http.StatusNotImplemented, errors.New("statistics are not configured"))
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	query := r.URL.Query()
	from, to, err := parseTimeRange(query)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if from.IsZero() || to.IsZero() {
		writeError(w, http.StatusBadRequest, errors.New("from and to are required"))
		return
	}
	if to.Sub(from) > maxStatistics24H {
		writeError(w, http.StatusBadRequest, errors.New("the time period must not exceed 24 hours"))
		return
	}
	userID, err := idQuery(query, "userId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	deviceIDs := s.deviceScope(r)
	if userID > 0 && s.permissions != nil {
		// An admin asking for another account gets that account's devices; the
		// admin flag of the caller does not widen the answer.
		deviceIDs, err = s.permissions.DeviceIDs(ctx, userID)
		if err != nil {
			s.logf("api: devices of user %d: %v", userID, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
	}

	devices, err := s.devices.Devices(ctx, model.DeviceFilter{DeviceIDs: deviceIDs})
	if err != nil {
		s.logf("api: list devices for statistics: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	ids := make([]int64, 0, len(devices))
	for _, device := range devices {
		ids = append(ids, device.ID)
	}
	counts, err := s.statistics.PositionCounts(ctx, ids, from, to)
	if err != nil {
		s.logf("api: count positions: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	response := make([]deviceStatisticsResponse, 0, len(devices))
	for _, device := range devices {
		response = append(response, deviceStatisticsResponse{
			DeviceID:       device.ID,
			DeviceName:     device.Name,
			MessagesStored: counts[device.ID],
		})
	}
	writeJSON(w, http.StatusOK, response)
}

type actionResponse struct {
	ID         int64          `json:"id"`
	ActionTime time.Time      `json:"actionTime"`
	Address    string         `json:"address"`
	UserID     int64          `json:"userId"`
	UserEmail  string         `json:"userEmail"`
	ActionType string         `json:"actionType"`
	ObjectType string         `json:"objectType"`
	ObjectID   int64          `json:"objectId"`
	Attributes map[string]any `json:"attributes"`
}

// handleAudit lists the audit trail between two dates. Only an administrator may
// read it, matching the Java resource.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if s.auditStore == nil {
		writeError(w, http.StatusNotImplemented, errors.New("audit is not configured"))
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	query := r.URL.Query()
	from, to, err := parseTimeRange(query)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limit, err := parseLimit(query, maxAuditLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	offset, err := parseOffset(query)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	actions, err := s.auditStore.QueryActions(ctx, from, to, limit, offset)
	if err != nil {
		s.logf("api: query audit: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	response := make([]actionResponse, 0, len(actions))
	for _, action := range actions {
		attributes := map[string]any(action.Attributes)
		if attributes == nil {
			attributes = map[string]any{}
		}
		response = append(response, actionResponse{
			ID:         action.ID,
			ActionTime: action.ActionTime.UTC(),
			Address:    action.Address,
			UserID:     action.UserID,
			UserEmail:  action.UserEmail,
			ActionType: action.ActionType,
			ObjectType: action.ObjectType,
			ObjectID:   action.ObjectID,
			Attributes: attributes,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

// registerAdminRoutes mounts the administrative and session routes.
func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/protocols", s.handleProtocols)
	mux.HandleFunc("PUT /api/protocols/{id}", s.handleUpdateProtocol)

	mux.HandleFunc("GET /api/server", s.handleServer)
	mux.HandleFunc("PUT /api/server", s.handleUpdateServer)
	mux.HandleFunc("GET /api/server/timezones", s.handleServerTimezones)
	mux.HandleFunc("GET /api/server/geocode", s.handleServerGeocode)
	mux.HandleFunc("GET /api/server/gc", s.handleServerGC)
	mux.HandleFunc("POST /api/server/reboot", s.handleServerReboot)
	mux.HandleFunc("POST /api/server/file/{path...}", s.handleServerFile)

	mux.HandleFunc("GET /api/statistics", s.handleStatistics)
	mux.HandleFunc("GET /api/statistics/devices", s.handleStatisticsDevices)
	mux.HandleFunc("GET /api/audit", s.handleAudit)

	mux.HandleFunc("GET /api/notificators", s.handleNotificators)
	mux.HandleFunc("GET /api/notifications/notificators", s.handleNotificators)
	mux.HandleFunc("GET /api/notifications/types", s.handleNotificationTypes)
	mux.HandleFunc("POST /api/notifications/test", s.handleNotificationTest)
	mux.HandleFunc("POST /api/notifications/test/{notificator}", s.handleNotificationTest)
	mux.HandleFunc("POST /api/notifications/send/{notificator}", s.handleNotificationSend)

	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("POST /api/session", s.handleSessionCreate)
	mux.HandleFunc("DELETE /api/session", s.handleSessionDelete)
	mux.HandleFunc("GET /api/session/{id}", s.handleSessionUser)
	mux.HandleFunc("POST /api/session/token", s.handleSessionToken)
	mux.HandleFunc("POST /api/session/token/revoke", s.handleSessionRevoke)

	mux.HandleFunc("GET /api/commands/send", s.handleCommandsSendList)
	mux.HandleFunc("POST /api/commands/send", s.handleCommandSend)
	mux.HandleFunc("POST /api/commands/bulk", s.handleCommandBulk)
	mux.HandleFunc("GET /api/commands/types", s.handleCommandTypes)

	mux.HandleFunc("GET /api/reports/route", s.handleReportRoute)
	mux.HandleFunc("GET /api/reports/trips", s.handleReportTrips)
	mux.HandleFunc("GET /api/reports/stops", s.handleReportStops)
	mux.HandleFunc("GET /api/reports/summary", s.handleReportSummary)
	mux.HandleFunc("GET /api/reports/events", s.handleReportEvents)
	mux.HandleFunc("GET /api/reports/geofences", s.handleReportGeofences)
	mux.HandleFunc("GET /api/reports/devices", s.handleReportDevices)
	mux.HandleFunc("GET /api/reports/combined", s.handleReportCombined)
	for _, name := range []string{"route", "trips", "stops", "summary", "events", "geofences", "devices"} {
		mux.HandleFunc("GET /api/reports/"+name+"/{type}", s.handleReportExport(name))
	}
}
