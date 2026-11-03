package geo

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

func TestGeocoderProvidersParseTheirAnswers(t *testing.T) {
	cases := []struct {
		provider string
		body     string
		want     string
	}{
		{"opencage", `{"results":[{"formatted":"Riga, Latvia"}]}`, "Riga, Latvia"},
		{"here", `{"items":[{"address":{"label":"Riga, Latvia"}}]}`, "Riga, Latvia"},
		{"locationiq", `{"display_name":"Riga, Latvia"}`, "Riga, Latvia"},
		{"mapbox", `{"features":[{"place_name":"Riga, Latvia"}]}`, "Riga, Latvia"},
		{"tomtom", `{"addresses":[{"address":{"freeformAddress":"Riga, Latvia"}}]}`, "Riga, Latvia"},
		{"photon", `{"features":[{"properties":{"name":"Riga","city":"Riga","country":"Latvia"}}]}`,
			"Riga, Riga, Latvia"},
		{"pelias", `{"features":[{"properties":{"label":"Riga, Latvia"}}]}`, "Riga, Latvia"},
		{"autonavi", `{"status":"1","regeocode":{"formatted_address":"Riga, Latvia"}}`, "Riga, Latvia"},
		{"baidu", `{"status":0,"result":{"formatted_address":"Riga, Latvia"}}`, "Riga, Latvia"},
		{"ban", `{"features":[{"properties":{"label":"Paris, France","countrycode":"fr"}}]}`, "Paris, France"},
		{"bingmaps", `{"resourceSets":[{"resources":[{"address":{"formattedAddress":"Riga, Latvia"}}]}]}`,
			"Riga, Latvia"},
		{"factual", `{"response":{"data":{"street_name":{"name":"Brivibas iela"},
			"locality":{"name":"Riga"},"country":{"name":"Latvia"}}}}`, "Brivibas iela, Riga, Latvia"},
		{"geoapify", `{"results":[{"formatted":"Riga, Latvia"}]}`, "Riga, Latvia"},
		{"geocodefarm", `{"RESULTS":{"result":{"0":{"formatted_address":"Riga, Latvia"}}}}`, "Riga, Latvia"},
		{"geocodejson", `{"features":[{"properties":{"label":"Riga, Latvia"}}]}`, "Riga, Latvia"},
		{"geocodexyz", `{"stnumber":"1","staddress":"Brivibas iela","city":"Riga",
			"region":"Riga","prov":"Latvia","postal":"1050"}`, "1 Brivibas iela, Riga, Riga, 1050, Latvia"},
		{"gisgraphy", `{"result":[{"formatedFull":"Riga, Latvia"}]}`, "Riga, Latvia"},
		{"mapquest", `{"results":[{"locations":[{"street":"Brivibas iela","adminArea5":"Riga",
			"adminArea3":"Riga","adminArea1":"LV"}]}]}`, "Brivibas iela, Riga, Riga, LV"},
		{"maptiler", `{"features":[{"place_name":"Riga, Latvia","text":"Riga","place_type":["place"]}]}`,
			"Riga, Latvia"},
		{"mapmyindia", `{"results":[{"formatted_address":"Riga, Latvia"}]}`, "Riga, Latvia"},
		{"mygeocode", `{"result":{"formatted":"Riga, Latvia"}}`, "Riga, Latvia"},
		{"positionstack", `{"data":[{"label":"Riga, Latvia"}]}`, "Riga, Latvia"},
		{"tencent", `{"result":{"address":"Riga, Latvia"}}`, "Riga, Latvia"},
	}
	for _, test := range cases {
		settings := testSettings()
		settings.Geocoder.Type = test.provider
		settings.Geocoder.URL = "http://provider.test/reverse"
		settings.Geocoder.Key = "key"
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return respond(http.StatusOK, test.body), nil
		})}
		address, err := NewGeocoder(settings, client).Address(context.Background(), 56.9, 24.1)
		if err != nil {
			t.Errorf("%s: %v", test.provider, err)
			continue
		}
		if address != test.want {
			t.Errorf("%s = %q, want %q", test.provider, address, test.want)
		}
	}
}

