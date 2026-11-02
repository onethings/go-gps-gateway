package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/totp"
)

// totpIssuer is the label an authenticator app shows for this server.
const totpIssuer = "Traccar"

type totpCodeRequest struct {
	Code string `json:"code"`
}

// handleTOTPStatus answers GET /api/users/{id}/totp. The secret is never
// returned here; the account only learns whether a code is required.
func (s *Server) handleTOTPStatus(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.totpUser(w, r)
	if !ok {
		return
	}
	if s.totp == nil {
		notConfigured(w, "two factor authentication")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()
	_, enabled, err := s.totp.UserTOTP(ctx, userID)
	if err != nil {
		s.writeTOTPError(w, userID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
}

// handleTOTPGenerate answers POST /api/users/{id}/totp: it creates a new
// secret and returns it once, together with the otpauth URL the client renders
// as a QR code. The account is not protected until a code proves the app holds
// the secret.
func (s *Server) handleTOTPGenerate(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.totpUser(w, r)
	if !ok {
		return
	}
	if s.totp == nil {
		notConfigured(w, "two factor authentication")
		return
	}
	secret, err := totp.GenerateSecret()
	if err != nil {
		s.logf("api: generate totp secret: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()
	if err := s.totp.SetUserTOTP(ctx, userID, secret, false); err != nil {
		s.writeTOTPError(w, userID, err)
		return
	}

	account := ""
	if s.users != nil {
		if user, err := s.users.User(ctx, userID); err == nil {
			account = user.Email
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": secret,
		"url":    totp.ProvisioningURL(totpIssuer, account, secret),
	})
}

// handleTOTPEnable answers PUT /api/users/{id}/totp. The posted code must match
// the stored secret before the account starts requiring it.
func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.totpUser(w, r)
	if !ok {
		return
	}
	if s.totp == nil {
		notConfigured(w, "two factor authentication")
		return
	}
	s.confirmTOTP(w, r, userID)
}

// handleTOTPVerifySelf answers POST /api/users/totp for the caller's own
// account. It is the route a client uses right after generating a secret.
func (s *Server) handleTOTPVerifySelf(w http.ResponseWriter, r *http.Request) {
	principal := auth.FromContext(r.Context())
	if principal == nil || principal.UserID == 0 {
		writeError(w, http.StatusForbidden,
			errors.New("this route needs a user token; the shared API key has no account"))
		return
	}
	if s.totp == nil {
		notConfigured(w, "two factor authentication")
		return
	}
	s.confirmTOTP(w, r, principal.UserID)
}

func (s *Server) confirmTOTP(w http.ResponseWriter, r *http.Request, userID int64) {
	var request totpCodeRequest
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxUserBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()
	secret, _, err := s.totp.UserTOTP(ctx, userID)
	if err != nil {
		s.writeTOTPError(w, userID, err)
		return
	}
	if secret == "" {
		writeError(w, http.StatusBadRequest, errors.New("no two factor secret has been generated"))
		return
	}
	if !totp.Validate(secret, request.Code, s.now()) {
		writeError(w, http.StatusBadRequest, errors.New("invalid verification code"))
		return
	}
	if err := s.totp.SetUserTOTP(ctx, userID, secret, true); err != nil {
		s.writeTOTPError(w, userID, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTOTPDisable answers DELETE /api/users/{id}/totp: the secret is removed
// and the account goes back to password only.
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.totpUser(w, r)
	if !ok {
		return
	}
	if s.totp == nil {
		notConfigured(w, "two factor authentication")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()
	if err := s.totp.SetUserTOTP(ctx, userID, "", false); err != nil {
		s.writeTOTPError(w, userID, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// totpUser reads {id} and allows only the account itself or an admin. A
// two factor secret is account data, so the shared API key cannot manage it.
func (s *Server) totpUser(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if !s.authorize(w, r, auth.ScopeWriteUsers) {
		return 0, false
	}
	userID, err := parsePositiveID(r.PathValue("id"), "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return 0, false
	}
	if !s.isSelf(r, userID) && !s.isAdmin(r) {
		writeError(w, http.StatusForbidden, errors.New("two factor settings are private to the account"))
		return 0, false
	}
	return userID, true
}

func (s *Server) writeTOTPError(w http.ResponseWriter, userID int64, err error) {
	if errors.Is(err, model.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	s.logf("api: totp of user %d: %v", userID, err)
	writeError(w, http.StatusInternalServerError, errors.New("internal error"))
}
