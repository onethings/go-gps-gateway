package geo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"traccar-server_go/go_server/internal/model"
)

// Provider endpoints, used when the operator does not override geocoder.url or
// geolocation.url. Each provider has its own request and response shape, so the
// parsers live next to the URLs.
const (
	openCageURL      = "https://api.opencagedata.com/geocode/v1/json"
	hereGeocodeURL   = "https://revgeocode.search.hereapi.com/v1/revgeocode"
	locationIQURL    = "https://us1.locationiq.com/v1/reverse"
	mapboxGeocodeURL = "https://api.mapbox.com/geocoding/v5/mapbox.places"
	tomtomURL        = "https://api.tomtom.com/search/2/reverseGeocode"
	photonURL        = "https://photon.komoot.io/reverse"
	peliasURL        = "https://api.geocode.earth/v1/reverse"

	mozillaURL    = "https://location.services.mozilla.com/v1/geolocate"
	openCellIDURL = "https://opencellid.org/cell/get"
	unwiredURL    = "https://us1.unwiredlabs.com/v2/process.php"
	hereLocateURL = "https://pos.ls.hereapi.com/pos/v1/locate.json"
)

// geolocationProviderURL returns the default endpoint of a provider. An empty
// type means Google, which is what the Java server defaults to.
func geolocationProviderURL(provider string) string {
	switch strings.ToLower(provider) {
	case "mozilla":
		return mozillaURL
	case "opencellid":
		return openCellIDURL
	case "unwired":
		return unwiredURL
	case "here":
		return hereLocateURL
	case "universal":
		// The universal provider has no endpoint of its own; geolocation.url
		// must point at the operator's service.
		return ""
	default:
		return geolocateURL
	}
}

// geocodeRequest runs one GET and returns the body.
func (g *Geocoder) geocodeRequest(ctx context.Context, address string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("geo: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgentName)
	return readJSON(g.client, request)
}

// openCage asks the OpenCage geocoder.
func (g *Geocoder) openCage(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("q", coordinate(latitude, longitude))
	query.Set("key", g.key)
	if g.language != "" {
		query.Set("language", g.language)
	}
	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		Results []struct {
			Formatted string `json:"formatted"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: opencage: %w", err)
	}
	if len(parsed.Results) == 0 || parsed.Results[0].Formatted == "" {
		return "", fmt.Errorf("geo: opencage: no address")
	}
	return parsed.Results[0].Formatted, nil
}

// hereGeocode asks the HERE reverse geocoder.
func (g *Geocoder) hereGeocode(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("at", coordinate(latitude, longitude))
	query.Set("apiKey", g.key)
	if g.language != "" {
		query.Set("lang", g.language)
	}
	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		Items []struct {
			Address struct {
				Label string `json:"label"`
			} `json:"address"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: here: %w", err)
	}
	if len(parsed.Items) == 0 || parsed.Items[0].Address.Label == "" {
		return "", fmt.Errorf("geo: here: no address")
	}
	return parsed.Items[0].Address.Label, nil
}

// locationIQ speaks the Nominatim wire format with a key.
func (g *Geocoder) locationIQ(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("key", g.key)
	query.Set("lat", formatCoordinate(latitude))
	query.Set("lon", formatCoordinate(longitude))
	query.Set("format", "json")
	if g.language != "" {
		query.Set("accept-language", g.language)
	}
	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: locationiq: %w", err)
	}
	if parsed.DisplayName == "" {
		return "", fmt.Errorf("geo: locationiq: no address")
	}
	return parsed.DisplayName, nil
}

// mapbox asks the Mapbox places API. The coordinate order is longitude first.
func (g *Geocoder) mapbox(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("access_token", g.key)
	if g.language != "" {
		query.Set("language", g.language)
	}
	address := g.baseURL + "/" + formatCoordinate(longitude) + "," +
		formatCoordinate(latitude) + ".json?" + query.Encode()
	body, err := g.geocodeRequest(ctx, address)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Features []struct {
			PlaceName string `json:"place_name"`
		} `json:"features"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: mapbox: %w", err)
	}
	if len(parsed.Features) == 0 || parsed.Features[0].PlaceName == "" {
		return "", fmt.Errorf("geo: mapbox: no address")
	}
	return parsed.Features[0].PlaceName, nil
}

// tomtom asks the TomTom reverse geocoder.
func (g *Geocoder) tomtom(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("key", g.key)
	if g.language != "" {
		query.Set("language", g.language)
	}
	address := g.baseURL + "/" + coordinate(latitude, longitude) + ".json?" + query.Encode()
	body, err := g.geocodeRequest(ctx, address)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Addresses []struct {
			Address struct {
				FreeformAddress string `json:"freeformAddress"`
			} `json:"address"`
		} `json:"addresses"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: tomtom: %w", err)
	}
	if len(parsed.Addresses) == 0 || parsed.Addresses[0].Address.FreeformAddress == "" {
		return "", fmt.Errorf("geo: tomtom: no address")
	}
	return parsed.Addresses[0].Address.FreeformAddress, nil
}

