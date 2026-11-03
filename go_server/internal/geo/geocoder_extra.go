package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Endpoints of the remaining geocoders. As with the providers in providers.go,
// the operator can override any of them with geocoder.url. Providers that build
// their path from the configuration, such as Bing Maps or MapmyIndia, treat
// geocoder.url as the API root instead of a complete endpoint.
const (
	autoNaviURL      = "https://restapi.amap.com/v3/geocode/regeo"
	baiduURL         = "https://api.map.baidu.com/reverse_geocoding/v3/"
	banURL           = "https://data.geopf.fr/geocodage/reverse"
	bingMapsURL      = "https://dev.virtualearth.net/REST/v1"
	factualURL       = "https://api.factual.com/geotag"
	geoapifyURL      = "https://api.geoapify.com/v1/geocode/reverse"
	geocodeFarmURL   = "https://api.geocode.farm/reverse"
	geocodeXyzURL    = "https://geocode.xyz"
	gisgraphyURL     = "http://services.gisgraphy.com/reversegeocoding/search"
	mapQuestURL      = "http://www.mapquestapi.com/geocoding/v1/reverse"
	mapTilerURL      = "https://api.maptiler.com/geocoding"
	mapmyIndiaURL    = "https://apis.mappls.com/advancedmaps/v1"
	myGeocodeURL     = "https://api.mygeocode.com/v1/reverse"
	positionStackURL = "http://api.positionstack.com/v1/reverse"
	tencentURL       = "https://apis.map.qq.com/ws/geocoder/v1/"
)

// extraGeocoderURLs is consulted by defaultGeocoderURL. Plus codes are computed
// locally, so they have no endpoint.
var extraGeocoderURLs = map[string]string{
	"autonavi":      autoNaviURL,
	"baidu":         baiduURL,
	"ban":           banURL,
	"bingmaps":      bingMapsURL,
	"factual":       factualURL,
	"geoapify":      geoapifyURL,
	"geocodefarm":   geocodeFarmURL,
	"geocodejson":   photonURL,
	"geocodexyz":    geocodeXyzURL,
	"gisgraphy":     gisgraphyURL,
	"mapquest":      mapQuestURL,
	"maptiler":      mapTilerURL,
	"mapmyindia":    mapmyIndiaURL,
	"mygeocode":     myGeocodeURL,
	"positionstack": positionStackURL,
	"tencent":       tencentURL,
}

// bingMaps asks the Bing Maps locations API.
func (g *Geocoder) bingMaps(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("key", g.key)
	query.Set("include", "ciso2")

	address := g.baseURL + "/Locations/" + coordinate(latitude, longitude) + "?" + query.Encode()
	body, err := g.geocodeRequest(ctx, address)
	if err != nil {
		return "", err
	}
	var parsed struct {
		ResourceSets []struct {
			Resources []struct {
				Address struct {
					FormattedAddress string `json:"formattedAddress"`
					AddressLine      string `json:"addressLine"`
					Locality         string `json:"locality"`
					AdminDistrict2   string `json:"adminDistrict2"`
					AdminDistrict    string `json:"adminDistrict"`
					CountryRegion    string `json:"countryRegionIso2"`
					PostalCode       string `json:"postalCode"`
				} `json:"address"`
			} `json:"resources"`
		} `json:"resourceSets"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: bingmaps: %w", err)
	}
	if len(parsed.ResourceSets) == 0 || len(parsed.ResourceSets[0].Resources) == 0 {
		return "", fmt.Errorf("geo: bingmaps: no address")
	}
	result := parsed.ResourceSets[0].Resources[0].Address
	address = strings.TrimSpace(result.FormattedAddress)
	if address == "" {
		address = composeAddress("", result.AddressLine, result.Locality,
			result.AdminDistrict2, result.AdminDistrict, result.PostalCode, result.CountryRegion)
	}
	if address == "" {
		return "", fmt.Errorf("geo: bingmaps: no address")
	}
	return address, nil
}

// mapQuest asks the MapQuest reverse geocoder.
func (g *Geocoder) mapQuest(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("key", g.key)
	query.Set("location", coordinate(latitude, longitude))

	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		Results []struct {
			Locations []struct {
				Street     string `json:"street"`
				Settlement string `json:"adminArea5"`
				District   string `json:"adminArea4"`
				State      string `json:"adminArea3"`
				Country    string `json:"adminArea1"`
				PostalCode string `json:"postalCode"`
			} `json:"locations"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: mapquest: %w", err)
	}
	if len(parsed.Results) == 0 || len(parsed.Results[0].Locations) == 0 {
		return "", fmt.Errorf("geo: mapquest: no address")
	}
	location := parsed.Results[0].Locations[0]
	address := composeAddress("", location.Street, location.Settlement, location.District,
		location.State, location.PostalCode, location.Country)
	if address == "" {
		return "", fmt.Errorf("geo: mapquest: no address")
	}
	return address, nil
}

