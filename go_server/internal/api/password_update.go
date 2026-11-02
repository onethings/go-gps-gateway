package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

// passwordUpdateRequest is the body of POST /api/password/update. The Java web
// interface posts the new password alone, because the caller already holds a
// session; the current password is only asked for by PUT /api/users/me/password.
type passwordUpdateRequest struct {
	Password    string `json:"password"`
	NewPassword string `json:"newPassword"`
}

// handlePasswordUpdate sets the password of the caller's own account. It is the
// route the Java web interface calls when an administrator resets a password or
// a user finishes the account dialog.
func (s *Server) handlePasswordUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) {
		return
	}
	principal := auth.FromContext(r.Context())
	if principal == nil || principal.UserID == 0 {
		writeError(w, http.StatusForbidden,
			errors.New("this route needs a user token; the shared API key has no account"))
		return
	}

	var request passwordUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}
	password := request.Password
	if password == "" {
		password = request.NewPassword
	}
	if password == "" {
		writeError(w, http.StatusBadRequest, errors.New("password is required"))
		return
	}

	hash, err := auth.HashPassword(password)
	if errors.Is(err, auth.ErrEmptyPassword) || errors.Is(err, auth.ErrPasswordTooLong) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		s.logf("api: hash updated password of user %d: %v", principal.UserID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()
	if err := s.users.SetPassword(ctx, principal.UserID, hash); err != nil {
		if errors.Is(err, model.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		s.logf("api: set password of user %d: %v", principal.UserID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if err := s.users.ResetLoginFailures(ctx, principal.UserID); err != nil {
		s.logf("api: reset login failures of user %d: %v", principal.UserID, err)
	}
	// Every other token was issued for the old password, so only the session
	// that made this change stays valid.
	if err := s.users.RevokeTokensExcept(ctx, principal.UserID, principal.TokenID); err != nil {
		s.logf("api: revoke tokens of user %d: %v", principal.UserID, err)
	}
	w.WriteHeader(http.StatusNoContent)
}