// photon asks the Komoot Photon geocoder, which returns OSM style properties.
func (g *Geocoder) photon(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("lat", formatCoordinate(latitude))
	query.Set("lon", formatCoordinate(longitude))
	if g.language != "" {
		query.Set("lang", g.language)
	}
	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		Features []struct {
			Properties struct {
				Name    string `json:"name"`
				Street  string `json:"street"`
				House   string `json:"housenumber"`
				City    string `json:"city"`
				State   string `json:"state"`
				Country string `json:"country"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: photon: %w", err)
	}
	if len(parsed.Features) == 0 {
		return "", fmt.Errorf("geo: photon: no address")
	}
	properties := parsed.Features[0].Properties
	parts := []string{properties.Name}
	if properties.Street != "" {
		street := properties.Street
		if properties.House != "" {
			street = properties.House + " " + street
		}
		parts = append(parts, street)
	}
	parts = append(parts, properties.City, properties.State, properties.Country)
	address := joinAddress(parts)
	if address == "" {
		return "", fmt.Errorf("geo: photon: no address")
	}
	return address, nil
}

// pelias asks the Pelias geocoder used by geocode.earth.
func (g *Geocoder) pelias(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("point.lat", formatCoordinate(latitude))
	query.Set("point.lon", formatCoordinate(longitude))
	query.Set("api_key", g.key)
	if g.language != "" {
		query.Set("lang", g.language)
	}
	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		Features []struct {
			Properties struct {
				Label string `json:"label"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: pelias: %w", err)
	}
	if len(parsed.Features) == 0 || parsed.Features[0].Properties.Label == "" {
		return "", fmt.Errorf("geo: pelias: no address")
	}
	return parsed.Features[0].Properties.Label, nil
}

// openCellID asks the OpenCellID service about the strongest tower.
func (l *Locator) openCellID(ctx context.Context, network *model.Network) (float64, float64, bool, error) {
	tower := strongestTower(network)
	query := url.Values{}
	query.Set("key", l.key)
	query.Set("mcc", strconv.Itoa(tower.MobileCountryCode))
	query.Set("mnc", strconv.Itoa(tower.MobileNetworkCode))
	query.Set("lac", strconv.Itoa(tower.LocationAreaCode))
	query.Set("cellid", strconv.FormatInt(tower.CellID, 10))
	query.Set("format", "json")

	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		l.baseURL+"?"+query.Encode(), nil)
	if err != nil {
		return 0, 0, false, fmt.Errorf("geo: %w", err)
	}
	body, err := readJSON(l.client, request)
	if err != nil {
		return 0, 0, false, err
	}
	var parsed struct {
		Lat  float64 `json:"lat"`
		Lon  float64 `json:"lon"`
		Code int     `json:"code"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, 0, false, fmt.Errorf("geo: opencellid: %w", err)
	}
	if parsed.Code == 404 || (parsed.Lat == 0 && parsed.Lon == 0) {
		return 0, 0, false, nil
	}
	return parsed.Lat, parsed.Lon, true, nil
}

// unwired asks the Unwired Labs service.
func (l *Locator) unwired(ctx context.Context, network *model.Network) (float64, float64, bool, error) {
	type cell struct {
		LAC int   `json:"lac"`
		CID int64 `json:"cid"`
	}
	payload := struct {
		Token string `json:"token"`
		Radio string `json:"radio"`
		MCC   int    `json:"mcc"`
		MNC   int    `json:"mnc"`
		Cells []cell `json:"cells"`
	}{
		Token: l.key,
		Radio: network.RadioType,
		MCC:   network.HomeMobileCountryCode,
		MNC:   network.HomeMobileNetworkCode,
	}
	for _, tower := range network.CellTowers {
		payload.Cells = append(payload.Cells, cell{LAC: tower.LocationAreaCode, CID: tower.CellID})
		if payload.MCC == 0 {
			payload.MCC = tower.MobileCountryCode
			payload.MNC = tower.MobileNetworkCode
		}
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, 0, false, fmt.Errorf("geo: encode unwired request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, l.baseURL,
		bytes.NewReader(encoded))
	if err != nil {
		return 0, 0, false, fmt.Errorf("geo: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	body, err := readJSON(l.client, request)
	if err != nil {
		return 0, 0, false, err
	}
	var parsed struct {
		Status  string  `json:"status"`
		Lat     float64 `json:"lat"`
		Lon     float64 `json:"lon"`
		Message string  `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, 0, false, fmt.Errorf("geo: unwired: %w", err)
	}
	if parsed.Status != "ok" || (parsed.Lat == 0 && parsed.Lon == 0) {
		return 0, 0, false, nil
	}
	return parsed.Lat, parsed.Lon, true, nil
}

