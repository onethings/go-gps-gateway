// Package auth defines the permission model shared by the REST layer and the
// storage layer: scopes, the principal resolved from a token, and the token
// generation and hashing helpers. It mirrors what Traccar calls "user" plus
// "server permissions", but keeps the data model small.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Scope names one REST capability. The strings are part of the API contract:
// they are stored verbatim in api_tokens.scopes.
type Scope string

const (
	ScopeReadPositions Scope = "read:positions"
	ScopeReadDevices   Scope = "read:devices"
	ScopeReadAlerts    Scope = "read:alerts"
	ScopeReadMetrics   Scope = "read:metrics"
	ScopeReadCommands  Scope = "read:commands"
	ScopeWriteDevices  Scope = "write:devices"
	ScopeWriteCommands Scope = "write:commands"
	// The permission routes are separate from the objects they link: a token
	// that may read devices does not automatically get to see who else was
	// granted them.
	ScopeReadPermissions  Scope = "read:permissions"
	ScopeWritePermissions Scope = "write:permissions"
	// ScopeWriteShare mints a public read only token for one device or group.
	ScopeWriteShare Scope = "write:share"

	// The resource configuration routes (groups, geofences, calendars, ...)
	// each have their own read/write pair, the same way devices and permissions
	// do: a token that may read geofences must not silently read driver
	// records too.
	ScopeReadGroups        Scope = "read:groups"
	ScopeWriteGroups       Scope = "write:groups"
	ScopeReadGeofences     Scope = "read:geofences"
	ScopeWriteGeofences    Scope = "write:geofences"
	ScopeReadCalendars     Scope = "read:calendars"
	ScopeWriteCalendars    Scope = "write:calendars"
	ScopeReadDrivers       Scope = "read:drivers"
	ScopeWriteDrivers      Scope = "write:drivers"
	ScopeReadMaintenances  Scope = "read:maintenances"
	ScopeWriteMaintenances Scope = "write:maintenances"
	ScopeReadOrders        Scope = "read:orders"
	ScopeWriteOrders       Scope = "write:orders"
	ScopeReadReports       Scope = "read:reports"
	ScopeWriteReports      Scope = "write:reports"
	ScopeReadAttributes    Scope = "read:attributes"
	ScopeWriteAttributes   Scope = "write:attributes"

	// ScopeAll grants every current and future scope. It is meant for machine
	// tokens that have to act like the legacy -api-key.
	ScopeAll Scope = "*"
)

// KnownScopes lists every scope that ParseScopes accepts, in documentation
// order.
var KnownScopes = []Scope{
	ScopeReadPositions,
	ScopeReadDevices,
	ScopeReadAlerts,
	ScopeReadMetrics,
	ScopeReadCommands,
	ScopeWriteDevices,
	ScopeWriteCommands,
	ScopeReadPermissions,
	ScopeWritePermissions,
	ScopeWriteShare,
	ScopeReadGroups,
	ScopeWriteGroups,
	ScopeReadGeofences,
	ScopeWriteGeofences,
	ScopeReadCalendars,
	ScopeWriteCalendars,
	ScopeReadDrivers,
	ScopeWriteDrivers,
	ScopeReadMaintenances,
	ScopeWriteMaintenances,
	ScopeReadOrders,
	ScopeWriteOrders,
	ScopeReadReports,
	ScopeWriteReports,
	ScopeReadAttributes,
	ScopeWriteAttributes,
	ScopeAll,
}

