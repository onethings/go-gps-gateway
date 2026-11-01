package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

// passwordResetTTL is how long an emailed reset code stays usable. It is short
// because the code is a bearer credential for one account.
const passwordResetTTL = 30 * time.Minute

// passwordResetSendTimeout bounds one delivery attempt. The send runs after the
// HTTP response, so a slow relay cannot make a known address answer later than
// an unknown one.
const passwordResetSendTimeout = 15 * time.Second

// resetSendSlots caps how many reset emails may be in flight at once, so a
// flood of forgot requests cannot turn into unbounded SMTP connections.
var resetSendSlots = make(chan struct{}, 8)

const (
	// DefaultLoginMaxFailures is how many consecutive wrong passwords lock an
	// account unless the server is configured otherwise.
	DefaultLoginMaxFailures = 5
	// DefaultLoginLockDuration is how long such a lock lasts.
	DefaultLoginLockDuration = 15 * time.Minute
)

// WithLoginLockout sets how many consecutive wrong passwords lock an account
// and for how long. maxFailures <= 0 disables the limiter.
func WithLoginLockout(maxFailures int, lockFor time.Duration) Option {
	return func(s *Server) {
		s.loginMaxFailures = maxFailures
		s.loginLockDuration = lockFor
	}
}

// PasswordResetMailer delivers a reset code to one address. *notify.PasswordResetMail
// implements it; tests use a recorder.
type PasswordResetMailer interface {
	SendPasswordReset(ctx context.Context, to, token string, ttl time.Duration) error
}

