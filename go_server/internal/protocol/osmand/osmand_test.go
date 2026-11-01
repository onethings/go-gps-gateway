package osmand_test

import (
	"net/url"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/protocol/osmand"
)

type fixedResolver struct {
	sessions map[string]*codec.DeviceSession
}

func (r fixedResolver) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	return r.sessions[uniqueID], nil
}

func base() (codec.Base, fixedResolver) {
	resolver := fixedResolver{sessions: map[string]*codec.DeviceSession{
		"123456789012345": {DeviceID: 7, UniqueID: "123456789012345"},
	}}
	clock := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	return codec.Base{
		Resolver: resolver,
		Now:      func() time.Time { return clock },
	}, resolver
}

func decode(t *testing.T, query string) *model.Position {
	t.Helper()
	settings, _ := base()
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	result := osmand.Decode(settings, values)
	if result == nil || len(result.Positions) != 1 {
		t.Fatalf("result = %+v", result)
	}
	return result.Positions[0]
}

func TestDecodeQueryValues(t *testing.T) {
	position := decode(t,
		"id=123456789012345&lat=54.94535&lon=24.01762&timestamp=1700000000"+
			"&speed=12.5&bearing=90&altitude=120&hdop=0.9&batt=87")

	if position.DeviceID != 7 {
		t.Errorf("device id = %d", position.DeviceID)
	}
	if position.Latitude != 54.94535 || position.Longitude != 24.01762 {
		t.Errorf("coordinates = %v,%v", position.Latitude, position.Longitude)
	}
	if !position.Valid {
		t.Error("position is not valid")
	}
	if position.Speed != 12.5 || position.Course != 90 || position.Altitude != 120 {
		t.Errorf("motion = %+v", position)
	}
	if position.Time.Unix() != 1700000000 {
		t.Errorf("time = %v", position.Time)
	}
	if value, _ := position.Get(model.KeyHDOP); value != 0.9 {
		t.Errorf("hdop = %v", value)
	}
	if value, _ := position.Get(model.KeyBatteryLevel); value != 87.0 {
		t.Errorf("battery = %v", value)
	}
}

func TestDecodeTimestampUnits(t *testing.T) {
	milliseconds := decode(t,
		"id=123456789012345&lat=1&lon=2&timestamp=1700000000123")
	if milliseconds.Time.Unix() != 1700000000 {
		t.Errorf("milliseconds = %v", milliseconds.Time)
	}

	microseconds := decode(t,
		"id=123456789012345&lat=1&lon=2&timestamp=1700000000123456")
	if microseconds.Time.Unix() != 1700000000 {
		t.Errorf("microseconds = %v", microseconds.Time)
	}

	// A missing timestamp falls back to the server clock instead of 1970.
	missing := decode(t, "id=123456789012345&lat=1&lon=2")
	if !missing.Time.Equal(time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("missing timestamp = %v", missing.Time)
	}
}

func TestDecodeNeedsAKnownDevice(t *testing.T) {
	settings, _ := base()
	values, _ := url.ParseQuery("id=nobody&lat=1&lon=2&timestamp=1700000000")
	if result := osmand.Decode(settings, values); result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}

	values, _ = url.ParseQuery("lat=1&lon=2&timestamp=1700000000")
	if result := osmand.Decode(settings, values); result != nil {
		t.Fatalf("result without an id = %+v", result)
	}
}

func TestDecodeWithoutCoordinatesIsInvalid(t *testing.T) {
	position := decode(t, "id=123456789012345&timestamp=1700000000")
	if position.Valid {
		t.Error("position without coordinates is valid")
	}
}