func TestGeocoderRejectsUnknownProvider(t *testing.T) {
	settings := testSettings()
	settings.Geocoder.Type = "nope"
	settings.Geocoder.URL = "http://provider.test/reverse"
	_, err := NewGeocoder(settings, &http.Client{Transport: roundTripFunc(
		func(*http.Request) (*http.Response, error) {
			return respond(http.StatusOK, `{}`), nil
		})}).Address(context.Background(), 1, 2)
	if err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
}

func TestGeolocationProviders(t *testing.T) {
	network := model.NewNetworkWith(
		model.NewCellTower(250, 1, 7, 12345),
		model.NewCellTower(250, 1, 7, 999),
	)
	cases := []struct {
		provider string
		body     string
	}{
		{"opencellid", `{"lat":56.9,"lon":24.1}`},
		{"unwired", `{"status":"ok","lat":56.9,"lon":24.1}`},
		{"here", `{"location":{"lat":56.9,"lng":24.1}}`},
		{"mozilla", `{"location":{"lat":56.9,"lng":24.1}}`},
		{"universal", `{"location":{"lat":56.9,"lng":24.1}}`},
	}
	for _, test := range cases {
		settings := testSettings()
		settings.Geolocation.Type = test.provider
		settings.Geolocation.URL = "http://locate.test"
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return respond(http.StatusOK, test.body), nil
		})}
		latitude, longitude, ok := NewLocator(settings, client).Locate(context.Background(), network)
		if !ok || latitude != 56.9 || longitude != 24.1 {
			t.Errorf("%s = %v,%v ok=%v", test.provider, latitude, longitude, ok)
		}
	}
}

func TestUnwiredSendsTheCellTowers(t *testing.T) {
	network := model.NewNetworkWith(model.NewCellTower(250, 1, 7, 12345))
	var payload struct {
		Token string `json:"token"`
		MCC   int    `json:"mcc"`
		Cells []struct {
			LAC int   `json:"lac"`
			CID int64 `json:"cid"`
		} `json:"cells"`
	}
	settings := testSettings()
	settings.Geolocation.Type = "unwired"
	settings.Geolocation.URL = "http://unwired.test"
	settings.Geolocation.Cache = time.Hour
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		return respond(http.StatusOK, `{"status":"ok","lat":1,"lon":2}`), nil
	})}
	NewLocator(settings, client).Locate(context.Background(), network)
	if payload.Token != "key" || payload.MCC != 250 || len(payload.Cells) != 1 ||
		payload.Cells[0].CID != 12345 {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestGeocoderProviderRequestFormat(t *testing.T) {
	cases := []struct {
		provider string
		base     string
		path     string
		query    string
	}{
		{"bingmaps", "http://bing.test/v1", "/v1/Locations/56.900000,24.100000", "key=key"},
		{"maptiler", "http://maptiler.test/geocoding", "/geocoding/24.100000,56.900000.json", "key=key"},
		{"mapmyindia", "http://mappls.test/v1", "/v1/key/rev_geocode",
			"lat=56.900000&lng=24.100000"},
		{"geocodexyz", "http://xyz.test", "/56.900000,24.100000", "geoit=JSON"},
		{"mapquest", "http://mapquest.test/reverse", "/reverse",
			"key=key&location=56.900000%2C24.100000"},
		{"autonavi", "http://amap.test/regeo", "/regeo",
			"location=24.108520%2C56.898416"},
	}
	for _, test := range cases {
		var captured string
		settings := testSettings()
		settings.Geocoder.Type = test.provider
		settings.Geocoder.URL = test.base
		settings.Geocoder.Key = "key"
		client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			captured = request.URL.Path + "?" + request.URL.RawQuery
			return respond(http.StatusOK, `{}`), nil
		})}
		// The body is intentionally unusable; only the request matters here.
		_, _ = NewGeocoder(settings, client).Address(context.Background(), 56.9, 24.1)
		if !strings.HasPrefix(captured, test.path+"?") {
			t.Errorf("%s path = %q, want %q", test.provider, captured, test.path+"?...")
		}
		if !strings.Contains(captured, test.query) {
			t.Errorf("%s query = %q, want it to contain %q", test.provider, captured, test.query)
		}
	}
}

