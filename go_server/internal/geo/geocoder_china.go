package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
)

// autoNavi asks the AMap (Gaode) reverse geocoder, which works in GCJ-02.
func (g *Geocoder) autoNavi(ctx context.Context, latitude, longitude float64) (string, error) {
	point := wgs84ToGCJ02(latitude, longitude)
	query := url.Values{}
	query.Set("output", "json")
	// AutoNavi takes the coordinate as longitude,latitude.
	query.Set("location", coordinate(point.longitude, point.latitude))
	query.Set("extensions", "all")
	query.Set("key", g.key)

	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		Info      string `json:"info"`
		Regeocode *struct {
			FormattedAddress string `json:"formatted_address"`
			AddressComponent struct {
				StreetNumber struct {
					Street string `json:"street"`
				} `json:"streetNumber"`
				Township string `json:"township"`
				District string `json:"district"`
				City     string `json:"city"`
				Province string `json:"province"`
				Country  string `json:"country"`
			} `json:"addressComponent"`
		} `json:"regeocode"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: autonavi: %w", err)
	}
	if parsed.Regeocode == nil {
		return "", fmt.Errorf("geo: autonavi: no address (%s)", parsed.Info)
	}
	component := parsed.Regeocode.AddressComponent
	address := strings.TrimSpace(parsed.Regeocode.FormattedAddress)
	if address == "" {
		address = composeAddress("", component.StreetNumber.Street,
			firstNonEmpty(component.City, component.Township, component.District),
			component.District, component.Province, "", component.Country)
	}
	if address == "" {
		return "", fmt.Errorf("geo: autonavi: no address")
	}
	return address, nil
}

// baidu asks the Baidu reverse geocoder, which expects GCJ-02 coordinates.
func (g *Geocoder) baidu(ctx context.Context, latitude, longitude float64) (string, error) {
	point := wgs84ToGCJ02(latitude, longitude)
	query := url.Values{}
	query.Set("output", "json")
	query.Set("location", coordinate(point.latitude, point.longitude))
	query.Set("coordtype", "gcj02ll")
	query.Set("extensions_poi", "1")
	query.Set("ak", g.key)
	if g.language != "" {
		query.Set("language", g.language)
		query.Set("language_auto", "1")
	}

	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		Message string `json:"message"`
		Result  *struct {
			FormattedAddress string `json:"formatted_address"`
			Pois             []struct {
				Name string `json:"name"`
			} `json:"pois"`
			AddressComponent struct {
				Street   string `json:"street"`
				Town     string `json:"town"`
				District string `json:"district"`
				City     string `json:"city"`
				Province string `json:"province"`
				Country  string `json:"country"`
			} `json:"addressComponent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: baidu: %w", err)
	}
	if parsed.Result == nil {
		return "", fmt.Errorf("geo: baidu: no address (%s)", parsed.Message)
	}
	component := parsed.Result.AddressComponent
	address := strings.TrimSpace(parsed.Result.FormattedAddress)
	if address == "" {
		address = composeAddress("", component.Street, component.City,
			component.District, component.Province, "", component.Country)
	}
	if address == "" {
		return "", fmt.Errorf("geo: baidu: no address")
	}
	return address, nil
}

// tencent asks the Tencent (QQ) maps reverse geocoder, which also works in
// GCJ-02.
func (g *Geocoder) tencent(ctx context.Context, latitude, longitude float64) (string, error) {
	point := wgs84ToGCJ02(latitude, longitude)
	query := url.Values{}
	query.Set("location", coordinate(point.latitude, point.longitude))
	query.Set("get_poi", "1")
	query.Set("output", "json")
	query.Set("key", g.key)

	body, err := g.geocodeRequest(ctx, g.baseURL+"?"+query.Encode())
	if err != nil {
		return "", err
	}
	var parsed struct {
		Message string `json:"message"`
		Result  *struct {
			Address string `json:"address"`
			Pois    []struct {
				Title string `json:"title"`
			} `json:"pois"`
			AddressComponent struct {
				Street   string `json:"street"`
				District string `json:"district"`
				City     string `json:"city"`
				Province string `json:"province"`
				Nation   string `json:"nation"`
			} `json:"address_component"`
			AddressReference struct {
				Town struct {
					Title string `json:"title"`
				} `json:"town"`
			} `json:"address_reference"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("geo: tencent: %w", err)
	}
	if parsed.Result == nil {
		return "", fmt.Errorf("geo: tencent: no address (%s)", parsed.Message)
	}
	component := parsed.Result.AddressComponent
	address := strings.TrimSpace(parsed.Result.Address)
	if address == "" {
		address = composeAddress("", component.Street,
			firstNonEmpty(component.City, parsed.Result.AddressReference.Town.Title),
			component.District, component.Province, "", component.Nation)
	}
	if address == "" {
		return "", fmt.Errorf("geo: tencent: no address")
	}
	return address, nil
}

// point is a geographic coordinate in a specific datum.
type point struct {
	latitude  float64
	longitude float64
}

// wgs84ToGCJ02 shifts a WGS-84 coordinate to the GCJ-02 datum that Chinese map
// providers use. Outside China the offset is still applied, matching the Java
// server.
func wgs84ToGCJ02(latitude, longitude float64) point {
	const (
		radius       = 6378245.0
		correction   = 0.00669342162296594323
		degreesToRad = math.Pi / 180.0
	)

	latitudeOffset := transformLatitude(latitude-35.0, longitude-105.0)
	longitudeOffset := transformLongitude(latitude-35.0, longitude-105.0)

	magic := math.Sin(latitude * degreesToRad)
	magic = 1 - correction*magic*magic
	sqrtMagic := math.Sqrt(magic)

	latitudeOffset = latitudeOffset * 180.0 /
		((radius * (1 - correction)) / (magic * sqrtMagic) * math.Pi)
	longitudeOffset = longitudeOffset * 180.0 /
		(radius / sqrtMagic * math.Cos(latitude*degreesToRad) * math.Pi)

	return point{latitude: latitude + latitudeOffset, longitude: longitude + longitudeOffset}
}

func transformLatitude(latitude, longitude float64) float64 {
	offset := -100.0 + 2.0*longitude + 3.0*latitude +
		0.2*latitude*latitude + 0.1*longitude*latitude +
		0.2*math.Sqrt(math.Abs(longitude))
	offset += (20.0*math.Sin(6.0*longitude*math.Pi) +
		20.0*math.Sin(2.0*longitude*math.Pi)) * 2.0 / 3.0
	offset += (20.0*math.Sin(latitude*math.Pi) +
		40.0*math.Sin(latitude/3.0*math.Pi)) * 2.0 / 3.0
	offset += (160.0*math.Sin(latitude/12.0*math.Pi) +
		320.0*math.Sin(latitude*math.Pi/30.0)) * 2.0 / 3.0
	return offset
}

func transformLongitude(latitude, longitude float64) float64 {
	offset := 300.0 + longitude + 2.0*latitude +
		0.1*longitude*longitude + 0.1*longitude*latitude +
		0.1*math.Sqrt(math.Abs(longitude))
	offset += (20.0*math.Sin(6.0*longitude*math.Pi) +
		20.0*math.Sin(2.0*longitude*math.Pi)) * 2.0 / 3.0
	offset += (20.0*math.Sin(longitude*math.Pi) +
		40.0*math.Sin(longitude/3.0*math.Pi)) * 2.0 / 3.0
	offset += (150.0*math.Sin(longitude/12.0*math.Pi) +
		300.0*math.Sin(longitude/30.0*math.Pi)) * 2.0 / 3.0
	return offset
}
