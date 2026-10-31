package notify

import (
	"strings"
	"testing"

	"traccar-server_go/go_server/internal/model"
)

func TestParseNotificators(t *testing.T) {
	channels, err := ParseNotificators([]string{"email", "webhook", "mail"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Join(channels, ",") != "mail,webhook" {
		t.Errorf("channels = %v", channels)
	}

	channels, err = ParseNotificators([]string{"mail, webhook"})
	if err != nil {
		t.Fatalf("parse combined: %v", err)
	}
	if strings.Join(channels, ",") != "mail,webhook" {
		t.Errorf("combined channels = %v", channels)
	}

	if _, err := ParseNotificators(nil); err == nil {
		t.Error("expected an error for an empty channel list")
	}
	if _, err := ParseNotificators([]string{"sms"}); err == nil {
		t.Error("expected an error for an unknown channel")
	}
}

func TestValidNotificationType(t *testing.T) {
	for _, value := range []string{
		model.AlertOverspeed, model.AlertPowerCut, model.AlertPowerRestored,
		model.AlertGeofenceEnter, model.AlertGeofenceExit, "all", "*",
	} {
		if !ValidNotificationType(value) {
			t.Errorf("%q should be a valid notification type", value)
		}
	}
	for _, value := range []string{"", "none", "overspeed", "deviceOverspeed "} {
		if ValidNotificationType(value) {
			t.Errorf("%q should not be a valid notification type", value)
		}
	}
}