func TestPlusCodeIsComputedLocally(t *testing.T) {
	settings := testSettings()
	settings.Geocoder.Type = "pluscodes"
	requests := 0
	geocoder := NewGeocoder(settings, &http.Client{Transport: roundTripFunc(
		func(*http.Request) (*http.Response, error) {
			requests++
			return respond(http.StatusOK, `{}`), nil
		})})
	geocoder.OnRequest = func() { requests++ }

	address, err := geocoder.Address(context.Background(), 47.365590, 8.524997)
	if err != nil {
		t.Fatal(err)
	}
	if address != "8FVC9G8F+6X" {
		t.Errorf("plus code = %q, want 8FVC9G8F+6X", address)
	}
	if requests != 0 {
		t.Errorf("plus code made %d requests, want 0", requests)
	}
}

func TestWGS84ToGCJ02(t *testing.T) {
	shifted := wgs84ToGCJ02(39.9042, 116.4074)
	if math.Abs(shifted.latitude-39.90560334316507) > 1e-9 ||
		math.Abs(shifted.longitude-116.41364225378803) > 1e-9 {
		t.Fatalf("gcj02 = %v, want 39.90560334316507, 116.41364225378803", shifted)
	}
}

func TestUniversalGeolocationPostsCellTowers(t *testing.T) {
	network := model.NewNetworkWith(
		model.NewCellTower(250, 1, 7, 12345),
		model.NewCellTower(250, 1, 7, 999),
	)
	var payload locatePayload
	var method, key string
	settings := testSettings()
	settings.Geolocation.Type = "universal"
	settings.Geolocation.URL = "http://universal.test/locate"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		method = request.Method
		key = request.URL.Query().Get("key")
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		return respond(http.StatusOK, `{"location":{"lat":56.9,"lng":24.1},"accuracy":50}`), nil
	})}

	latitude, longitude, ok := NewLocator(settings, client).Locate(context.Background(), network)
	if !ok || latitude != 56.9 || longitude != 24.1 {
		t.Fatalf("locate = %v,%v ok=%v", latitude, longitude, ok)
	}
	if method != http.MethodPost || key != "key" {
		t.Errorf("request = %s key=%q", method, key)
	}
	if len(payload.CellTowers) != 2 || payload.CellTowers[1].CellID != 999 {
		t.Errorf("payload = %+v", payload)
	}
}

func TestUniversalGeolocationReportsErrors(t *testing.T) {
	network := model.NewNetworkWith(model.NewCellTower(250, 1, 7, 12345))
	settings := testSettings()
	settings.Geolocation.Type = "universal"
	settings.Geolocation.URL = "http://universal.test/locate"
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusOK, `{"error":{"message":"not found"}}`), nil
	})}
	if _, _, ok := NewLocator(settings, client).Locate(context.Background(), network); ok {
		t.Fatal("an error answer must not produce a position")
	}
}

func TestGeocoderKeyRequirement(t *testing.T) {
	keyless := []string{"", "nominatim", "photon", "pluscodes", "ban", "geocodejson"}
	for _, provider := range keyless {
		if geocoderNeedsKey(provider) {
			t.Errorf("%q must work without a key", provider)
		}
	}
	for _, provider := range []string{"google", "opencage", "mapbox", "autonavi", "baidu"} {
		if !geocoderNeedsKey(provider) {
			t.Errorf("%q must require a key", provider)
		}
	}
}
