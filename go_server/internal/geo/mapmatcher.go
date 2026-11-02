package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"traccar-server_go/go_server/internal/handler"
	"traccar-server_go/go_server/internal/model"
)

// Matcher snaps a coordinate onto the nearest road. The implementation talks to
// the Mapbox map matching API, which needs a key; without one the service stays
// disabled rather than guessing.
type Matcher struct {
	baseURL string
	key     string
	radius  int
	client  *http.Client
	cache   *Cache
}

// NewMatcher creates the matcher.
func NewMatcher(settings Settings, client *http.Client) *Matcher {
	if client == nil {
		client = &http.Client{Timeout: settings.Timeout}
	}
	return &Matcher{
		baseURL: settings.MapMatcher.URL,
		key:     settings.MapMatcher.Key,
		radius:  settings.MapMatcher.Radius,
		client:  client,
		cache:   NewCache(settings.MapMatcher.Cache, settings.MapMatcher.CacheFile),
	}
}

// Snap returns the coordinate moved onto the matched road. The result reports
// whether a match was found; an unmatched coordinate is left alone.
func (m *Matcher) Snap(ctx context.Context, latitude, longitude float64) (float64, float64, bool) {
	key := KeyFor("mapmatcher", latitude, longitude)
	if cached, ok := m.cache.Get(key); ok {
		parts, err := decodePair(cached)
		if err == nil {
			return parts[0], parts[1], true
		}
	}

	matched, ok, err := m.query(ctx, latitude, longitude)
	if err != nil || !ok {
		return latitude, longitude, false
	}
	m.cache.Set(key, formatPair(matched[0], matched[1]))
	return matched[0], matched[1], true
}

// Load reads the persisted cache.
func (m *Matcher) Load() error { return m.cache.Load() }

// Save writes the persisted cache.
func (m *Matcher) Save() error { return m.cache.Save() }

// query asks Mapbox for the nearest point on a road.
func (m *Matcher) query(ctx context.Context, latitude, longitude float64) ([2]float64, bool, error) {
	var result [2]float64
	if m.key == "" {
		return result, false, fmt.Errorf("geo: map matching needs a key")
	}

	// Mapbox takes longitude first, then latitude.
	target := fmt.Sprintf("%s/%f,%f.json", m.baseURL, longitude, latitude)
	query := url.Values{}
	query.Set("access_token", m.key)
	query.Set("radiuses", strconv.Itoa(m.radius))
	query.Set("geometries", "geojson")

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target+"?"+query.Encode(), nil)
	if err != nil {
		return result, false, fmt.Errorf("geo: %w", err)
	}

	body, err := readJSON(m.client, request)
	if err != nil {
		return result, false, err
	}
	var parsed struct {
		Code      string `json:"code"`
		Matchings []struct {
			Geometry struct {
				// GeoJSON order: one [longitude, latitude] pair per point.
				Coordinates [][]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"matchings"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return result, false, fmt.Errorf("geo: mapbox: %w", err)
	}
	if len(parsed.Matchings) == 0 || len(parsed.Matchings[0].Geometry.Coordinates) == 0 {
		return result, false, nil
	}
	point := parsed.Matchings[0].Geometry.Coordinates[0]
	if len(point) < 2 {
		return result, false, nil
	}
	return [2]float64{point[1], point[0]}, true, nil
}

// MapMatcherStage is the chain stage that snaps a valid position's coordinates
// onto the nearest road. It mirrors the Java server's MapMatcherHandler, which
// rewrites the position before distance, geofence and report stages see it.
type MapMatcherStage struct {
	// Matcher is the snapping service. A nil matcher leaves the position alone.
	Matcher *Matcher
}

// Name implements handler.PositionHandler.
func (s MapMatcherStage) Name() string { return handler.StageMapMatcher }

// HandlePosition implements handler.PositionHandler. Only a valid GPS fix is
// snapped; a position without one is left alone. A missing match keeps the
// original coordinate, exactly as the Mapbox API reports an unsnapped point.
func (s MapMatcherStage) HandlePosition(
	ctx context.Context, position *model.Position, next handler.Next,
) error {
	if s.Matcher != nil && position != nil && position.Valid {
		latitude, longitude, ok := s.Matcher.Snap(ctx, position.Latitude, position.Longitude)
		if ok {
			position.Latitude = latitude
			position.Longitude = longitude
		}
	}
	return next(ctx, position)
}

func formatPair(latitude, longitude float64) string {
	return strconv.FormatFloat(latitude, 'f', 6, 64) + "," +
		strconv.FormatFloat(longitude, 'f', 6, 64)
}

func decodePair(text string) ([2]float64, error) {
	var result [2]float64
	comma := -1
	for index := 0; index < len(text); index++ {
		if text[index] == ',' {
			comma = index
			break
		}
	}
	if comma < 0 {
		return result, fmt.Errorf("geo: %q is not a coordinate pair", text)
	}
	latitude, err := strconv.ParseFloat(text[:comma], 64)
	if err != nil {
		return result, err
	}
	longitude, err := strconv.ParseFloat(text[comma+1:], 64)
	if err != nil {
		return result, err
	}
	result = [2]float64{latitude, longitude}
	return result, nil
}