// gisgraphy asks the Gisgraphy reverse geocoder.
func (g *Geocoder) gisgraphy(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("format", "json")
	query.Set("lat", formatCoordinate(latitude))
	query.Set("lng", formatCoordinate(longitude))
	query.Set("from", "1")
	query.Set("to", "1")

	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		Result []struct {
			StreetName string `json:"streetName"`
			City       string `json:"city"`
			State      string `json:"state"`
			Country    string `json:"countryCode"`
			// The API spells it with one "t".
			Formatted string `json:"formatedFull"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: gisgraphy: %w", err)
	}
	if len(parsed.Result) == 0 {
		return "", fmt.Errorf("geo: gisgraphy: no address")
	}
	result := parsed.Result[0]
	address := strings.TrimSpace(result.Formatted)
	if address == "" {
		address = composeAddress("", result.StreetName, result.City, "", result.State, "", result.Country)
	}
	if address == "" {
		return "", fmt.Errorf("geo: gisgraphy: no address")
	}
	return address, nil
}

// positionStack asks the PositionStack reverse geocoder.
func (g *Geocoder) positionStack(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("access_key", g.key)
	query.Set("query", coordinate(latitude, longitude))

	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		Data []struct {
			Label      string `json:"label"`
			Number     string `json:"number"`
			Street     string `json:"street"`
			Suburb     string `json:"neighbourhood"`
			Settlement string `json:"locality"`
			State      string `json:"region"`
			Country    string `json:"country_code"`
			PostalCode string `json:"postal_code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: positionstack: %w", err)
	}
	if len(parsed.Data) == 0 {
		return "", fmt.Errorf("geo: positionstack: no address")
	}
	result := parsed.Data[0]
	address := strings.TrimSpace(result.Label)
	if address == "" {
		address = composeAddress(result.Number, result.Street, result.Settlement,
			result.Suburb, result.State, result.PostalCode, result.Country)
	}
	if address == "" {
		return "", fmt.Errorf("geo: positionstack: no address")
	}
	return address, nil
}

// geocodeFarm asks the Geocode.Farm reverse geocoder.
func (g *Geocoder) geocodeFarm(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("lat", formatCoordinate(latitude))
	query.Set("lon", formatCoordinate(longitude))
	if g.key != "" {
		query.Set("key", g.key)
	}
	if g.language != "" {
		query.Set("lang", g.language)
	}

	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		Results struct {
			Result map[string]struct {
				FormattedAddress string `json:"formatted_address"`
				HouseNumber      string `json:"house_number"`
				StreetName       string `json:"street_name"`
				Locality         string `json:"locality"`
				Admin1           string `json:"admin_1"`
				Country          string `json:"country"`
				PostalCode       string `json:"postal_code"`
			} `json:"result"`
		} `json:"RESULTS"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: geocodefarm: %w", err)
	}
	result, ok := parsed.Results.Result["0"]
	if !ok {
		return "", fmt.Errorf("geo: geocodefarm: no address")
	}
	address := strings.TrimSpace(result.FormattedAddress)
	if address == "" {
		address = composeAddress(result.HouseNumber, result.StreetName, result.Locality,
			"", result.Admin1, result.PostalCode, result.Country)
	}
	if address == "" {
		return "", fmt.Errorf("geo: geocodefarm: no address")
	}
	return address, nil
}

// geocodeXyz asks the geocode.xyz service.
func (g *Geocoder) geocodeXyz(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("geoit", "JSON")
	if g.key != "" {
		query.Set("key", g.key)
	}

	address := g.baseURL + "/" + coordinate(latitude, longitude) + "?" + query.Encode()
	body, err := g.geocodeRequest(ctx, address)
	if err != nil {
		return "", err
	}
	var parsed struct {
		House      string `json:"stnumber"`
		Street     string `json:"staddress"`
		City       string `json:"city"`
		State      string `json:"region"`
		Country    string `json:"prov"`
		PostalCode string `json:"postal"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: geocodexyz: %w", err)
	}
	address = composeAddress(parsed.House, parsed.Street, parsed.City, "", parsed.State,
		parsed.PostalCode, parsed.Country)
	if address == "" {
		return "", fmt.Errorf("geo: geocodexyz: no address")
	}
	return address, nil
}

