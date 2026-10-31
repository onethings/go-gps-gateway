// Package api exposes the read only REST endpoints of the gateway on top of
// net/http. Only alerts and a liveness probe exist so far.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/model"
)

const (
	defaultQueryTimeout = 5 * time.Second
	maxAlertLimit       = 1000
)

// AlertQuerier is the storage contract the alerts handler needs.
type AlertQuerier interface {
	QueryAlerts(ctx context.Context, filter model.AlertFilter) ([]model.Alert, error)
}

// Server holds the handlers and their configuration.
type Server struct {
	alerts       AlertQuerier
	logf         func(format string, args ...any)
	queryTimeout time.Duration
	now          func() time.Time
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
	mux.HandleFunc("GET /api/health", s.handleHealth)
	return mux
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

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   s.now().UTC().Format(time.RFC3339),
	})
}

// alertFilter parses and validates the query string:
//
//	deviceId=7&type=deviceOverspeed,geofenceEnter
//	from=2026-10-31T00:00:00Z&to=1780000000000&limit=50&offset=0
func alertFilter(r *http.Request) (model.AlertFilter, error) {
	query := r.URL.Query()
	var filter model.AlertFilter

	if raw := query.Get("deviceId"); raw != "" {
		deviceID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || deviceID <= 0 {
			return filter, fmt.Errorf("invalid deviceId %q", raw)
		}
		filter.DeviceID = deviceID
	}

	for _, raw := range query["type"] {
		for _, alertType := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(alertType); trimmed != "" {
				filter.Types = append(filter.Types, trimmed)
			}
		}
	}

	var err error
	if filter.From, err = parseTime(query.Get("from")); err != nil {
		return filter, fmt.Errorf("invalid from: %w", err)
	}
	if filter.To, err = parseTime(query.Get("to")); err != nil {
		return filter, fmt.Errorf("invalid to: %w", err)
	}
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.From.After(filter.To) {
		return filter, errors.New("from must not be after to")
	}

	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > maxAlertLimit {
			return filter, fmt.Errorf("limit must be between 1 and %d", maxAlertLimit)
		}
		filter.Limit = limit
	}
	if raw := query.Get("offset"); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return filter, fmt.Errorf("invalid offset %q", raw)
		}
		filter.Offset = offset
	}

	return filter, nil
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
