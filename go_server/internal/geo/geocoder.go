package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Geocoder resolves a coordinate to an address using the provider selected by
// geocoder.type. Providers that call a remote service are cached; pluscodes
// are computed locally.
type Geocoder struct {
	provider string
	baseURL  string
	key      string
	language string
	client   *http.Client
	cache    *Cache
	// OnRequest is called before a remote request, so the caller can count it.
	OnRequest func()
}

// NewGeocoder creates the geocoder from the settings. A nil client means the
// shared one; tests inject their own transport through it.
func NewGeocoder(settings Settings, client *http.Client) *Geocoder {
	baseURL := settings.Geocoder.URL
	if baseURL == "" {
		baseURL = defaultGeocoderURL(settings.Geocoder.Type)
	}
	if client == nil {
		client = &http.Client{Timeout: settings.Timeout}
	}
	return &Geocoder{
		provider: strings.ToLower(settings.Geocoder.Type),
		baseURL:  baseURL,
		key:      settings.Geocoder.Key,
		language: settings.Geocoder.Language,
		client:   client,
		cache:    NewCache(settings.Geocoder.CacheTTL, settings.CacheFile),
	}
}

// Address implements api.Geocoder.
func (g *Geocoder) Address(ctx context.Context, latitude, longitude float64) (string, error) {
	key := KeyFor("geocode", latitude, longitude)
	if address, ok := g.cache.Get(key); ok {
		return address, nil
	}

	// Plus codes are computed locally, so they cost neither a request nor a key.
	if g.provider == "pluscodes" {
		return plusCode(latitude, longitude), nil
	}

	if g.OnRequest != nil {
		g.OnRequest()
	}
	address, err := g.lookup(ctx, latitude, longitude)
	if err != nil {
		return "", err
	}
	g.cache.Set(key, address)
	return address, nil
}

// Load reads the persisted cache.
func (g *Geocoder) Load() error { return g.cache.Load() }

// Save writes the persisted cache.
func (g *Geocoder) Save() error { return g.cache.Save() }

// lookup calls the configured provider. The response shapes differ, so each one
// has its own parser.
func (g *Geocoder) lookup(ctx context.Context, latitude, longitude float64) (string, error) {
	switch g.provider {
	case "", "nominatim":
		return g.nominatim(ctx, latitude, longitude)
	case "google":
		return g.google(ctx, latitude, longitude)
	case "opencage":
		return g.openCage(ctx, latitude, longitude)
	case "here":
		return g.hereGeocode(ctx, latitude, longitude)
	case "locationiq":
		return g.locationIQ(ctx, latitude, longitude)
	case "mapbox":
		return g.mapbox(ctx, latitude, longitude)
	case "tomtom":
		return g.tomtom(ctx, latitude, longitude)
	case "photon":
		return g.photon(ctx, latitude, longitude)
	case "pelias":
		return g.pelias(ctx, latitude, longitude)
	case "autonavi":
		return g.autoNavi(ctx, latitude, longitude)
	case "baidu":
		return g.baidu(ctx, latitude, longitude)
	case "ban":
		return g.geocodeJSON(ctx, latitude, longitude, "FR")
	case "bingmaps":
		return g.bingMaps(ctx, latitude, longitude)
	case "factual":
		return g.factual(ctx, latitude, longitude)
	case "geoapify":
		return g.geoapify(ctx, latitude, longitude)
	case "geocodefarm":
		return g.geocodeFarm(ctx, latitude, longitude)
	case "geocodejson":
		return g.geocodeJSON(ctx, latitude, longitude, "")
	case "geocodexyz":
		return g.geocodeXyz(ctx, latitude, longitude)
	case "gisgraphy":
		return g.gisgraphy(ctx, latitude, longitude)
	case "mapquest":
		return g.mapQuest(ctx, latitude, longitude)
	case "maptiler":
		return g.mapTiler(ctx, latitude, longitude)
	case "mapmyindia":
		return g.mapmyIndia(ctx, latitude, longitude)
	case "mygeocode":
		return g.myGeocode(ctx, latitude, longitude)
	case "positionstack":
		return g.positionStack(ctx, latitude, longitude)
	case "tencent":
		return g.tencent(ctx, latitude, longitude)
	case "pluscodes":
		return plusCode(latitude, longitude), nil
	default:
		return "", fmt.Errorf("geo: unknown geocoder type %q", g.provider)
	}
}

// nominatim asks the OpenStreetMap service, which only needs an identifying
// user agent.
func (g *Geocoder) nominatim(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("format", "jsonv2")
	query.Set("lat", strconv.FormatFloat(latitude, 'f', 6, 64))
	query.Set("lon", strconv.FormatFloat(longitude, 'f', 6, 64))
	if g.language != "" {
		query.Set("accept-language", g.language)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		g.baseURL+"?"+query.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("geo: %w", err)
	}
	request.Header.Set("User-Agent", userAgentName)

	body, err := readJSON(g.client, request)
	if err != nil {
		return "", err
	}
	var parsed struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: nominatim: %w", err)
	}
	if parsed.DisplayName == "" {
		return "", fmt.Errorf("geo: nominatim: no address")
	}
	return parsed.DisplayName, nil
}

// google asks the Google Geocoding API.
func (g *Geocoder) google(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("latlng", strconv.FormatFloat(latitude, 'f', 6, 64)+","+
		strconv.FormatFloat(longitude, 'f', 6, 64))
	query.Set("key", g.key)
	if g.language != "" {
		query.Set("language", g.language)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		g.baseURL+"?"+query.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("geo: %w", err)
	}

	body, err := readJSON(g.client, request)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Status  string `json:"status"`
		Results []struct {
			FormattedAddress string `json:"formatted_address"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: google: %w", err)
	}
	if len(parsed.Results) == 0 || parsed.Results[0].FormattedAddress == "" {
		return "", fmt.Errorf("geo: google: no address (%s)", parsed.Status)
	}
	return parsed.Results[0].FormattedAddress, nil
}

// readJSON runs one request and returns the body, turning a non 2xx answer into
// an error that carries the status.
func readJSON(client *http.Client, request *http.Request) ([]byte, error) {
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("geo: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("geo: read: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("geo: %s answered %d", request.URL.Host, response.StatusCode)
	}
	return body, nil
}
