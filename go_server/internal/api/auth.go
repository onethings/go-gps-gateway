package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

// exemptPaths stay reachable without a key so that liveness probes keep
// working when authentication is enabled.
var exemptPaths = map[string]struct{}{
	"/api/health": {},
}

// RequireAPIKey wraps next so that every request except the exempt ones must
// present the key, either as "X-Api-Key" or as an "Authorization: Bearer"
// token. The comparison is constant time.
func RequireAPIKey(key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, exempt := exemptPaths[r.URL.Path]; exempt {
			next.ServeHTTP(w, r)
			return
		}
		if !validAPIKey(key, r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="traccar"`)
			writeError(w, http.StatusUnauthorized, errors.New("invalid API key"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validAPIKey(key string, r *http.Request) bool {
	supplied := r.Header.Get("X-Api-Key")
	if supplied == "" {
		if authorization := r.Header.Get("Authorization"); strings.HasPrefix(authorization, "Bearer ") {
			supplied = strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
		}
	}
	if supplied == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(supplied), []byte(key)) == 1
}
