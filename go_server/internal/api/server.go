// Package api exposes the REST endpoints of the gateway on top of net/http:
// alert queries, a liveness probe and device commands.
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

	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
)

const (
	defaultQueryTimeout = 5 * time.Second
	maxAlertLimit       = 1000
	maxPositionLimit    = 1000
	maxDeviceLimit      = 1000
)

// AlertQuerier is the storage contract the alerts handler needs.
type AlertQuerier interface {
	QueryAlerts(ctx context.Context, filter model.AlertFilter) ([]model.Alert, error)
}

// CommandRouter encodes a command and delivers it to the device connection.
type CommandRouter interface {
	EncodeCommand(protocol string, command *model.Command) ([]byte, error)
	Send(protocol string, deviceID int64, payload []byte) error
}

// DeviceProtocols resolves the protocol a device speaks.
type DeviceProtocols interface {
	DeviceProtocol(ctx context.Context, deviceID int64) (protocol string, found bool, err error)
}

// PositionQuerier is the storage contract the positions handler needs.
type PositionQuerier interface {
	QueryPositions(ctx context.Context, filter model.PositionFilter) ([]model.Position, error)
}

// DeviceQuerier is the storage contract the devices handler needs.
type DeviceQuerier interface {
	Devices(ctx context.Context, filter model.DeviceFilter) ([]model.Device, error)
}

