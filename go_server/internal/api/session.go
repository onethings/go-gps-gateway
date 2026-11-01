package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

// sessionCredentials reads the login form fields. The web interface posts
// application/x-www-form-urlencoded, which is what the Java server consumes;
// JSON is accepted as well so API clients can use one body shape.
func sessionCredentials(r *http.Request) (email, password string, err error) {
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch contentType {
	case "application/x-www-form-urlencoded", "multipart/form-data":
		if err := r.ParseForm(); err != nil {
			return "", "", fmt.Errorf("invalid form: %w", err)
		}
		return strings.TrimSpace(r.PostFormValue("email")), r.PostFormValue("password"), nil
	default:
		var request loginRequest
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxUserBody)).Decode(&request); err != nil {
			return "", "", fmt.Errorf("invalid body: %w", err)
		}
		return strings.TrimSpace(request.Email), request.Password, nil
	}
}

// handleSession returns the account behind the current credential. The route is
// exempt from the auth middleware so an unauthenticated browser gets 404
// instead of 401, which is what the web interface expects before it shows the
// login page.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	// A token in the query string is how a QR code or a native app logs in.
	if token := r.URL.Query().Get("token"); token != "" {
		if s.tokens == nil || s.users == nil {
			writeError(w, http.StatusNotFound, errors.New("session not found"))
			return
		}
		principal, err := s.tokens.LookupToken(ctx, auth.HashToken(token))
		if err != nil {
			s.logf("api: lookup session token: %v", err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
		if principal == nil || principal.UserID == 0 {
			writeError(w, http.StatusNotFound, errors.New("session not found"))
			return
		}
		user, err := s.users.User(ctx, principal.UserID)
		if err != nil {
			writeError(w, http.StatusNotFound, errors.New("session not found"))
			return
		}
		// The token stays valid until it expires; the cookie only carries it.
		setSessionCookie(w, token, s.now().Add(sessionCookieTTL))
		writeJSON(w, http.StatusOK, newUserResponse(*user))
		return
	}

	principal := auth.FromContext(r.Context())
	if principal == nil || principal.UserID == 0 || s.users == nil {
		writeError(w, http.StatusNotFound, errors.New("session not found"))
		return
	}
	user, err := s.users.User(ctx, principal.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("session not found"))
		return
	}
	writeJSON(w, http.StatusOK, newUserResponse(*user))
}