// geoapify asks the Geoapify reverse geocoder.
func (g *Geocoder) geoapify(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("format", "json")
	query.Set("lat", formatCoordinate(latitude))
	query.Set("lon", formatCoordinate(longitude))
	query.Set("apiKey", g.key)
	if g.language != "" {
		query.Set("lang", g.language)
	}

	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		Results []struct {
			Formatted  string `json:"formatted"`
			House      string `json:"housenumber"`
			Street     string `json:"street"`
			Suburb     string `json:"suburb"`
			Settlement string `json:"city"`
			District   string `json:"district"`
			State      string `json:"state"`
			Country    string `json:"country_code"`
			PostalCode string `json:"postcode"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: geoapify: %w", err)
	}
	if len(parsed.Results) == 0 {
		return "", fmt.Errorf("geo: geoapify: no address")
	}
	result := parsed.Results[0]
	address := strings.TrimSpace(result.Formatted)
	if address == "" {
		address = composeAddress(result.House, result.Street, result.Settlement,
			firstNonEmpty(result.District, result.Suburb), result.State, result.PostalCode, result.Country)
	}
	if address == "" {
		return "", fmt.Errorf("geo: geoapify: no address")
	}
	return address, nil
}

// geocodeJSON reads the GeoJSON reverse format used by Photon, the French BAN
// service and other Pelias style deployments.
func (g *Geocoder) geocodeJSON(ctx context.Context, latitude, longitude float64, country string) (string, error) {
	query := url.Values{}
	query.Set("lat", formatCoordinate(latitude))
	query.Set("lon", formatCoordinate(longitude))
	if g.key != "" {
		query.Set("key", g.key)
	}
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
				Label       string `json:"label"`
				House       string `json:"housenumber"`
				Street      string `json:"street"`
				Settlement  string `json:"city"`
				District    string `json:"district"`
				State       string `json:"state"`
				CountryCode string `json:"countrycode"`
				PostalCode  string `json:"postcode"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: geocodejson: %w", err)
	}
	if len(parsed.Features) == 0 {
		return "", fmt.Errorf("geo: geocodejson: no address")
	}
	properties := parsed.Features[0].Properties
	if country == "" {
		country = strings.ToUpper(properties.CountryCode)
	}
	address := strings.TrimSpace(properties.Label)
	if address == "" {
		address = composeAddress(properties.House, properties.Street, properties.Settlement,
			properties.District, properties.State, properties.PostalCode, country)
	}
	if address == "" {
		return "", fmt.Errorf("geo: geocodejson: no address")
	}
	return address, nil
}

// mapTiler asks the MapTiler geocoding API.
func (g *Geocoder) mapTiler(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("key", g.key)

	address := g.baseURL + "/" + formatCoordinate(longitude) + "," +
		formatCoordinate(latitude) + ".json?" + query.Encode()
	body, err := g.geocodeRequest(ctx, address)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Features []struct {
			PlaceName string `json:"place_name"`
			Text      string `json:"text"`
		} `json:"features"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: maptiler: %w", err)
	}
	if len(parsed.Features) == 0 {
		return "", fmt.Errorf("geo: maptiler: no address")
	}
	address = strings.TrimSpace(parsed.Features[0].PlaceName)
	if address == "" {
		address = strings.TrimSpace(parsed.Features[0].Text)
	}
	if address == "" {
		return "", fmt.Errorf("geo: maptiler: no address")
	}
	return address, nil
}

// mapmyIndia asks the MapmyIndia (Mappls) reverse geocoder. The key is part of
// the path, not a query parameter.
func (g *Geocoder) mapmyIndia(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("lat", formatCoordinate(latitude))
	query.Set("lng", formatCoordinate(longitude))

	address := g.baseURL + "/" + url.PathEscape(g.key) + "/rev_geocode?" + query.Encode()
	body, err := g.geocodeRequest(ctx, address)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Results []struct {
			FormattedAddress string `json:"formatted_address"`
			HouseNumber      string `json:"house_number"`
			HouseName        string `json:"house_name"`
			Street           string `json:"street"`
			Locality         string `json:"locality"`
			SubLocality      string `json:"sublocality"`
			SubSubLocality   string `json:"subsublocality"`
			City             string `json:"city"`
			Village          string `json:"village"`
			District         string `json:"district"`
			SubDistrict      string `json:"subDistrict"`
			State            string `json:"state"`
			Pincode          string `json:"pincode"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: mapmyindia: %w", err)
	}
	if len(parsed.Results) == 0 {
		return "", fmt.Errorf("geo: mapmyindia: no address")
	}
	result := parsed.Results[0]
	address = strings.TrimSpace(result.FormattedAddress)
	if address == "" {
		address = composeAddress(firstNonEmpty(result.HouseNumber, result.HouseName), result.Street,
			firstNonEmpty(result.City, result.Village),
			firstNonEmpty(result.District, result.SubDistrict, result.Locality,
				result.SubLocality, result.SubSubLocality),
			result.State, result.Pincode, "")
	}
	if address == "" {
		return "", fmt.Errorf("geo: mapmyindia: no address")
	}
	return address, nil
}

