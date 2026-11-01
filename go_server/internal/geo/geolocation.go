package geo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"traccar-server_go/go_server/internal/model"
)

// Locator turns a cell tower description into a coordinate. It talks to the
// Google Geolocation API, which needs a key, and caches the answer per tower so
// a device that reports the same cell does not cost another request.
type Locator struct {
	baseURL string
	key     string
	client  *http.Client
	cache   *Cache
	// OnRequest is called before a remote request, so the caller can count it.
	OnRequest func()
}

// NewLocator creates the locator.
func NewLocator(settings Settings, client *http.Client) *Locator {
	if client == nil {
		client = &http.Client{Timeout: settings.Timeout}
	}
	return &Locator{
		baseURL: settings.Geolocation.URL,
		key:     settings.Geolocation.Key,
		client:  client,
		cache:   NewCache(settings.Geolocation.Cache, settings.CacheFile),
	}
}

// Locate returns the coordinate of a cell tower group. The second result reports
// a hit: a miss is not an error, it only means the position cannot be filled in.
func (l *Locator) Locate(ctx context.Context, network *model.Network) (float64, float64, bool) {
	if l == nil || network.IsEmpty() || l.key == "" {
		return 0, 0, false
	}
	key := CellKey(network)
	if cached, ok := l.cache.Get(key); ok {
		if parts, err := decodePair(cached); err == nil {
			return parts[0], parts[1], true
		}
	}

	if l.OnRequest != nil {
		l.OnRequest()
	}
	latitude, longitude, ok, err := l.query(ctx, network)
	if err != nil || !ok {
		return 0, 0, false
	}
	l.cache.Set(key, formatPair(latitude, longitude))
	return latitude, longitude, true
}

// Load reads the persisted cache.
func (l *Locator) Load() error { return l.cache.Load() }

// Save writes the persisted cache.
func (l *Locator) Save() error { return l.cache.Save() }

// query posts the cell towers to the API.
func (l *Locator) query(ctx context.Context, network *model.Network) (float64, float64, bool, error) {
	type cellTower struct {
		CellID            int64 `json:"cellId"`
		LocationAreaCode  int   `json:"locationAreaCode"`
		MobileCountryCode int   `json:"mobileCountryCode"`
		MobileNetworkCode int   `json:"mobileNetworkCode"`
		SignalStrength    int   `json:"signalStrength,omitempty"`
	}
	type request_ struct {
		HomeMobileCountryCode int         `json:"homeMobileCountryCode,omitempty"`
		HomeMobileNetworkCode int         `json:"homeMobileNetworkCode,omitempty"`
		RadioType             string      `json:"radioType,omitempty"`
		ConsiderIP            bool        `json:"considerIp"`
		CellTowers            []cellTower `json:"cellTowers"`
	}

	payload := request_{
		HomeMobileCountryCode: network.HomeMobileCountryCode,
		HomeMobileNetworkCode: network.HomeMobileNetworkCode,
		RadioType:             network.RadioType,
		// A cell only lookup must not fall back to the server's own IP address,
		// which would put every unknown device in the data centre.
		ConsiderIP: false,
	}
	for _, tower := range network.CellTowers {
		payload.CellTowers = append(payload.CellTowers, cellTower{
			CellID:            tower.CellID,
			LocationAreaCode:  tower.LocationAreaCode,
			MobileCountryCode: tower.MobileCountryCode,
			MobileNetworkCode: tower.MobileNetworkCode,
			SignalStrength:    tower.SignalStrength,
		})
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, 0, false, fmt.Errorf("geo: encode cell towers: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		l.baseURL+"?key="+l.key, bytes.NewReader(encoded))
	if err != nil {
		return 0, 0, false, fmt.Errorf("geo: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	body, err := readJSON(l.client, request)
	if err != nil {
		return 0, 0, false, err
	}
	var parsed struct {
		Location struct {
			Latitude  float64 `json:"lat"`
			Longitude float64 `json:"lng"`
		} `json:"location"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, 0, false, fmt.Errorf("geo: geolocation: %w", err)
	}
	if parsed.Location.Latitude == 0 && parsed.Location.Longitude == 0 {
		return 0, 0, false, nil
	}
	return parsed.Location.Latitude, parsed.Location.Longitude, true, nil
}