// here asks the HERE positioning API.
func (l *Locator) here(ctx context.Context, network *model.Network) (float64, float64, bool, error) {
	type cell struct {
		MCC int   `json:"mcc"`
		MNC int   `json:"mnc"`
		LAC int   `json:"lac"`
		CID int64 `json:"cid"`
	}
	type payload struct {
		CellTowers []cell `json:"cellTowers"`
	}
	var request payload
	for _, tower := range network.CellTowers {
		request.CellTowers = append(request.CellTowers, cell{
			MCC: tower.MobileCountryCode,
			MNC: tower.MobileNetworkCode,
			LAC: tower.LocationAreaCode,
			CID: tower.CellID,
		})
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return 0, 0, false, fmt.Errorf("geo: encode here request: %w", err)
	}
	address := l.baseURL + "?apiKey=" + url.QueryEscape(l.key)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, address,
		bytes.NewReader(encoded))
	if err != nil {
		return 0, 0, false, fmt.Errorf("geo: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	body, err := readJSON(l.client, httpRequest)
	if err != nil {
		return 0, 0, false, err
	}
	var parsed struct {
		Location struct {
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
		} `json:"location"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, 0, false, fmt.Errorf("geo: here: %w", err)
	}
	if parsed.Location.Lat == 0 && parsed.Location.Lng == 0 {
		return 0, 0, false, nil
	}
	return parsed.Location.Lat, parsed.Location.Lng, true, nil
}

// universal posts the cell towers to an operator supplied endpoint that speaks
// the Google geolocation protocol, so a self hosted service can replace Google.
func (l *Locator) universal(ctx context.Context, network *model.Network) (float64, float64, bool, error) {
	encoded, err := json.Marshal(newLocatePayload(network, false))
	if err != nil {
		return 0, 0, false, fmt.Errorf("geo: encode cell towers: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		l.baseURL+"?key="+url.QueryEscape(l.key), bytes.NewReader(encoded))
	if err != nil {
		return 0, 0, false, fmt.Errorf("geo: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	body, err := readJSON(l.client, request)
	if err != nil {
		return 0, 0, false, err
	}
	var parsed struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Location struct {
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
		} `json:"location"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, 0, false, fmt.Errorf("geo: universal: %w", err)
	}
	if parsed.Error != nil {
		return 0, 0, false, fmt.Errorf("geo: universal: %s", parsed.Error.Message)
	}
	if parsed.Location.Lat == 0 && parsed.Location.Lng == 0 {
		return 0, 0, false, nil
	}
	return parsed.Location.Lat, parsed.Location.Lng, true, nil
}

func strongestTower(network *model.Network) model.CellTower {
	best := network.CellTowers[0]
	for _, tower := range network.CellTowers[1:] {
		if tower.SignalStrength > best.SignalStrength {
			best = tower
		}
	}
	return best
}

func coordinate(latitude, longitude float64) string {
	return formatCoordinate(latitude) + "," + formatCoordinate(longitude)
}

func formatCoordinate(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}

func joinAddress(parts []string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return strings.Join(cleaned, ", ")
}

// defaultGeocoderURL returns the endpoint of a provider when the operator did
// not set geocoder.url.
func defaultGeocoderURL(provider string) string {
	provider = strings.ToLower(provider)
	if endpoint, ok := extraGeocoderURLs[provider]; ok {
		return endpoint
	}
	switch provider {
	case "google":
		return googleURL
	case "opencage":
		return openCageURL
	case "here":
		return hereGeocodeURL
	case "locationiq":
		return locationIQURL
	case "mapbox":
		return mapboxGeocodeURL
	case "tomtom":
		return tomtomURL
	case "photon":
		return photonURL
	case "pelias":
		return peliasURL
	default:
		return nominatimURL
	}
}