// Server holds the handlers and their configuration.
type Server struct {
	alerts       AlertQuerier
	positions    PositionQuerier
	devices      DeviceQuerier
	router       CommandRouter
	protocols    DeviceProtocols
	meters       *metrics.Set
	logf         func(format string, args ...any)
	queryTimeout time.Duration
	now          func() time.Time
	apiKey       string
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

// WithPositions enables GET /api/positions. Without it the route reports 501.
func WithPositions(positions PositionQuerier) Option {
	return func(s *Server) { s.positions = positions }
}

// WithDevices enables GET /api/devices. Without it the route reports 501.
func WithDevices(devices DeviceQuerier) Option {
	return func(s *Server) { s.devices = devices }
}

// WithAPIKey requires every request except /api/health to present the key.
// An empty key leaves the API open, which is only meant for local development.
func WithAPIKey(key string) Option {
	return func(s *Server) { s.apiKey = key }
}

// WithMetrics enables GET /metrics. Without it the route reports 501.
func WithMetrics(set *metrics.Set) Option {
	return func(s *Server) { s.meters = set }
}

// NewServer creates the API handlers.
func NewServer(alerts AlertQuerier, options ...Option) *Server {
	server := &Server{
		alerts:       alerts,
		logf:         func(string, ...any) {},
		queryTimeout: defaultQueryTimeout,
		now:          time.Now,
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
	mux.HandleFunc("GET /api/alerts", s.handleAlerts)
	mux.HandleFunc("GET /api/devices", s.handleDevices)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/positions", s.handlePositions)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("POST /api/commands", s.handleCommand)

	if s.apiKey == "" {
		return mux
	}
	return RequireAPIKey(s.apiKey, mux)
}

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

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	filter, err := alertFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	alerts, err := s.alerts.QueryAlerts(ctx, filter)
	if err != nil {
		// The database error stays in the log; the client only learns that the
		// request failed.
		s.logf("api: query alerts: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	response := alertsResponse{Alerts: make([]alertResponse, 0, len(alerts)), Count: len(alerts)}
	for _, alert := range alerts {
		response.Alerts = append(response.Alerts, alertResponse{
			ID:         alert.ID,
			DeviceID:   alert.DeviceID,
			PositionID: alert.PositionID,
			Type:       alert.Type,
			AlertTime:  alert.Time.UTC(),
			GeofenceID: alert.GeofenceID,
			Attributes: alert.Attributes,
		})
	}
	writeJSON(w, http.StatusOK, response)
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

	filter, err := positionFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	positions, err := s.positions.QueryPositions(ctx, filter)
	if err != nil {
		s.logf("api: query positions: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	response := positionsResponse{
		Positions: make([]positionResponse, 0, len(positions)),
		Count:     len(positions),
	}
	for _, position := range positions {
		attributes := position.Attributes
		if attributes == nil {
			attributes = map[string]any{}
		}
		response.Positions = append(response.Positions, positionResponse{
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
		})
	}
	writeJSON(w, http.StatusOK, response)
}

type deviceResponse struct {
	ID         int64          `json:"id"`
	UniqueID   string         `json:"uniqueId"`
	Name       string         `json:"name"`
	Model      string         `json:"model,omitempty"`
	Protocol   string         `json:"protocol"`
	Attributes map[string]any `json:"attributes"`
	// LastUpdate stays absent until the device has reported a position.
	LastUpdate *time.Time `json:"lastUpdate,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type devicesResponse struct {
	Devices []deviceResponse `json:"devices"`
	Count   int              `json:"count"`
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	if s.devices == nil {
		writeError(w, http.StatusNotImplemented, errors.New("devices are not configured"))
		return
	}

	filter, err := deviceFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	devices, err := s.devices.Devices(ctx, filter)
	if err != nil {
		s.logf("api: query devices: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	response := devicesResponse{
		Devices: make([]deviceResponse, 0, len(devices)),
		Count:   len(devices),
	}
	for _, device := range devices {
		attributes := device.Attributes
		if attributes == nil {
			attributes = map[string]any{}
		}
		entry := deviceResponse{
			ID:         device.ID,
			UniqueID:   device.UniqueID,
			Name:       device.Name,
			Model:      device.Model,
			Protocol:   device.Protocol,
			Attributes: attributes,
			CreatedAt:  device.CreatedAt.UTC(),
		}
		if !device.LastUpdate.IsZero() {
			lastUpdate := device.LastUpdate.UTC()
			entry.LastUpdate = &lastUpdate
		}
		response.Devices = append(response.Devices, entry)
	}
	writeJSON(w, http.StatusOK, response)
}

// handleMetrics serves the Prometheus text format. It is subject to the API key
// like every other route except /api/health.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	if s.meters == nil {
		writeError(w, http.StatusNotImplemented, errors.New("metrics are not configured"))
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.meters.Render(w)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   s.now().UTC().Format(time.RFC3339),
	})
}

const maxCommandBody = 1 << 16

type commandRequest struct {
	DeviceID   int64          `json:"deviceId"`
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes"`
}

type commandResponse struct {
	DeviceID int64  `json:"deviceId"`
	Type     string `json:"type"`
	Protocol string `json:"protocol"`
	Queued   bool   `json:"queued"`
}

// handleCommand encodes a device command and writes it to the device
// connection. Status codes are stable: 404 unknown device, 409 offline or
// unknown protocol, 400 unsupported command, 501 commands not configured.
func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if s.router == nil || s.protocols == nil {
		writeError(w, http.StatusNotImplemented, errors.New("commands are not configured"))
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

	if err := s.router.Send(protocol, request.DeviceID, payload); err != nil {
		if errors.Is(err, gateway.ErrDeviceOffline) {
			writeError(w, http.StatusConflict, fmt.Errorf("device %d is offline", request.DeviceID))
			return
		}
		s.logf("api: send command %s to device %d: %v", request.Type, request.DeviceID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	writeJSON(w, http.StatusAccepted, commandResponse{
		DeviceID: request.DeviceID,
		Type:     request.Type,
		Protocol: protocol,
		Queued:   true,
	})
}

// alertFilter parses and validates the query string:
//
//	deviceId=7&type=deviceOverspeed,geofenceEnter
//	from=2026-10-31T00:00:00Z&to=1780000000000&limit=50&offset=0
func alertFilter(r *http.Request) (model.AlertFilter, error) {
	query := r.URL.Query()
	var filter model.AlertFilter

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
//	protocol=gt06&limit=50&offset=0
func deviceFilter(r *http.Request) (model.DeviceFilter, error) {
	query := r.URL.Query()
	var filter model.DeviceFilter

	filter.Protocol = strings.TrimSpace(query.Get("protocol"))

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
