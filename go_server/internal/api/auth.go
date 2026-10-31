package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"traccar-server_go/go_server/internal/auth"
)

// exemptPaths stay reachable without a key so that liveness probes keep
// working when authentication is enabled.
var exemptPaths = map[string]struct{}{
	"/api/health": {},
}

// TokenLookup resolves a hashed bearer token to a principal. The postgres store
// implements it; tests use a map.
type TokenLookup interface {
	LookupToken(ctx context.Context, tokenHash string) (*auth.Principal, error)
}

// RequireAuth wraps next so that every request except the exempt ones must
// present a credential, either as "X-Api-Key" or as an "Authorization: Bearer"
// token.
//
// The legacy apiKey is compared in constant time and maps to an admin
// principal. Any other value is hashed and resolved through tokens, which
// carries the scopes and device grants. A lookup failure is reported as 500
// instead of 401 so a database outage does not look like a bad token.
func RequireAuth(apiKey string, tokens TokenLookup, logf func(string, ...any), next http.Handler) http.Handler {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, exempt := exemptPaths[r.URL.Path]; exempt {
			next.ServeHTTP(w, r)
			return
		}

		credential := requestToken(r)
		if credential == "" {
			writeUnauthorized(w)
			return
		}

		if apiKey != "" && subtle.ConstantTimeCompare([]byte(credential), []byte(apiKey)) == 1 {
			next.ServeHTTP(w, r.WithContext(
				auth.WithPrincipal(r.Context(), auth.AdminPrincipal())))
			return
		}

		if tokens != nil {
			principal, err := tokens.LookupToken(r.Context(), auth.HashToken(credential))
			if err != nil {
				logf("api: lookup token: %v", err)
				writeError(w, http.StatusInternalServerError, errors.New("internal error"))
				return
			}
			if principal != nil {
				next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
				return
			}
		}

		writeUnauthorized(w)
	})
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="traccar"`)
	writeError(w, http.StatusUnauthorized, errors.New("invalid API key"))
}

// requestToken reads the credential from X-Api-Key or a Bearer header.
func requestToken(r *http.Request) string {
	if supplied := r.Header.Get("X-Api-Key"); supplied != "" {
		return supplied
	}
	authorization := r.Header.Get("Authorization")
	if strings.HasPrefix(authorization, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	}
	return ""
}
