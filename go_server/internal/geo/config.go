package geo

import (
	"log"
	"time"

	"traccar-server_go/go_server/internal/config"
)

// Configuration keys of the geographic services. The enable/url/key spelling
// follows the Java server; cache.file and the request timeout are Go additions.
const (
	KeyGeocoderEnable   = config.GeocoderEnable
	KeyGeocoderType     = config.GeocoderType
	KeyGeocoderURL      = config.GeocoderURL
	KeyGeocoderKey      = config.GeocoderKey
	KeyGeocoderLanguage = config.GeocoderLanguage
	KeyGeocoderCache    = config.GeocoderCache

	KeySpeedLimitEnable = config.SpeedLimitEnable
	KeySpeedLimitURL    = config.SpeedLimitURL
	KeySpeedLimitRadius = config.SpeedLimitRadius
	KeySpeedLimitCache  = config.SpeedLimitCache

	KeyMapMatcherEnable = config.MapMatcherEnable
	KeyMapMatcherURL    = config.MapMatcherURL
	KeyMapMatcherKey    = config.MapMatcherKey
	KeyMapMatcherRadius = config.MapMatcherRadius
	KeyMapMatcherCache  = config.MapMatcherCache
	KeyMapMatcherCacheFile = config.MapMatcherCacheFile

	KeyGeolocationEnable = config.GeolocationEnable
	KeyGeolocationURL    = config.GeolocationURL
	KeyGeolocationKey    = config.GeolocationKey
	KeyGeolocationCache  = config.GeolocationCache

	KeyCacheFile      = config.GeocoderCacheFile
	KeyRequestTimeout = config.GeocoderTimeout
)

// Service URLs of the free endpoints, used when the operator configures a key
// or simply turns the service on.
const (
	nominatimURL  = "https://nominatim.openstreetmap.org/reverse"
	googleURL     = "https://maps.googleapis.com/maps/api/geocode/json"
	overpassURL   = "https://overpass-api.de/api/interpreter"
	mapboxURL     = "https://api.mapbox.com/matching/v5/mapbox/driving"
	geolocateURL  = "https://www.googleapis.com/geolocation/v1/geolocate"
	userAgentName = "traccar-server_go"
)

// Settings is the resolved configuration of the geographic services.
type Settings struct {
	Geocoder struct {
		Enabled  bool
		Type     string
		URL      string
		Key      string
		Language string
		CacheTTL time.Duration
	}
	SpeedLimit struct {
		Enabled bool
		URL     string
		Radius  int
		Cache   time.Duration
	}
	MapMatcher struct {
		Enabled bool
		URL     string
		Key     string
		Radius  int
		Cache   time.Duration
		// CacheFile is where the snapped-coordinate cache is persisted. It
		// falls back to the shared cache.file so an operator who only set that
		// still gets a persisting cache.
		CacheFile string
	}
	Geolocation struct {
		Enabled bool
		URL     string
		Key     string
		Cache   time.Duration
	}
	CacheFile string
	Timeout   time.Duration
}

// ReadSettings reads the geographic keys. A service without a URL or a key
// stays disabled, because silently calling a third party with no configuration
// is never what an operator meant.
func ReadSettings(cfg *config.Config) Settings {
	settings := Settings{
		CacheFile: "",
		Timeout:   10 * time.Second,
	}
	if cfg == nil {
		return settings
	}

	readBool := func(key string) bool {
		value, err := cfg.Bool(key)
		if err != nil {
			log.Printf("geo: %v", err)
			return false
		}
		return value
	}
	readDuration := func(key string, fallback time.Duration) time.Duration {
		value, err := cfg.Duration(key)
		if err != nil {
			log.Printf("geo: %v", err)
			return fallback
		}
		if value <= 0 {
			return fallback
		}
		return value
	}

	settings.Geocoder.Enabled = readBool(KeyGeocoderEnable)
	settings.Geocoder.Type = cfg.StringOr(KeyGeocoderType, "nominatim")
	settings.Geocoder.URL = cfg.String(KeyGeocoderURL)
	settings.Geocoder.Key = cfg.String(KeyGeocoderKey)
	settings.Geocoder.Language = cfg.String(KeyGeocoderLanguage)
	settings.Geocoder.CacheTTL = readDuration(KeyGeocoderCache, DefaultCacheTTL)

	settings.SpeedLimit.Enabled = readBool(KeySpeedLimitEnable)
	settings.SpeedLimit.URL = cfg.StringOr(KeySpeedLimitURL, overpassURL)
	settings.SpeedLimit.Cache = readDuration(KeySpeedLimitCache, DefaultCacheTTL)
	if radius, err := cfg.Int(KeySpeedLimitRadius); err == nil && radius > 0 {
		settings.SpeedLimit.Radius = radius
	} else {
		settings.SpeedLimit.Radius = 25
	}

	settings.MapMatcher.Enabled = readBool(KeyMapMatcherEnable)
	settings.MapMatcher.URL = cfg.StringOr(KeyMapMatcherURL, mapboxURL)
	settings.MapMatcher.Key = cfg.String(KeyMapMatcherKey)
	if radius, err := cfg.Int(KeyMapMatcherRadius); err == nil && radius > 0 {
		settings.MapMatcher.Radius = radius
	} else {
		settings.MapMatcher.Radius = 50
	}
	settings.MapMatcher.Cache = readDuration(KeyMapMatcherCache, DefaultCacheTTL)
	settings.MapMatcher.CacheFile = cfg.StringOr(KeyMapMatcherCacheFile, settings.CacheFile)

	settings.Geolocation.Enabled = readBool(KeyGeolocationEnable)
	settings.Geolocation.URL = cfg.StringOr(KeyGeolocationURL, geolocateURL)
	settings.Geolocation.Key = cfg.String(KeyGeolocationKey)
	settings.Geolocation.Cache = readDuration(KeyGeolocationCache, DefaultCacheTTL)

	settings.CacheFile = cfg.String(KeyCacheFile)
	settings.Timeout = readDuration(KeyRequestTimeout, settings.Timeout)

	// A key based service cannot work without its key, so say so once instead of
	// failing on every request.
	if settings.Geocoder.Enabled && settings.Geocoder.Key == "" &&
		settings.Geocoder.Type != "nominatim" {
		log.Printf("geo: geocoder.type %q needs %s; geocoding stays disabled",
			settings.Geocoder.Type, KeyGeocoderKey)
		settings.Geocoder.Enabled = false
	}
	if settings.MapMatcher.Enabled && settings.MapMatcher.Key == "" {
		log.Printf("geo: map matching needs %s; it stays disabled", KeyMapMatcherKey)
		settings.MapMatcher.Enabled = false
	}
	if settings.Geolocation.Enabled && settings.Geolocation.Key == "" {
		log.Printf("geo: geolocation needs %s; it stays disabled", KeyGeolocationKey)
		settings.Geolocation.Enabled = false
	}
	return settings
}