// ParseScopes parses a comma separated scope list, trimming whitespace and
// rejecting unknown names. An empty string yields no scopes.
func ParseScopes(raw string) ([]Scope, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	known := make(map[Scope]struct{}, len(KnownScopes))
	for _, scope := range KnownScopes {
		known[scope] = struct{}{}
	}

	seen := make(map[Scope]struct{})
	scopes := make([]Scope, 0, 4)
	for _, field := range strings.Split(raw, ",") {
		scope := Scope(strings.TrimSpace(field))
		if scope == "" {
			continue
		}
		if _, ok := known[scope]; !ok {
			return nil, fmt.Errorf("unknown scope %q", scope)
		}
		if _, duplicate := seen[scope]; duplicate {
			continue
		}
		seen[scope] = struct{}{}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

const (
	tokenPrefix              = "trc_"
	passwordResetTokenPrefix = "prt_"
)

// GenerateToken returns a new opaque bearer token. Only its hash is stored.
func GenerateToken() (string, error) {
	return generateToken(tokenPrefix)
}

// GeneratePasswordResetToken returns a single-use password reset code. It uses
// the same entropy and hashing as a bearer token but carries its own prefix so
// the two cannot be confused, and a reset code never reaches api_tokens.
func GeneratePasswordResetToken() (string, error) {
	return generateToken(passwordResetTokenPrefix)
}

func generateToken(prefix string) (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("auth: generate token: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buffer), nil
}

// HashToken returns the hex encoded SHA-256 of the token, which is what
// api_tokens.token_hash stores. The token itself is never persisted.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Principal is the authenticated caller. A nil *Principal means "no
// authentication configured" at the call sites that distinguish the two.
type Principal struct {
	// UserID is the users.id row the token belongs to; zero for the legacy
	// shared -api-key.
	UserID int64
	// TokenID is the api_tokens.id that authenticated the call. Zero means the
	// credential is not a stored token, which is the case for the legacy shared
	// key and for principals built by tests.
	TokenID int64
	// Admin bypasses scope and device ownership checks.
	Admin bool

	scopes    map[Scope]struct{}
	devices   map[int64]struct{}
	deviceIDs []int64
}

// NewPrincipal builds a principal. deviceIDs is copied, so later mutation of
// the caller's slice cannot widen the grant. The resulting DeviceIDs always
// returns a non-nil slice (possibly empty), which keeps "no devices granted"
// distinct from "every device" in the storage filter.
func NewPrincipal(userID int64, admin bool, scopes []Scope, deviceIDs []int64) *Principal {
	principal := &Principal{
		UserID:    userID,
		Admin:     admin,
		scopes:    make(map[Scope]struct{}, len(scopes)),
		deviceIDs: append([]int64{}, deviceIDs...),
	}
	for _, scope := range scopes {
		principal.scopes[scope] = struct{}{}
	}
	if len(deviceIDs) > 0 {
		principal.devices = make(map[int64]struct{}, len(deviceIDs))
		for _, deviceID := range deviceIDs {
			principal.devices[deviceID] = struct{}{}
		}
	}
	return principal
}

// AdminPrincipal is the principal behind the legacy -api-key.
func AdminPrincipal() *Principal {
	return &Principal{Admin: true}
}

// Has reports whether the principal may use scope. Admins and tokens holding
// ScopeAll pass every check.
func (p *Principal) Has(scope Scope) bool {
	if p == nil {
		return false
	}
	if p.Admin {
		return true
	}
	if _, ok := p.scopes[ScopeAll]; ok {
		return true
	}
	_, ok := p.scopes[scope]
	return ok
}

// CanAccessDevice reports whether the principal owns the device. Admins may
// access every device; a non-admin needs a matching permissions row (a direct
// device grant or one inherited from a group).
func (p *Principal) CanAccessDevice(deviceID int64) bool {
	if p == nil {
		return false
	}
	if p.Admin {
		return true
	}
	_, ok := p.devices[deviceID]
	return ok
}

// DeviceIDs returns the devices granted to the principal. It is nil for an
// admin (no restriction) and a non-nil, possibly empty slice otherwise.
func (p *Principal) DeviceIDs() []int64 {
	if p == nil || p.Admin {
		return nil
	}
	if p.deviceIDs == nil {
		// A zero value principal must not be mistaken for "every device".
		return []int64{}
	}
	return p.deviceIDs
}

type principalKey struct{}

// WithPrincipal attaches the authenticated caller to the request context.
func WithPrincipal(ctx context.Context, principal *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// FromContext returns the caller attached by the authentication middleware, or
// nil when authentication is disabled.
func FromContext(ctx context.Context) *Principal {
	principal, _ := ctx.Value(principalKey{}).(*Principal)
	return principal
}
