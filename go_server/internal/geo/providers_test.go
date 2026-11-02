package geo

import (
	"context"
	"encoding/json"
	"net/http"
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