// myGeocode asks the MyGeocode reverse geocoder.
func (g *Geocoder) myGeocode(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("lat", formatCoordinate(latitude))
	query.Set("lon", formatCoordinate(longitude))
	if g.key != "" {
		query.Set("key", g.key)
	}
	if g.language != "" {
		query.Set("lang", g.language)
	}

	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		Result *struct {
			Formatted  string `json:"formatted"`
			Components struct {
				House      string `json:"house_number"`
				Street     string `json:"road"`
				Suburb     string `json:"suburb"`
				Settlement string `json:"city"`
				District   string `json:"county"`
				State      string `json:"state"`
				PostalCode string `json:"postcode"`
				Country    string `json:"country_code"`
			} `json:"components"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: mygeocode: %w", err)
	}
	if parsed.Result == nil {
		return "", fmt.Errorf("geo: mygeocode: no address")
	}
	components := parsed.Result.Components
	address := strings.TrimSpace(parsed.Result.Formatted)
	if address == "" {
		address = composeAddress(components.House, components.Street, components.Settlement,
			firstNonEmpty(components.District, components.Suburb), components.State,
			components.PostalCode, strings.ToUpper(components.Country))
	}
	if address == "" {
		return "", fmt.Errorf("geo: mygeocode: no address")
	}
	return address, nil
}

// factual asks the Factual geotag API.
func (g *Geocoder) factual(ctx context.Context, latitude, longitude float64) (string, error) {
	query := url.Values{}
	query.Set("latitude", formatCoordinate(latitude))
	query.Set("longitude", formatCoordinate(longitude))
	query.Set("KEY", g.key)

	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	type place struct {
		Name string `json:"name"`
	}
	var parsed struct {
		Response struct {
			Data *struct {
				HouseNumber *place `json:"street_number"`
				StreetName  *place `json:"street_name"`
				Locality    *place `json:"locality"`
				County      *place `json:"county"`
				Region      *place `json:"region"`
				Country     *place `json:"country"`
				Postcode    *place `json:"postcode"`
			} `json:"data"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: factual: %w", err)
	}
	data := parsed.Response.Data
	if data == nil {
		return "", fmt.Errorf("geo: factual: no address")
	}
	name := func(value *place) string {
		if value == nil {
			return ""
		}
		return value.Name
	}
	address := composeAddress(name(data.HouseNumber), name(data.StreetName), name(data.Locality),
		name(data.County), name(data.Region), name(data.Postcode), name(data.Country))
	if address == "" {
		return "", fmt.Errorf("geo: factual: no address")
	}
	return address, nil
}

// geocoderNeedsKey reports whether a provider cannot answer without
// geocoder.key. The OpenStreetMap style endpoints and the local plus code
// encoder work without one.
func geocoderNeedsKey(provider string) bool {
	switch strings.ToLower(provider) {
	case "", "nominatim", "photon", "pluscodes", "ban", "geocodejson":
		return false
	}
	return true
}

// composeAddress renders the default address order: house and street first,
// then the administrative levels from the smallest to the largest.
func composeAddress(house, street, settlement, district, state, postcode, country string) string {
	switch {
	case house != "" && street != "":
		street = house + " " + street
	case street == "":
		street = house
	}
	return joinAddress([]string{street, settlement, district, state, postcode, country})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