// handleSessionCreate logs an account in and stores the session token in the
// browser cookie. It is the cookie counterpart of POST /api/login.
func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) {
		return
	}
	email, password, err := sessionCredentials(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !validEmail(email) || password == "" {
		writeError(w, http.StatusBadRequest, errors.New("email and password are required"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	token, plaintext, ok := s.passwordLogin(ctx, w, r, email, password)
	if !ok {
		return
	}
	user, err := s.users.User(ctx, token.UserID)
	if err != nil {
		s.logf("api: load user %d after login: %v", token.UserID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	setSessionCookie(w, plaintext, s.now().Add(sessionCookieTTL))
	writeJSON(w, http.StatusOK, newUserResponse(*user))
}

// handleSessionDelete logs out: the token of the cookie is revoked and the
// cookie removed.
func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	principal := auth.FromContext(r.Context())
	if principal != nil && principal.TokenID != 0 && principal.UserID != 0 && s.users != nil {
		ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
		defer cancel()
		if err := s.users.RevokeToken(ctx, principal.UserID, principal.TokenID); err != nil {
			s.logf("api: revoke session token %d: %v", principal.TokenID, err)
		}
		if s.auditStore != nil {
			action := &model.Action{
				ActionTime: s.now(),
				Address:    requestAddress(r),
				UserID:     principal.UserID,
				ActionType: model.ActionLogout,
			}
			if err := s.auditStore.RecordAction(ctx, action); err != nil {
				s.logf("api: record logout action: %v", err)
			}
		}
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleSessionUser answers GET /api/session/{id}. An administrator uses it to
// switch to another account, which is why a new token and cookie are issued.
func (s *Server) handleSessionUser(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) {
		return
	}
	userID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.isAdmin(r) && !s.isSelf(r, userID) {
		writeError(w, http.StatusForbidden, errors.New("admin privileges or the own account are required"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	user, err := s.users.User(ctx, userID)
	if errors.Is(err, model.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown user %d", userID))
		return
	}
	if err != nil {
		s.logf("api: load user %d: %v", userID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if user.Disabled {
		writeError(w, http.StatusForbidden, errors.New("account is disabled"))
		return
	}

	if !s.isSelf(r, userID) {
		token, plaintext, err := s.users.IssueTokenForUser(ctx, userID, []string{string(auth.ScopeAll)}, nil)
		if err != nil {
			s.logf("api: issue session token for user %d: %v", userID, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
		setSessionCookie(w, plaintext, s.now().Add(sessionCookieTTL))
		if s.auditStore != nil {
			action := &model.Action{
				ActionTime: s.now(),
				Address:    requestAddress(r),
				UserID:     token.UserID,
				ActionType: model.ActionToken,
				ObjectType: "token",
				ObjectID:   token.ID,
			}
			if err := s.auditStore.RecordAction(ctx, action); err != nil {
				s.logf("api: record token action: %v", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, newUserResponse(*user))
}

// sessionTokenRequest is the body of POST /api/session/token. The web form sends
// an "expiration" date; JSON clients may send expiresIn as a Go duration.
type sessionTokenRequest struct {
	Expiration string `json:"expiration"`
	ExpiresIn  string `json:"expiresIn"`
}

// handleSessionToken mints a token for the caller and returns it as plain text,
// which is what the Java resource does.
func (s *Server) handleSessionToken(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) {
		return
	}
	principal := auth.FromContext(r.Context())
	if principal == nil || principal.UserID == 0 {
		writeError(w, http.StatusBadRequest, errors.New("the shared API key cannot own a token"))
		return
	}

	var request sessionTokenRequest
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if contentType == "application/x-www-form-urlencoded" || contentType == "multipart/form-data" {
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid form: %w", err))
			return
		}
		request.Expiration = r.PostFormValue("expiration")
		request.ExpiresIn = r.PostFormValue("expiresIn")
	} else if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBody)).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
			return
		}
	}

	var expiresAt *time.Time
	if request.Expiration != "" {
		parsed, err := parseTime(request.Expiration)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid expiration: %w", err))
			return
		}
		if !parsed.IsZero() {
			if !parsed.After(s.now()) {
				writeError(w, http.StatusBadRequest, errors.New("expiration must be in the future"))
				return
			}
			expiresAt = &parsed
		}
	} else if request.ExpiresIn != "" {
		duration, err := time.ParseDuration(request.ExpiresIn)
		if err != nil || duration <= 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid expiresIn %q", request.ExpiresIn))
			return
		}
		deadline := s.now().Add(duration)
		expiresAt = &deadline
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	token, plaintext, err := s.users.IssueTokenForUser(ctx, principal.UserID, []string{string(auth.ScopeAll)}, expiresAt)
	if err != nil {
		s.logf("api: issue token for user %d: %v", principal.UserID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if s.auditStore != nil {
		action := &model.Action{
			ActionTime: s.now(),
			Address:    requestAddress(r),
			UserID:     principal.UserID,
			ActionType: model.ActionToken,
			ObjectType: "token",
			ObjectID:   token.ID,
		}
		if err := s.auditStore.RecordAction(ctx, action); err != nil {
			s.logf("api: record token action: %v", err)
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(plaintext))
}

// handleSessionRevoke drops a token the caller owns. An administrator may revoke
// a token of any account.
func (s *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) || s.tokens == nil {
		writeError(w, http.StatusNotImplemented, errors.New("token sessions are not configured"))
		return
	}

	var raw string
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if contentType == "application/x-www-form-urlencoded" || contentType == "multipart/form-data" {
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid form: %w", err))
			return
		}
		raw = r.PostFormValue("token")
	} else {
		var request struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBody)).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
			return
		}
		raw = request.Token
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		writeError(w, http.StatusBadRequest, errors.New("token is required"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	principal, err := s.tokens.LookupToken(ctx, auth.HashToken(raw))
	if err != nil {
		s.logf("api: lookup revoked token: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if principal == nil {
		writeError(w, http.StatusNotFound, errors.New("unknown token"))
		return
	}
	if !s.isAdmin(r) && !s.isSelf(r, principal.UserID) {
		writeError(w, http.StatusForbidden, errors.New("admin privileges or the own account are required"))
		return
	}
	if err := s.users.RevokeToken(ctx, principal.UserID, principal.TokenID); err != nil {
		s.logf("api: revoke token %d: %v", principal.TokenID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	s.audit(r, model.ActionRemove, "token", principal.TokenID)
	w.WriteHeader(http.StatusNoContent)
}
