package main

import (
	"log"
	"path/filepath"
	"strings"

	"traccar-server_go/go_server/internal/api"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/geo"
	"traccar-server_go/go_server/internal/ldap"
)

// deviceImagePath is where the per-device images live. The explicit key wins;
// otherwise the media directory gets a devices subdirectory so one volume holds
// every upload.
func deviceImagePath(cfg *config.Config, mediaDirectory string) string {
	if cfg == nil {
		return ""
	}
	if directory := cfg.String(config.DeviceImagePath); directory != "" {
		return directory
	}
	if mediaDirectory != "" {
		return filepath.Join(mediaDirectory, "devices")
	}
	return ""
}

// readOpenIDSettings reads the OpenID Connect keys. A configuration without an
// issuer or a client id stays disabled instead of failing every login.
func readOpenIDSettings(cfg *config.Config) api.OpenIDSettings {
	settings := api.OpenIDSettings{}
	if cfg == nil {
		return settings
	}
	enabled, err := cfg.Bool(config.OpenIDEnable)
	if err != nil || !enabled {
		return settings
	}
	settings.Enabled = true
	settings.IssuerURL = cfg.String(config.OpenIDIssuer)
	settings.ClientID = cfg.String(config.OpenIDClientID)
	settings.ClientSecret = cfg.String(config.OpenIDClientSecret)
	settings.RedirectURL = cfg.String(config.OpenIDRedirectURL)
	settings.SuccessURL = cfg.StringOr(config.OpenIDSuccessURL, "/")
	settings.ButtonName = cfg.String(config.OpenIDButtonName)
	if createMissing, err := cfg.Bool(config.OpenIDCreateMissing); err == nil {
		settings.CreateMissing = createMissing
	}
	for _, scope := range strings.Split(cfg.StringOr(config.OpenIDScopes, "openid,email,profile"), ",") {
		if trimmed := strings.TrimSpace(scope); trimmed != "" {
			settings.Scopes = append(settings.Scopes, trimmed)
		}
	}
	if settings.IssuerURL == "" || settings.ClientID == "" {
		log.Printf("api: openid.enable is set but openid.issuer or openid.clientId is missing; OpenID stays disabled")
		return api.OpenIDSettings{}
	}
	return settings
}

// readCopyAttributes reads the comma separated attribute keys the position
// stage copies from the previous position. An empty list turns the handler off.
func readCopyAttributes(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	var keys []string
	for _, key := range strings.Split(cfg.String(config.PositionCopyAttributes), ",") {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			keys = append(keys, trimmed)
		}
	}
	return keys
}

// readLDAPConfig reads the directory settings. A configuration without a URL or
// a base stays disabled, so the login never reaches for an unconfigured server.
func readLDAPConfig(cfg *config.Config) (ldap.Authenticator, bool) {
	if cfg == nil {
		return ldap.Authenticator{}, false
	}
	address := cfg.String(config.LDAPURL)
	base := cfg.String(config.LDAPBase)
	if address == "" || base == "" {
		if address != "" || base != "" {
			log.Print("api: ldap.url and ldap.base are both required; LDAP stays disabled")
		}
		return ldap.Authenticator{}, false
	}
	timeout, _ := cfg.Duration(config.LDAPTimeout)
	return ldap.Authenticator{Config: ldap.Config{
		URL:           address,
		BindDN:        cfg.String(config.LDAPUser),
		BindPassword:  cfg.String(config.LDAPPassword),
		BaseDN:        base,
		UserFilter:    cfg.String(config.LDAPUserFilter),
		NameAttribute: cfg.String(config.LDAPNameAttribute),
		MailAttribute: cfg.String(config.LDAPMailAttribute),
		Timeout:       timeout,
	}}, true
}

// geoCacheList publishes the cache sizes of the services that are enabled.
func geoCacheList(geocoder *geo.Geocoder, locator *geo.Locator,
	speedLimit *geo.SpeedLimits, mapMatcher *geo.Matcher) []api.NamedCache {
	var caches []api.NamedCache
	if geocoder != nil {
		caches = append(caches, api.NamedCache{Name: "geocoder", Size: geocoder.Size})
	}
	if locator != nil {
		caches = append(caches, api.NamedCache{Name: "geolocation", Size: locator.Size})
	}
	if speedLimit != nil {
		caches = append(caches, api.NamedCache{Name: "speedLimit", Size: speedLimit.Size})
	}
	if mapMatcher != nil {
		caches = append(caches, api.NamedCache{Name: "mapMatcher", Size: mapMatcher.Size})
	}
	return caches
}
