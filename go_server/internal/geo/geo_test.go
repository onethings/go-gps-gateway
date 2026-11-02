package geo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func respond(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func testSettings() Settings {
	var settings Settings
	settings.Geocoder.Enabled = true
	settings.Geocoder.Type = "nominatim"
	settings.Geocoder.CacheTTL = time.Hour
	settings.SpeedLimit.Enabled = true
	settings.SpeedLimit.URL = "http://overpass.test/interpreter"
	settings.SpeedLimit.Radius = 25
	settings.SpeedLimit.Cache = time.Hour
	settings.Geolocation.Enabled = true
	settings.Geolocation.URL = "http://geolocate.test"
	settings.Geolocation.Key = "key"
	settings.Geolocation.Cache = time.Hour
	settings.MapMatcher.Enabled = true
	settings.MapMatcher.URL = "http://mapbox.test/matching"
	settings.MapMatcher.Key = "token"
	settings.MapMatcher.Radius = 50
	settings.Timeout = 5 * time.Second
	return settings
}

func TestGeocoderCachesTheAnswer(t *testing.T) {
	var calls atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.URL.Query().Get("lat") == "" || request.URL.Query().Get("lon") == "" {
			t.Errorf("query = %s", request.URL.RawQuery)
		}
		if agent := request.Header.Get("User-Agent"); agent == "" {
			t.Error("nominatim requires a user agent")
		}
		return respond(http.StatusOK, `{"display_name":"Riga, Latvia"}`), nil
	})}

	geocoder := NewGeocoder(testSettings(), client)
	for attempt := 0; attempt < 2; attempt++ {
		address, err := geocoder.Address(context.Background(), 54.94535, 24.01762)
		if err != nil {
			t.Fatalf("address: %v", err)
		}
		if address != "Riga, Latvia" {
			t.Fatalf("address = %q", address)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want the second answer from the cache", calls.Load())
	}
}

func TestGoogleGeocoder(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("key") != "secret" {
			t.Errorf("key = %q", request.URL.Query().Get("key"))
		}
		return respond(http.StatusOK,
			`{"status":"OK","results":[{"formatted_address":"Riga, Latvia"}]}`), nil
	})}

	settings := testSettings()
	settings.Geocoder.Type = "google"
	settings.Geocoder.Key = "secret"
	address, err := NewGeocoder(settings, client).Address(context.Background(), 54.9, 24.0)
	if err != nil {
		t.Fatalf("address: %v", err)
	}
	if address != "Riga, Latvia" {
		t.Fatalf("address = %q", address)
	}
}

func TestGeocoderReportsAFailedCall(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusTooManyRequests, ""), nil
	})}
	if _, err := NewGeocoder(testSettings(), client).Address(context.Background(), 1, 2); err == nil {
		t.Fatal("a 429 was treated as an address")
	}
}

func TestSpeedLimitsLookupIsNonBlockingThenCached(t *testing.T) {
	release := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		<-release
		return respond(http.StatusOK,
			`{"elements":[{"tags":{"maxspeed":"70"}},{"tags":{"maxspeed":"50"}}]}`), nil
	})}

	limits := NewSpeedLimits(testSettings(), client, func(string, ...any) {})
	if _, ok := limits.Lookup(context.Background(), 54.94535, 24.01762); ok {
		t.Fatal("a cold cache answered")
	}
	// The lookup must have returned before the request completed, so a cold
	// cache costs the ingest nothing.
	close(release)
	if !waitIdle(limits, time.Second) {
		t.Fatal("the background request did not finish")
	}

	limit, ok := limits.Lookup(context.Background(), 54.94535, 24.01762)
	if !ok {
		t.Fatal("the cached limit is missing")
	}
	// The lowest limit wins and 50 km/h is 26.99 knots.
	if limit <= 26.9 || limit >= 27.1 {
		t.Fatalf("limit = %v knots", limit)
	}
}

func TestSpeedLimitFetchReportsNoRoad(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusOK, `{"elements":[]}`), nil
	})}
	limits := NewSpeedLimits(testSettings(), client, func(string, ...any) {})

	if _, known, err := limits.Fetch(context.Background(), 1, 2); err != nil || known {
		t.Fatalf("known = %v, err = %v", known, err)
	}
}

func TestParseMaxSpeed(t *testing.T) {
	cases := []struct {
		raw  string
		want float64
		ok   bool
	}{
		{"50", 26.99, true},
		{"50 km/h", 26.99, true},
		{"30 mph", 26.08, true},
		{"none", 0, false},
		{"RO:urban", 0, false},
		{"", 0, false},
	}
	for _, testCase := range cases {
		got, ok := parseMaxSpeed(testCase.raw)
		if ok != testCase.ok {
			t.Errorf("%q: ok = %v, want %v", testCase.raw, ok, testCase.ok)
			continue
		}
		if ok && (got-testCase.want > 0.05 || testCase.want-got > 0.05) {
			t.Errorf("%q: = %v, want %v", testCase.raw, got, testCase.want)
		}
	}
}

