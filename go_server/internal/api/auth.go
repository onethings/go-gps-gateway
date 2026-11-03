package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/totp"
)

// exemptPaths stay reachable without a key so that liveness probes keep
// working when authentication is enabled, and so that a caller with no
// credential at all can obtain one.
var exemptPaths = map[string]struct{}{
	"/api/health":          {},
	"/api/login":           {},
	"/api/password/forgot": {},
	"/api/password/reset":  {},
	// The login form posts here without a credential and the web interface
	// asks for the settings before it has one; both handlers do their own
	// checks.
	"/api/session": {},
	// The OpenID routes run before a credential exists: the browser is sent to
	// the provider and comes back with a code, which is the credential.
	"/api/session/openid/auth":     {},
	"/api/session/openid/callback": {},
	"/api/server":                  {},
	// The web interface registers the first account (and any account while
	// server.registration is on) without a credential. The handler enforces
	// both conditions; a GET on the same path still requires an admin.
	"/api/users": {},
	// A browser cannot set headers on a WebSocket, so /api/socket reads its
	// token from the query string or the session cookie and authenticates
	// itself.
	"/api/socket": {},
}

// SessionCookieName carries the browser session token. The Java server uses a
// JSESSIONID cookie; this one holds a bearer token so both kinds of client
// share one authentication path.
const SessionCookieName = "traccar_session"

// sessionCookieTTL is how long a browser stays logged in. The token in the
// cookie is revoked on logout, so the value only bounds a forgotten session.
const sessionCookieTTL = 24 * time.Hour

// TokenLookup resolves a hashed bearer token to a principal. The storage store
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
		_, exempt := exemptPaths[r.URL.Path]

		// An exempt route does not need a credential, but one that is supplied
		// still authenticates: GET /api/server is anonymous while PUT on the
		// same path is admin only, and GET /api/session reads the cookie.
		if exempt && requestCredential(r) == "" {
			next.ServeHTTP(w, r)
			return
		}

		credential := requestCredential(r)
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

		if exempt {
			// A bad credential on an anonymous route is not an error: the
			// handler decides what an unauthenticated caller may see.
			next.ServeHTTP(w, r)
			return
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
	// Code is the two factor code of an account that enabled TOTP.
	Code string `json:"code"`
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

	token, plaintext, ok := s.passwordLogin(ctx, w, r, email, request.Password, request.Code)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, tokenIssueResponse{
		tokenResponse: newTokenResponse(*token),
		Token:         plaintext,
	})
}

