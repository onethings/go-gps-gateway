package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

// exemptPaths stay reachable without a key so that liveness probes keep
// working when authentication is enabled, and so that a caller with no
// credential at all can obtain one.
var exemptPaths = map[string]struct{}{
	"/api/health": {},
	"/api/login":  {},
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

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleLogin trades an email and a password for a new bearer token. The route
// is exempt from authentication because a caller without a credential has no
// other way to get one.
//
// The issued token carries "*": password possession is the account's own
// authority, the same way a Traccar session is. Account administration still
// needs users.admin, which no scope grants.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) {
		return
	}

	var request loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}
	email := strings.TrimSpace(request.Email)
	if !validEmail(email) || request.Password == "" {
		writeError(w, http.StatusBadRequest, errors.New("email and password are required"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	credential, err := s.users.CredentialByEmail(ctx, email)
	if errors.Is(err, model.ErrUserNotFound) {
		// An empty hash costs the same as a real one, so an unknown address and
		// a wrong password take the same time to answer.
		auth.VerifyPassword("", request.Password)
		writeError(w, http.StatusUnauthorized, errors.New("invalid email or password"))
		return
	}
	if err != nil {
		s.logf("api: lookup credential of %q: %v", email, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	// An account that never had a password has an empty hash, which VerifyPassword
	// charges one bcrypt for, so it answers like a wrong password.
	if !auth.VerifyPassword(credential.PasswordHash, request.Password) {
		writeError(w, http.StatusUnauthorized, errors.New("invalid email or password"))
		return
	}
	// Checked after the password so an unauthenticated caller cannot use the
	// status code to learn which addresses have a disabled account.
	if credential.Disabled {
		writeError(w, http.StatusForbidden, errors.New("account is disabled"))
		return
	}

	token, plaintext, err := s.users.IssueTokenForUser(ctx, credential.UserID, []string{string(auth.ScopeAll)}, nil)
	if err != nil {
		s.logf("api: issue login token for user %d: %v", credential.UserID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	writeJSON(w, http.StatusOK, tokenIssueResponse{
		tokenResponse: newTokenResponse(*token),
		Token:         plaintext,
	})
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