func TestStageWritesTheCachedLimit(t *testing.T) {
	settings := testSettings()
	parser := func(string, ...any) {}
	limits := NewSpeedLimits(settings, nil, parser)
	limits.cache.Set(KeyFor("speedlimit", 54.94535, 24.01762), "26.99")

	position := model.NewPosition("test")
	position.Latitude = 54.94535
	position.Longitude = 24.01762
	position.Valid = true

	stage := Stage{Limits: limits}
	called := false
	if err := stage.HandlePosition(context.Background(), position,
		func(context.Context, *model.Position) error {
			called = true
			return nil
		}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if !called {
		t.Fatal("the stage dropped the position")
	}
	if value, _ := position.Get(model.KeySpeedLimit); value != 26.99 {
		t.Fatalf("speedLimit = %v", value)
	}
}

func TestLocatorCachesTheCellTowers(t *testing.T) {
	var calls atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("body: %v", err)
		}
		if payload["considerIp"] != false {
			t.Errorf("considerIp = %v", payload["considerIp"])
		}
		return respond(http.StatusOK, `{"location":{"lat":54.9,"lng":24.0}}`), nil
	})}

	locator := NewLocator(testSettings(), client)
	network := model.NewNetworkWith(model.NewCellTower(247, 1, 100, 2000))
	for attempt := 0; attempt < 2; attempt++ {
		latitude, longitude, ok := locator.Locate(context.Background(), network)
		if !ok {
			t.Fatal("no position")
		}
		if latitude != 54.9 || longitude != 24.0 {
			t.Fatalf("coordinate = %v,%v", latitude, longitude)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want one request", calls.Load())
	}
}

func TestLocatorWithoutAKeyIsDisabled(t *testing.T) {
	settings := testSettings()
	settings.Geolocation.Key = ""
	locator := NewLocator(settings, nil)
	if _, _, ok := locator.Locate(context.Background(),
		model.NewNetworkWith(model.NewCellTower(1, 2, 3, 4))); ok {
		t.Fatal("a keyless locator answered")
	}
}

func TestMatcherSnapsToTheRoad(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		// Mapbox takes longitude first.
		if !strings.Contains(request.URL.Path, "24.017620,54.945350") {
			t.Errorf("path = %s", request.URL.Path)
		}
		return respond(http.StatusOK,
			`{"code":"Ok","matchings":[{"geometry":{"coordinates":[[24.0177,54.9454],[24.0178,54.9455]]}}]}`), nil
	})}

	matcher := NewMatcher(testSettings(), client)
	latitude, longitude, ok := matcher.Snap(context.Background(), 54.94535, 24.01762)
	if !ok {
		t.Fatal("no match")
	}
	if latitude != 54.9454 || longitude != 24.0177 {
		t.Fatalf("coordinate = %v,%v", latitude, longitude)
	}
}

func TestMatcherWithoutAKeyKeepsTheCoordinate(t *testing.T) {
	settings := testSettings()
	settings.MapMatcher.Key = ""
	matcher := NewMatcher(settings, nil)
	latitude, longitude, ok := matcher.Snap(context.Background(), 54.9, 24.0)
	if ok || latitude != 54.9 || longitude != 24.0 {
		t.Fatalf("coordinate = %v,%v ok = %v", latitude, longitude, ok)
	}
}

func TestMapMatcherStageSnapsThePosition(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return respond(http.StatusOK,
			`{"code":"Ok","matchings":[{"geometry":{"coordinates":[[24.0177,54.9454]]}}]}`), nil
	})}
	stage := MapMatcherStage{Matcher: NewMatcher(testSettings(), client)}

	position := model.NewPosition("test")
	position.Latitude = 54.94535
	position.Longitude = 24.01762
	position.Valid = true

	called := false
	if err := stage.HandlePosition(context.Background(), position,
		func(context.Context, *model.Position) error {
			called = true
			return nil
		}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if !called {
		t.Fatal("the stage dropped the position")
	}
	if position.Latitude != 54.9454 || position.Longitude != 24.0177 {
		t.Fatalf("coordinate = %v,%v, want the snapped point",
			position.Latitude, position.Longitude)
	}
}

func TestMapMatcherStageLeavesAnInvalidFixAlone(t *testing.T) {
	stage := MapMatcherStage{Matcher: NewMatcher(testSettings(), nil)}

	position := model.NewPosition("test")
	position.Latitude = 54.94535
	position.Longitude = 24.01762
	position.Valid = false

	if err := stage.HandlePosition(context.Background(), position,
		func(context.Context, *model.Position) error { return nil }); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if position.Latitude != 54.94535 || position.Longitude != 24.01762 {
		t.Fatalf("an invalid fix was moved to %v,%v",
			position.Latitude, position.Longitude)
	}
}

func TestCachePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "geo.json")
	cache := NewCache(time.Hour, path)
	cache.Set("geocode:1:2", "somewhere")
	if err := cache.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	}

	restored := NewCache(time.Hour, path)
	if err := restored.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if value, ok := restored.Get("geocode:1:2"); !ok || value != "somewhere" {
		t.Fatalf("value = %q, ok = %v", value, ok)
	}
}

func TestCacheExpires(t *testing.T) {
	cache := NewCache(time.Millisecond, "")
	cache.Set("key", "value")
	time.Sleep(5 * time.Millisecond)
	if _, ok := cache.Get("key"); ok {
		t.Fatal("an expired entry was returned")
	}
}

func TestReadSettingsKeepsKeyedServicesOffWithoutAKey(t *testing.T) {
	if settings := ReadSettings(nil); settings.Geocoder.Enabled {
		t.Fatal("geocoding is enabled without configuration")
	}

	settings := ReadSettings(config.Default())
	if settings.Geocoder.Enabled || settings.SpeedLimit.Enabled ||
		settings.Geolocation.Enabled || settings.MapMatcher.Enabled {
		t.Fatalf("settings = %+v", settings)
	}
}

func waitIdle(limits *SpeedLimits, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		limits.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
