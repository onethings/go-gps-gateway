package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"traccar-server_go/go_server/internal/handler"
	"traccar-server_go/go_server/internal/model"
)

// SpeedLimits answers the posted limit of a coordinate from an Overpass server.
// A lookup never runs on the ingest goroutine: Lookup returns what the cache
// knows and schedules a refresh, so a slow third party only means the attribute
// arrives one position later.
type SpeedLimits struct {
	baseURL string
	radius  int
	client  *http.Client
	cache   *Cache
	logf    func(format string, args ...any)

	mu       sync.Mutex
	inflight map[string]bool
	wg       sync.WaitGroup
}

// NewSpeedLimits creates the service.
func NewSpeedLimits(settings Settings, client *http.Client, logf func(string, ...any)) *SpeedLimits {
	if client == nil {
		client = &http.Client{Timeout: settings.Timeout}
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &SpeedLimits{
		baseURL:  settings.SpeedLimit.URL,
		radius:   settings.SpeedLimit.Radius,
		client:   client,
		cache:    NewCache(settings.SpeedLimit.Cache, settings.CacheFile),
		logf:     logf,
		inflight: make(map[string]bool),
	}
}

// Lookup returns the cached limit for a coordinate in knots. The second result
// reports whether the cache knows the answer; a miss triggers a background
// request and still returns false, so the caller keeps working.
func (s *SpeedLimits) Lookup(ctx context.Context, latitude, longitude float64) (float64, bool) {
	if s == nil {
		return 0, false
	}
	key := KeyFor("speedlimit", latitude, longitude)
	if value, ok := s.cache.Get(key); ok {
		limit, err := strconv.ParseFloat(value, 64)
		if err == nil {
			return limit, true
		}
	}

	s.refresh(ctx, key, latitude, longitude)
	return 0, false
}

// Fetch asks the service for the limit and waits for the answer. The ingest path
// uses Lookup instead; this is for the REST route, where the caller is a user who
// expects the value in the response.
func (s *SpeedLimits) Fetch(ctx context.Context, latitude, longitude float64) (float64, bool, error) {
	if s == nil {
		return 0, false, nil
	}
	key := KeyFor("speedlimit", latitude, longitude)
	if value, ok := s.cache.Get(key); ok {
		if limit, err := strconv.ParseFloat(value, 64); err == nil {
			return limit, true, nil
		}
	}

	limit, err := s.query(ctx, latitude, longitude)
	if err != nil {
		// "No road with a limit around the position" is an answer, not a failure.
		if strings.Contains(err.Error(), "no maxspeed") {
			return 0, false, nil
		}
		return 0, false, err
	}
	s.cache.Set(key, strconv.FormatFloat(limit, 'f', -1, 64))
	return limit, true, nil
}

// Load reads the persisted cache.
func (s *SpeedLimits) Load() error { return s.cache.Load() }

// Save writes the persisted cache.
func (s *SpeedLimits) Save() error { return s.cache.Save() }

// refresh starts one background request per coordinate.
func (s *SpeedLimits) refresh(ctx context.Context, key string, latitude, longitude float64) {
	s.mu.Lock()
	if s.inflight[key] {
		s.mu.Unlock()
		return
	}
	s.inflight[key] = true
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, key)
			s.mu.Unlock()
		}()

		limit, err := s.query(ctx, latitude, longitude)
		if err != nil {
			s.logf("geo: speedlimit %f,%f: %v", latitude, longitude, err)
			return
		}
		s.cache.Set(key, strconv.FormatFloat(limit, 'f', -1, 64))
	}()
}

// query asks Overpass for the ways around the coordinate that carry a maxspeed
// tag and keeps the lowest one, which is what the driver has to obey.
func (s *SpeedLimits) query(ctx context.Context, latitude, longitude float64) (float64, error) {
	query := fmt.Sprintf(
		"[out:json];way(around:%d,%f,%f)[highway][maxspeed];out tags 25;",
		s.radius, latitude, longitude)

	form := url.Values{}
	form.Set("data", query)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return 0, fmt.Errorf("geo: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	body, err := readJSON(s.client, request)
	if err != nil {
		return 0, err
	}
	var parsed struct {
		Elements []struct {
			Tags struct {
				MaxSpeed string `json:"maxspeed"`
			} `json:"tags"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, fmt.Errorf("geo: overpass: %w", err)
	}

	limit := 0.0
	for _, element := range parsed.Elements {
		value, ok := parseMaxSpeed(element.Tags.MaxSpeed)
		if !ok {
			continue
		}
		if limit == 0 || value < limit {
			limit = value
		}
	}
	if limit == 0 {
		return 0, fmt.Errorf("geo: overpass: no maxspeed around the position")
	}
	return limit, nil
}

// parseMaxSpeed reads the OpenStreetMap maxspeed value, which is a number in
// km/h unless it carries a unit, or a named zone the map does not resolve.
func parseMaxSpeed(raw string) (float64, bool) {
	text := strings.TrimSpace(strings.ToLower(raw))
	if text == "" {
		return 0, false
	}
	if text == "none" || text == "signals" || text == "walk" {
		return 0, false
	}

	unit := "km/h"
	if strings.HasSuffix(text, "mph") {
		unit = "mph"
		text = strings.TrimSpace(strings.TrimSuffix(text, "mph"))
	} else if strings.HasSuffix(text, "knots") {
		return 0, false
	} else if strings.HasSuffix(text, "km/h") {
		text = strings.TrimSpace(strings.TrimSuffix(text, "km/h"))
	}
	// A named zone such as "RO:urban" has no number and is skipped.
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	switch unit {
	case "mph":
		return kmhToKnots(value * 1.609344), true
	default:
		return kmhToKnots(value), true
	}
}

// Close waits for the background requests to finish, bounded by ctx.
func (s *SpeedLimits) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stage is the chain stage that writes the limit onto the position. It only
// reads the cache, so it never slows the ingest down.
type Stage struct {
	Limits *SpeedLimits
}

// Name implements handler.PositionHandler.
func (s Stage) Name() string { return handler.StageSpeedLimit }

// HandlePosition implements handler.PositionHandler.
func (s Stage) HandlePosition(
	ctx context.Context, position *model.Position, next handler.Next,
) error {
	if s.Limits != nil && position != nil && position.Valid {
		if limit, ok := s.Limits.Lookup(ctx, position.Latitude, position.Longitude); ok {
			// The attribute is in knots, the same unit as the position speed, so
			// the overspeed rule compares them directly.
			position.Set(model.KeySpeedLimit, limit)
		}
	}
	return next(ctx, position)
}