// passwordLogin verifies one email and password pair and issues a session token.
// It writes the failure response itself and reports false when the login must
// not proceed. A successful login is recorded in the audit trail.
func (s *Server) passwordLogin(
	ctx context.Context, w http.ResponseWriter, r *http.Request, email, password, code string,
) (*model.APIToken, string, bool) {
	credential, err := s.users.CredentialByEmail(ctx, email)
	if errors.Is(err, model.ErrUserNotFound) {
		// An address that has no local account may still be a directory
		// account; the lookup is the same login, not an account enumeration.
		if s.ldap != nil {
			return s.ldapLogin(ctx, w, r, email, password, code)
		}
		// An empty hash costs the same as a real one, so an unknown address and
		// a wrong password take the same time to answer.
		auth.VerifyPassword("", password)
		s.audit(r, model.ActionDenied, "", 0)
		writeError(w, http.StatusUnauthorized, errors.New("invalid email or password"))
		return nil, "", false
	}
	if err != nil {
		s.logf("api: lookup credential of %q: %v", email, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return nil, "", false
	}
	// A locked account is refused before the password is checked. The status
	// code already tells the caller that this address has a locked account, so
	// spending a bcrypt here would only make a brute force cheaper to serve.
	if credential.LockedUntil != nil && s.now().Before(*credential.LockedUntil) {
		s.audit(r, model.ActionDenied, "", 0)
		writeLockedOut(w, credential.LockedUntil.Sub(s.now()))
		return nil, "", false
	}
	// An account that never had a password is a directory account: the local
	// row exists so the account keeps its devices and grants, but the secret
	// lives in the directory.
	if credential.PasswordHash == "" && s.ldap != nil {
		return s.ldapLogin(ctx, w, r, email, password, code)
	}
	// An account that never had a password has an empty hash, which VerifyPassword
	// charges one bcrypt for, so it answers like a wrong password.
	if !auth.VerifyPassword(credential.PasswordHash, password) {
		s.audit(r, model.ActionDenied, "", 0)
		if s.recordLoginFailure(ctx, w, credential) {
			return nil, "", false
		}
		writeError(w, http.StatusUnauthorized, errors.New("invalid email or password"))
		return nil, "", false
	}
	// Checked after the password so an unauthenticated caller cannot use the
	// status code to learn which addresses have a disabled account.
	if credential.Disabled {
		s.audit(r, model.ActionDenied, "", 0)
		writeError(w, http.StatusForbidden, errors.New("account is disabled"))
		return nil, "", false
	}
	// A wrong code is a failed login too, but it does not count against the
	// password lockout: the password was already right, so counting it would
	// let someone with the password lock the owner out of their own account.
	if !s.verifyTOTP(w, r, credential.TOTPSecret, credential.TOTPEnabled, code) {
		return nil, "", false
	}

	// The password was right, so the failure window starts over.
	if err := s.users.ResetLoginFailures(ctx, credential.UserID); err != nil {
		s.logf("api: reset login failures of user %d: %v", credential.UserID, err)
	}

	token, plaintext, err := s.users.IssueTokenForUser(ctx, credential.UserID, []string{string(auth.ScopeAll)}, nil)
	if err != nil {
		s.logf("api: issue login token for user %d: %v", credential.UserID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return nil, "", false
	}

	s.recordLoginContext(ctx, r, credential.UserID)
	return token, plaintext, true
}

// verifyTOTP enforces the second factor and writes the failure response. A
// missing code answers with the "TOTP" challenge the Java server uses, so a
// client can ask for the code instead of showing a wrong-password error.
// A wrong code is a failed login too, but neither case counts against the
// password lockout: the password was already right.
func (s *Server) verifyTOTP(
	w http.ResponseWriter, r *http.Request, secret string, enabled bool, code string,
) bool {
	if !enabled {
		return true
	}
	if strings.TrimSpace(code) == "" {
		s.audit(r, model.ActionDenied, "", 0)
		w.Header().Set("WWW-Authenticate", "TOTP")
		writeError(w, http.StatusUnauthorized, errors.New("verification code required"))
		return false
	}
	if !totp.Validate(secret, code, s.now()) {
		s.audit(r, model.ActionDenied, "", 0)
		writeError(w, http.StatusUnauthorized, errors.New("invalid verification code"))
		return false
	}
	return true
}

// recordLoginContext writes the audit row of a successful login. The audit row
// needs the user id, which only the credential knows before a principal exists.
func (s *Server) recordLoginContext(ctx context.Context, r *http.Request, userID int64) {
	if s.auditStore == nil {
		return
	}
	action := &model.Action{
		ActionTime: s.now(),
		Address:    requestAddress(r),
		UserID:     userID,
		ActionType: model.ActionLogin,
	}
	if err := s.auditStore.RecordAction(ctx, action); err != nil {
		s.logf("api: record login action: %v", err)
	}
}

// recordLogin is the background context form used by the directory path, which
// already holds a request context of its own.
func (s *Server) recordLogin(r *http.Request, userID int64) {
	s.recordLoginContext(r.Context(), r, userID)
}

// recordLoginFailure counts one wrong password and reports whether it locked
// the account, in which case the response is already written. A bookkeeping
// error is logged but changes nothing: the password was wrong either way.
func (s *Server) recordLoginFailure(ctx context.Context, w http.ResponseWriter, credential *auth.Credential) bool {
	if s.loginMaxFailures <= 0 {
		return false
	}
	lockedUntil, err := s.users.RecordLoginFailure(ctx, credential.UserID, s.loginMaxFailures, s.loginLockDuration)
	if err != nil {
		s.logf("api: record login failure of user %d: %v", credential.UserID, err)
		return false
	}
	if lockedUntil == nil {
		return false
	}
	writeLockedOut(w, lockedUntil.Sub(s.now()))
	return true
}

// writeLockedOut answers 429 and tells the caller when the lock ends.
func writeLockedOut(w http.ResponseWriter, wait time.Duration) {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeError(w, http.StatusTooManyRequests, errors.New("too many failed login attempts; try again later"))
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

// requestCredential is requestToken with the browser session cookie as the last
// place to look, so a page loaded without an explicit header still authenticates.
func requestCredential(r *http.Request) string {
	if credential := requestToken(r); credential != "" {
		return credential
	}
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		return cookie.Value
	}
	return ""
}

// setSessionCookie stores the plaintext token in the browser session cookie. A
// zero expiration makes it a session cookie the browser drops on close.
func setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	cookie := &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if !expires.IsZero() {
		cookie.Expires = expires
		cookie.MaxAge = int(time.Until(expires).Seconds())
		if cookie.MaxAge < 1 {
			cookie.MaxAge = 1
		}
	}
	http.SetCookie(w, cookie)
}

// clearSessionCookie asks the browser to forget the session.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}
