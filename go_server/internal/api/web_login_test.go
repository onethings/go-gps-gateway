package api

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

var assetReference = regexp.MustCompile(`(?:src|href)="\./(assets/[^"]+)"`)

// TestBundledWebServesLoginAndPositions runs the same-origin path a browser
// takes: the bundled build answers the homepage, login mints a credential on
// that origin, and the device with its last position is readable with it.
func TestBundledWebServesLoginAndPositions(t *testing.T) {
	root := filepath.Join("..", "..", "web")
	if _, err := os.Stat(filepath.Join(root, "index.html")); err != nil {
		t.Skipf("no bundled web build: %v", err)
	}

	users := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, users, 1)
	devices := &fakeDeviceList{results: []model.Device{{
		ID: 7, Name: "van", UniqueID: "864717003283581",
	}}}
	positions := &fakePositions{results: []model.Position{{
		ID:         11,
		DeviceID:   7,
		Time:       time.Date(2026, 10, 31, 13, 2, 10, 0, time.UTC),
		Valid:      true,
		Latitude:   25.033,
		Longitude:  121.565,
		Speed:      42,
		Attributes: map[string]any{"sat": 9},
	}}}

	handler := NewServer(nil,
		WithUsers(users),
		WithDevices(devices),
		WithPositions(positions),
		WithWebPath(root),
		WithAPIKey("shared-key"),
		WithTokens(fakeTokens{tokens: map[string]*auth.Principal{
			auth.HashToken("trc_fake_plaintext"): auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeAll}, []int64{7}),
		}}),
	).Handler()

	home := get(t, handler, "/")
	if home.Code != http.StatusOK {
		t.Fatalf("homepage status = %d (body %s)", home.Code, home.Body)
	}
	if got := home.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("homepage content type = %q", got)
	}
	document := home.Body.String()
	if !strings.Contains(document, `<div id="root"`) {
		t.Fatal("the homepage is not the traccar-web document")
	}
	if strings.Contains(document, "${title}") || strings.Contains(document, "${description}") {
		t.Fatal("the server left the branding placeholders in the document")
	}

	asset := assetReference.FindStringSubmatch(document)
	if asset == nil {
		t.Fatal("the document references no build asset")
	}
	served := get(t, handler, "/"+asset[1])
	if served.Code != http.StatusOK || served.Body.Len() == 0 {
		t.Fatalf("asset %s: status %d, %d bytes", asset[1], served.Code, served.Body.Len())
	}

	login := apiRequest(t, handler, http.MethodPost, "/api/login",
		`{"email":"`+testEmail+`","password":"`+testPassword+`"}`, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d (body %s)", login.Code, login.Body)
	}
	issued := decodeBody[struct {
		Token string `json:"token"`
	}](t, login)
	if issued.Token == "" {
		t.Fatal("login issued no token")
	}
	headers := map[string]string{"Authorization": "Bearer " + issued.Token}

	list := getWith(t, handler, "/api/devices", headers)
	if list.Code != http.StatusOK {
		t.Fatalf("devices status = %d (body %s)", list.Code, list.Body)
	}
	if !strings.Contains(list.Body.String(), `"van"`) {
		t.Fatalf("the device list does not carry the vehicle: %s", list.Body)
	}

	history := getWith(t, handler, "/api/positions?deviceId=7", headers)
	if history.Code != http.StatusOK {
		t.Fatalf("positions status = %d (body %s)", history.Code, history.Body)
	}
	if !strings.Contains(history.Body.String(), `"latitude":25.033`) {
		t.Fatalf("the position the map draws is missing: %s", history.Body)
	}
}