// WithPasswordReset enables POST /api/password/forgot. Without a mailer the
// route reports 501, because no reset code could reach the account.
func WithPasswordReset(mailer PasswordResetMailer) Option {
	return func(s *Server) { s.passwordReset = mailer }
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type forgotPasswordRequest struct {
	Email string `json:"email"`
}

type resetPasswordRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// handleChangePassword lets an account replace its own password after proving
// it knows the current one. An account without a password has to be given one
// by an administrator first, otherwise any token would be able to turn itself
// into a password login.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) {
		return
	}

	principal := auth.FromContext(r.Context())
	if principal == nil || principal.UserID == 0 {
		writeError(w, http.StatusForbidden,
			errors.New("this route needs a user token; the shared API key has no account"))
		return
	}

	var request changePasswordRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}
	if request.CurrentPassword == "" || request.NewPassword == "" {
		writeError(w, http.StatusBadRequest, errors.New("currentPassword and newPassword are required"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	credential, err := s.users.CredentialByID(ctx, principal.UserID)
	if errors.Is(err, model.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown user %d", principal.UserID))
		return
	}
	if err != nil {
		s.logf("api: lookup credential of user %d: %v", principal.UserID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if credential.PasswordHash == "" {
		writeError(w, http.StatusForbidden,
			errors.New("this account has no password; ask an administrator to set one"))
		return
	}
	if !auth.VerifyPassword(credential.PasswordHash, request.CurrentPassword) {
		writeError(w, http.StatusForbidden, errors.New("current password is incorrect"))
		return
	}

	hash, err := auth.HashPassword(request.NewPassword)
	if errors.Is(err, auth.ErrEmptyPassword) || errors.Is(err, auth.ErrPasswordTooLong) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		s.logf("api: hash new password of user %d: %v", principal.UserID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	if err := s.users.SetPassword(ctx, principal.UserID, hash); err != nil {
		if errors.Is(err, model.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, fmt.Errorf("unknown user %d", principal.UserID))
			return
		}
		s.logf("api: set password of user %d: %v", principal.UserID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	// The password changed, so everything derived from the old one has to go:
	// failures no longer matter, outstanding reset codes must not work, and
	// other tokens must be logged out. None of these can fail the request,
	// because the new password is already stored.
	if err := s.users.ResetLoginFailures(ctx, principal.UserID); err != nil {
		s.logf("api: reset login failures of user %d: %v", principal.UserID, err)
	}
	if err := s.users.DeletePasswordResets(ctx, principal.UserID); err != nil {
		s.logf("api: delete password resets of user %d: %v", principal.UserID, err)
	}
	if err := s.users.RevokeTokensExcept(ctx, principal.UserID, principal.TokenID); err != nil {
		s.logf("api: revoke other tokens of user %d: %v", principal.UserID, err)
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleForgotPassword starts the reset flow. It answers the same way for an
// unknown, disabled or throttled address so that the endpoint cannot be used to
// learn which addresses have an account.
func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) {
		return
	}
	if s.passwordReset == nil {
		writeError(w, http.StatusNotImplemented, errors.New("password reset is not configured"))
		return
	}

	var request forgotPasswordRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}
	email := strings.TrimSpace(request.Email)
	if !validEmail(email) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid email %q", email))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	credential, err := s.users.CredentialByEmail(ctx, email)
	if err != nil && !errors.Is(err, model.ErrUserNotFound) {
		s.logf("api: lookup credential of %q: %v", email, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if err == nil && !credential.Disabled {
		s.sendPasswordReset(ctx, email, credential.UserID)
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status": "if the address has an account, a reset code has been sent",
	})
}

// sendPasswordReset creates one code and queues its delivery. Every failure
// after the caller's response is logged and swallowed, because the response
// deliberately says nothing about the account. The delivery is asynchronous so
// that SMTP latency cannot become a timing oracle for which addresses exist.
func (s *Server) sendPasswordReset(ctx context.Context, email string, userID int64) {
	token, err := auth.GeneratePasswordResetToken()
	if err != nil {
		s.logf("api: generate password reset for user %d: %v", userID, err)
		return
	}
	tokenHash := auth.HashToken(token)
	expiresAt := s.now().Add(passwordResetTTL)
	err = s.users.CreatePasswordReset(ctx, userID, tokenHash, expiresAt)
	if errors.Is(err, model.ErrResetTooSoon) {
		return
	}
	if err != nil {
		s.logf("api: create password reset for user %d: %v", userID, err)
		return
	}

	// Taking a slot here, not inside the goroutine, bounds the number of
	// waiting deliveries; a flood then drops the code instead of queuing an
	// unbounded number of goroutines.
	select {
	case resetSendSlots <- struct{}{}:
	default:
		s.logf("api: password reset delivery is saturated; dropping the code of user %d", userID)
		if err := s.users.DeletePasswordReset(ctx, tokenHash); err != nil {
			s.logf("api: delete undelivered password reset of user %d: %v", userID, err)
		}
		return
	}
	go s.deliverPasswordReset(userID, email, token, tokenHash)
}

// deliverPasswordReset sends one code and drops it when the relay refuses it,
// so the next request can try again instead of being throttled by a code that
// never arrived. It must not use the request context, which is already done.
func (s *Server) deliverPasswordReset(userID int64, email, token, tokenHash string) {
	defer func() { <-resetSendSlots }()

	ctx, cancel := context.WithTimeout(context.Background(), passwordResetSendTimeout)
	defer cancel()

	if err := s.passwordReset.SendPasswordReset(ctx, email, token, passwordResetTTL); err != nil {
		s.logf("api: send password reset to %q: %v", email, err)
		if err := s.users.DeletePasswordReset(ctx, tokenHash); err != nil {
			s.logf("api: delete undelivered password reset of user %d: %v", userID, err)
		}
	}
}

// handleResetPassword trades an emailed code for a new password. The code is
// consumed in the same request, and every existing token of the account is
// revoked: a password reset is what an account does when it lost control of
// the old credentials.
func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) {
		return
	}

	var request resetPasswordRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}
	token := strings.TrimSpace(request.Token)
	if token == "" || request.Password == "" {
		writeError(w, http.StatusBadRequest, errors.New("token and password are required"))
		return
	}

	// Hashed before the code is consumed, so a rejected password does not burn
	// the account's only reset code.
	hash, err := auth.HashPassword(request.Password)
	if errors.Is(err, auth.ErrEmptyPassword) || errors.Is(err, auth.ErrPasswordTooLong) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		s.logf("api: hash reset password: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	userID, err := s.users.ConsumePasswordReset(ctx, auth.HashToken(token))
	if errors.Is(err, model.ErrResetTokenInvalid) {
		writeError(w, http.StatusBadRequest, model.ErrResetTokenInvalid)
		return
	}
	if err != nil {
		s.logf("api: consume password reset: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	if err := s.users.SetPassword(ctx, userID, hash); err != nil {
		if errors.Is(err, model.ErrUserNotFound) {
			writeError(w, http.StatusBadRequest, model.ErrResetTokenInvalid)
			return
		}
		s.logf("api: set reset password of user %d: %v", userID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	if err := s.users.ResetLoginFailures(ctx, userID); err != nil {
		s.logf("api: reset login failures of user %d: %v", userID, err)
	}
	if err := s.users.DeletePasswordResets(ctx, userID); err != nil {
		s.logf("api: delete password resets of user %d: %v", userID, err)
	}
	if err := s.users.RevokeTokensExcept(ctx, userID, 0); err != nil {
		s.logf("api: revoke tokens of user %d: %v", userID, err)
	}

	w.WriteHeader(http.StatusNoContent)
}
