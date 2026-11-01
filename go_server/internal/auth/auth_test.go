package auth

import (
	"context"
	"strings"
	"testing"
)

func TestParseScopes(t *testing.T) {
	scopes, err := ParseScopes(" read:positions , write:commands ,read:positions,, ")
	if err != nil {
		t.Fatalf("ParseScopes: %v", err)
	}
	if len(scopes) != 2 || scopes[0] != ScopeReadPositions || scopes[1] != ScopeWriteCommands {
		t.Fatalf("scopes = %v", scopes)
	}

	if scopes, err := ParseScopes("  "); err != nil || scopes != nil {
		t.Errorf("empty = %v, err = %v", scopes, err)
	}
	if _, err := ParseScopes("read:everything"); err == nil {
		t.Error("unknown scope was accepted")
	}
}

// Every scope a route checks must be a known scope, otherwise the token that
// carries it cannot be issued in the first place.
func TestPermissionScopesParse(t *testing.T) {
	raw := string(ScopeReadPermissions) + "," + string(ScopeWritePermissions) + "," + string(ScopeWriteShare)
	scopes, err := ParseScopes(raw)
	if err != nil {
		t.Fatalf("ParseScopes(%q): %v", raw, err)
	}
	if len(scopes) != 3 {
		t.Fatalf("scopes = %v, want 3", scopes)
	}

	principal := NewPrincipal(1, false, scopes, nil)
	for _, scope := range scopes {
		if !principal.Has(scope) {
			t.Errorf("principal does not hold %q", scope)
		}
	}
	if principal.Has(ScopeWriteDevices) {
		t.Error("a permission token must not carry write:devices")
	}
}

// The resource routes carry one read/write pair per type; all of them have to
// survive ParseScopes so -token-scopes can issue them.
func TestResourceScopesParse(t *testing.T) {
	scopes := []Scope{
		ScopeReadGroups, ScopeWriteGroups,
		ScopeReadGeofences, ScopeWriteGeofences,
		ScopeReadCalendars, ScopeWriteCalendars,
		ScopeReadDrivers, ScopeWriteDrivers,
		ScopeReadMaintenances, ScopeWriteMaintenances,
		ScopeReadOrders, ScopeWriteOrders,
		ScopeReadReports, ScopeWriteReports,
		ScopeReadAttributes, ScopeWriteAttributes,
	}
	raw := make([]string, len(scopes))
	for index, scope := range scopes {
		raw[index] = string(scope)
	}
	parsed, err := ParseScopes(strings.Join(raw, ","))
	if err != nil {
		t.Fatalf("ParseScopes: %v", err)
	}
	if len(parsed) != len(scopes) {
		t.Fatalf("scopes = %v, want %d", parsed, len(scopes))
	}

	principal := NewPrincipal(1, false, []Scope{ScopeReadGeofences}, nil)
	if !principal.Has(ScopeReadGeofences) {
		t.Error("principal does not hold read:geofences")
	}
	if principal.Has(ScopeWriteGeofences) {
		t.Error("read:geofences must not imply write:geofences")
	}
}

func TestGenerateToken(t *testing.T) {
	first, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	second, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if !strings.HasPrefix(first, tokenPrefix) {
		t.Errorf("token = %q, want the %q prefix", first, tokenPrefix)
	}
	if len(first) < 40 {
		t.Errorf("token is too short: %q", first)
	}
	if first == second {
		t.Error("two tokens are identical")
	}
}

func TestHashTokenIsStable(t *testing.T) {
	if HashToken("secret") != HashToken("secret") {
		t.Error("hash is not stable")
	}
	if HashToken("secret") == HashToken("secret ") {
		t.Error("hash ignores trailing whitespace")
	}
	if HashToken("secret") == "secret" {
		t.Error("hash returns the token")
	}
}

func TestPrincipalScopes(t *testing.T) {
	principal := NewPrincipal(7, false, []Scope{ScopeReadPositions}, []int64{3})
	if !principal.Has(ScopeReadPositions) {
		t.Error("granted scope was rejected")
	}
	if principal.Has(ScopeReadAlerts) {
		t.Error("missing scope was accepted")
	}

	all := NewPrincipal(7, false, []Scope{ScopeAll}, nil)
	if !all.Has(ScopeWriteCommands) {
		t.Error("ScopeAll did not grant a scope")
	}

	admin := AdminPrincipal()
	if !admin.Has(ScopeWriteDevices) || !admin.CanAccessDevice(99) {
		t.Error("admin was restricted")
	}

	var disabled *Principal
	if disabled.Has(ScopeReadAlerts) || disabled.CanAccessDevice(1) {
		t.Error("nil principal granted access")
	}
}

func TestPrincipalDevices(t *testing.T) {
	principal := NewPrincipal(7, false, nil, []int64{3, 5})
	if !principal.CanAccessDevice(3) || !principal.CanAccessDevice(5) {
		t.Error("granted device was rejected")
	}
	if principal.CanAccessDevice(4) {
		t.Error("unowned device was accepted")
	}

	devices := principal.DeviceIDs()
	if len(devices) != 2 {
		t.Fatalf("devices = %v", devices)
	}
	devices[0] = 99
	if principal.CanAccessDevice(99) {
		t.Error("DeviceIDs exposed the internal slice")
	}

	empty := NewPrincipal(7, false, nil, nil)
	if empty.DeviceIDs() == nil {
		t.Error("restricted principal has a nil device slice")
	}
	var zero Principal
	if zero.DeviceIDs() == nil {
		t.Error("zero value principal must not be unrestricted")
	}
	if admin := AdminPrincipal(); admin.DeviceIDs() != nil {
		t.Error("admin should be unrestricted")
	}
}

func TestPrincipalContext(t *testing.T) {
	if FromContext(context.Background()) != nil {
		t.Error("background context has a principal")
	}

	principal := NewPrincipal(1, true, nil, nil)
	ctx := WithPrincipal(context.Background(), principal)
	if FromContext(ctx) != principal {
		t.Error("principal was not stored")
	}
}
