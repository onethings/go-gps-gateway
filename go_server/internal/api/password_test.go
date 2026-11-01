package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

// fakeResetMailer records the codes the forgot route would have emailed. A
// non-nil block makes SendPasswordReset wait, which lets a test prove the HTTP
// response does not depend on delivery.
type fakeResetMailer struct {
	mu    sync.Mutex
	sends []resetMail
	err   error
	block <-chan struct{}
}

type resetMail struct {
	to    string
	token string
	ttl   time.Duration
}

func (f *fakeResetMailer) SendPasswordReset(_ context.Context, to, token string, ttl time.Duration) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sends = append(f.sends, resetMail{to: to, token: token, ttl: ttl})
	return nil
}

func (f *fakeResetMailer) sent() []resetMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]resetMail{}, f.sends...)
}

// waitForMail waits for the asynchronous delivery and returns what arrived.
func waitForMail(t *testing.T, mailer *fakeResetMailer, want int) []resetMail {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sends := mailer.sent(); len(sends) >= want {
			return sends
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("mailer received %d messages, want %d", len(mailer.sent()), want)
	return nil
}

// waitForResets waits until the stored codes reach want.
func waitForResets(t *testing.T, store *fakeUsers, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		count := len(store.resets)
		store.mu.Unlock()
		if count == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("stored reset codes did not reach %d", want)
}

// accountPrincipal is a token of userID. TokenID identifies the token that
// made the call, which is what a password change keeps.
func accountPrincipal(userID, tokenID int64) *auth.Principal {
	principal := auth.NewPrincipal(userID, false, []auth.Scope{auth.ScopeAll}, nil)
	principal.TokenID = tokenID
	return principal
}

func changePasswordHandler(users UserStore, principal *auth.Principal) http.Handler {
	return NewServer(nil, WithUsers(users), withTestToken(principal)).Handler()
}

func TestChangePassword(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, store, 1)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, _, err := store.IssueTokenForUser(ctx, 1, []string{"*"}, nil); err != nil {
			t.Fatalf("seed token: %v", err)
		}
	}
	if _, err := store.RecordLoginFailure(ctx, 1, 5, time.Minute); err != nil {
		t.Fatalf("seed failure: %v", err)
	}
	if err := store.CreatePasswordReset(ctx, 1, "stale-hash", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("seed reset: %v", err)
	}

	// The caller holds token 1; token 2 is a session that has to be logged out.
	handler := changePasswordHandler(store, accountPrincipal(1, 1))
	body := `{"currentPassword":"` + testPassword + `","newPassword":"a new password"}`
	recorder := withToken(t, handler, http.MethodPut, "/api/users/me/password", body)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", recorder.Code, recorder.Body)
	}

	credential, err := store.CredentialByID(ctx, 1)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if !auth.VerifyPassword(credential.PasswordHash, "a new password") {
		t.Error("the new password does not verify")
	}
	if auth.VerifyPassword(credential.PasswordHash, testPassword) {
		t.Error("the old password still verifies")
	}
	if tokens := store.tokens[1]; len(tokens) != 1 || tokens[0].ID != 1 {
		t.Errorf("tokens = %+v, want only the caller's token", tokens)
	}
	if len(store.resets) != 0 {
		t.Errorf("outstanding reset codes = %d, want 0", len(store.resets))
	}
	store.mu.Lock()
	failed := store.failed[1]
	store.mu.Unlock()
	if failed != 0 {
		t.Errorf("failed logins = %d, want 0", failed)
	}
}

func TestChangePasswordRejectsWrongCurrent(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, store, 1)
	handler := changePasswordHandler(store, accountPrincipal(1, 1))

	body := `{"currentPassword":"not the password","newPassword":"a new password"}`
	recorder := withToken(t, handler, http.MethodPut, "/api/users/me/password", body)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
	if !auth.VerifyPassword(store.passwords[1], testPassword) {
		t.Error("a rejected change replaced the stored password")
	}
}

func TestChangePasswordNeedsAnExistingPassword(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	handler := changePasswordHandler(store, accountPrincipal(1, 1))

	body := `{"currentPassword":"anything","newPassword":"a new password"}`
	recorder := withToken(t, handler, http.MethodPut, "/api/users/me/password", body)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
	if hash := store.passwords[1]; hash != "" {
		t.Error("a token-only account set its own first password")
	}
}

func TestChangePasswordNeedsAUserToken(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, store, 1)
	handler := NewServer(nil, WithUsers(store), WithAPIKey("shared-key")).Handler()

	body := `{"currentPassword":"` + testPassword + `","newPassword":"a new password"}`
	recorder := apiRequest(t, handler, http.MethodPut, "/api/users/me/password", body, "shared-key")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
	if !auth.VerifyPassword(store.passwords[1], testPassword) {
		t.Error("the shared key changed the account password")
	}
}

func TestChangePasswordRejectsLongNewPassword(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, store, 1)
	handler := changePasswordHandler(store, accountPrincipal(1, 1))

	body := `{"currentPassword":"` + testPassword + `","newPassword":"` + strings.Repeat("a", 73) + `"}`
	recorder := withToken(t, handler, http.MethodPut, "/api/users/me/password", body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body)
	}
	if !auth.VerifyPassword(store.passwords[1], testPassword) {
		t.Error("a rejected password was stored")
	}
}

func TestChangePasswordReportsNotConfigured(t *testing.T) {
	handler := NewServer(nil).Handler()

	recorder := apiRequest(t, handler, http.MethodPut, "/api/users/me/password",
		`{"currentPassword":"a","newPassword":"b"}`, "")
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", recorder.Code, recorder.Body)
	}
}

func forgotPasswordHandler(store UserStore, mailer PasswordResetMailer) http.Handler {
	return NewServer(nil, WithUsers(store), WithAPIKey("shared-key"), WithPasswordReset(mailer)).Handler()
}

func TestForgotPasswordMailsACode(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	mailer := &fakeResetMailer{}
	handler := forgotPasswordHandler(store, mailer)

	recorder := apiRequest(t, handler, http.MethodPost, "/api/password/forgot",
		`{"email":"`+testEmail+`"}`, "")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body)
	}

	sends := waitForMail(t, mailer, 1)
	if len(sends) != 1 {
		t.Fatalf("sent = %d messages, want 1", len(sends))
	}
	if sends[0].to != testEmail {
		t.Errorf("recipient = %q, want %q", sends[0].to, testEmail)
	}
	if !strings.HasPrefix(sends[0].token, "prt_") {
		t.Errorf("token = %q, want the prt_ prefix", sends[0].token)
	}
	if sends[0].ttl != passwordResetTTL {
		t.Errorf("ttl = %v, want %v", sends[0].ttl, passwordResetTTL)
	}
	if _, ok := store.resets[auth.HashToken(sends[0].token)]; !ok {
		t.Error("the mailed code was not stored")
	}
	if strings.Contains(recorder.Body.String(), testEmail) {
		t.Errorf("the response repeats the address: %s", recorder.Body)
	}
}

func TestForgotPasswordHidesUnknownAddress(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	mailer := &fakeResetMailer{}
	handler := forgotPasswordHandler(store, mailer)

	recorder := apiRequest(t, handler, http.MethodPost, "/api/password/forgot",
		`{"email":"nobody@example.test"}`, "")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body)
	}
	if sends := mailer.sent(); len(sends) != 0 {
		t.Errorf("sent %d messages for an unknown address", len(sends))
	}
	if len(store.resets) != 0 {
		t.Errorf("stored %d codes for an unknown address", len(store.resets))
	}
}

func TestForgotPasswordSkipsDisabledAccount(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	disabled := true
	if _, err := store.UpdateUser(context.Background(), 1, model.UserUpdate{Disabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	mailer := &fakeResetMailer{}
	handler := forgotPasswordHandler(store, mailer)

	if recorder := apiRequest(t, handler, http.MethodPost, "/api/password/forgot",
		`{"email":"`+testEmail+`"}`, ""); recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", recorder.Code)
	}
	if sends := mailer.sent(); len(sends) != 0 {
		t.Errorf("a disabled account received %d reset messages", len(sends))
	}
}

func TestForgotPasswordThrottlesResend(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	mailer := &fakeResetMailer{}
	handler := forgotPasswordHandler(store, mailer)

	for i := 0; i < 2; i++ {
		recorder := apiRequest(t, handler, http.MethodPost, "/api/password/forgot",
			`{"email":"`+testEmail+`"}`, "")
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("request %d status = %d, want 202", i+1, recorder.Code)
		}
		if i == 0 {
			// Delivery is asynchronous; let the first code exist before the
			// second request so the throttle is what refuses it.
			waitForMail(t, mailer, 1)
		}
	}
	if sends := mailer.sent(); len(sends) != 1 {
		t.Errorf("sent = %d messages within the throttle window, want 1", len(sends))
	}
}

func TestForgotPasswordDropsUndeliveredCode(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	mailer := &fakeResetMailer{err: errLookup}
	handler := forgotPasswordHandler(store, mailer)

	recorder := apiRequest(t, handler, http.MethodPost, "/api/password/forgot",
		`{"email":"`+testEmail+`"}`, "")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body)
	}
	waitForResets(t, store, 0)

	// The next request is not throttled by the code that never arrived.
	mailer.mu.Lock()
	mailer.err = nil
	mailer.mu.Unlock()
	if recorder := apiRequest(t, handler, http.MethodPost, "/api/password/forgot",
		`{"email":"`+testEmail+`"}`, ""); recorder.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, want 202", recorder.Code)
	}
	if sends := waitForMail(t, mailer, 1); len(sends) != 1 {
		t.Errorf("retry sent = %d messages, want 1", len(sends))
	}
}

func TestForgotPasswordDoesNotWaitForDelivery(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	release := make(chan struct{})
	mailer := &fakeResetMailer{block: release}
	handler := forgotPasswordHandler(store, mailer)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- apiRequest(t, handler, http.MethodPost, "/api/password/forgot",
			`{"email":"`+testEmail+`"}`, "")
	}()

	select {
	case recorder := <-done:
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202", recorder.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the forgot response waited for the mail send")
	}

	close(release)
	waitForMail(t, mailer, 1)
}

func TestForgotPasswordReportsNotConfigured(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	handler := NewServer(nil, WithUsers(store), WithAPIKey("shared-key")).Handler()

	recorder := apiRequest(t, handler, http.MethodPost, "/api/password/forgot",
		`{"email":"`+testEmail+`"}`, "")
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestForgotPasswordRejectsMalformedEmail(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	handler := forgotPasswordHandler(store, &fakeResetMailer{})

	recorder := apiRequest(t, handler, http.MethodPost, "/api/password/forgot", `{"email":"nope"}`, "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body)
	}
}

// resetPasswordHandler wires a store, a mailer and the forgot route so a test
// can obtain a real code and then spend it.
func resetPasswordHandler(t *testing.T, store *fakeUsers) (http.Handler, *fakeResetMailer) {
	t.Helper()
	mailer := &fakeResetMailer{}
	handler := NewServer(nil,
		WithUsers(store),
		WithAPIKey("shared-key"),
		WithPasswordReset(mailer),
	).Handler()
	recorder := apiRequest(t, handler, http.MethodPost, "/api/password/forgot",
		`{"email":"`+testEmail+`"}`, "")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("forgot status = %d, want 202", recorder.Code)
	}
	sends := waitForMail(t, mailer, 1)
	if len(sends) != 1 {
		t.Fatalf("forgot sent %d messages, want 1", len(sends))
	}
	return handler, mailer
}

func TestResetPassword(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, store, 1)
	ctx := context.Background()
	if _, _, err := store.IssueTokenForUser(ctx, 1, []string{"*"}, nil); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	if _, err := store.RecordLoginFailure(ctx, 1, 5, time.Minute); err != nil {
		t.Fatalf("seed failure: %v", err)
	}

	handler, mailer := resetPasswordHandler(t, store)
	token := mailer.sent()[0].token
	body := `{"token":"` + token + `","password":"a new password"}`
	recorder := apiRequest(t, handler, http.MethodPost, "/api/password/reset", body, "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", recorder.Code, recorder.Body)
	}

	credential, err := store.CredentialByID(ctx, 1)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if !auth.VerifyPassword(credential.PasswordHash, "a new password") {
		t.Error("the reset password does not verify")
	}
	if tokens := store.tokens[1]; len(tokens) != 0 {
		t.Errorf("tokens = %d after a reset, want 0", len(tokens))
	}
	if len(store.resets) != 0 {
		t.Errorf("reset codes left = %d, want 0", len(store.resets))
	}
	store.mu.Lock()
	failed := store.failed[1]
	store.mu.Unlock()
	if failed != 0 {
		t.Errorf("failed logins = %d, want 0", failed)
	}

	// The code works once: the second attempt cannot use it again.
	again := apiRequest(t, handler, http.MethodPost, "/api/password/reset", body, "")
	if again.Code != http.StatusBadRequest {
		t.Errorf("reused code status = %d, want 400", again.Code)
	}
}

func TestResetPasswordRejectsUnknownCode(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	handler := NewServer(nil, WithUsers(store), WithAPIKey("shared-key"),
		WithPasswordReset(&fakeResetMailer{})).Handler()

	recorder := apiRequest(t, handler, http.MethodPost, "/api/password/reset",
		`{"token":"prt_unknown","password":"a new password"}`, "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body)
	}
	if hash := store.passwords[1]; hash != "" {
		t.Error("an unknown code changed the password")
	}
}

func TestResetPasswordKeepsCodeForRejectedPassword(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	handler, mailer := resetPasswordHandler(t, store)
	token := mailer.sent()[0].token

	body := `{"token":"` + token + `","password":"` + strings.Repeat("a", 73) + `"}`
	if recorder := apiRequest(t, handler, http.MethodPost, "/api/password/reset", body, ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if _, ok := store.resets[auth.HashToken(token)]; !ok {
		t.Fatal("a rejected password consumed the code")
	}

	good := `{"token":"` + token + `","password":"a new password"}`
	if recorder := apiRequest(t, handler, http.MethodPost, "/api/password/reset", good, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("valid reset after rejection = %d, want 204", recorder.Code)
	}
}

func loginLockoutHandler(store UserStore, maxFailures int, lockFor time.Duration) http.Handler {
	return NewServer(nil,
		WithUsers(store),
		WithAPIKey("shared-key"),
		WithLoginLockout(maxFailures, lockFor),
	).Handler()
}

func TestLoginLocksAfterRepeatedFailures(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, store, 1)
	handler := loginLockoutHandler(store, 2, 10*time.Minute)

	wrong := `{"email":"` + testEmail + `","password":"wrong"}`
	if recorder := apiRequest(t, handler, http.MethodPost, "/api/login", wrong, ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("first failure status = %d, want 401", recorder.Code)
	}
	recorder := apiRequest(t, handler, http.MethodPost, "/api/login", wrong, "")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("second failure status = %d, want 429 (body %s)", recorder.Code, recorder.Body)
	}
	retryAfter, err := strconv.Atoi(recorder.Header().Get("Retry-After"))
	if err != nil || retryAfter <= 0 || retryAfter > 600 {
		t.Errorf("Retry-After = %q", recorder.Header().Get("Retry-After"))
	}

	// Even the right password is refused while the lock lasts.
	correct := `{"email":"` + testEmail + `","password":"` + testPassword + `"}`
	if recorder := apiRequest(t, handler, http.MethodPost, "/api/login", correct, ""); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("login during lock = %d, want 429", recorder.Code)
	}
	if tokens := store.tokens[1]; len(tokens) != 0 {
		t.Errorf("a locked account minted %d tokens", len(tokens))
	}

	// Once the deadline passes the account is usable again, and the failure
	// counter starts from zero.
	store.mu.Lock()
	store.locks[1] = time.Now().Add(-time.Second)
	store.mu.Unlock()
	if recorder := apiRequest(t, handler, http.MethodPost, "/api/login", correct, ""); recorder.Code != http.StatusOK {
		t.Fatalf("login after lock expiry = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
	store.mu.Lock()
	failed := store.failed[1]
	store.mu.Unlock()
	if failed != 0 {
		t.Errorf("failed logins after success = %d, want 0", failed)
	}
}

func TestLoginLockoutCanBeDisabled(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, store, 1)
	handler := loginLockoutHandler(store, 0, time.Minute)

	wrong := `{"email":"` + testEmail + `","password":"wrong"}`
	for i := 0; i < 6; i++ {
		if recorder := apiRequest(t, handler, http.MethodPost, "/api/login", wrong, ""); recorder.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status = %d, want 401", i+1, recorder.Code)
		}
	}
}

func TestChangePasswordRouteDoesNotShadowUserUpdate(t *testing.T) {
	// PUT /api/users/{id} and PUT /api/users/me/password live side by side, so
	// the account update route has to keep working.
	store := newFakeUsers(sampleUser(1, testEmail))
	handler := adminUsersHandler(store, nil)
	if recorder := apiRequest(t, handler, http.MethodPut, "/api/users/1", `{"name":"renamed"}`, testAdminKey); recorder.Code != http.StatusOK {
		t.Fatalf("PUT /api/users/1 = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}
}

func TestAdminSetPasswordClearsLockAndResetCodes(t *testing.T) {
	store := newFakeUsers(sampleUser(1, testEmail))
	withPassword(t, store, 1)
	ctx := context.Background()
	if _, err := store.RecordLoginFailure(ctx, 1, 1, 10*time.Minute); err != nil {
		t.Fatalf("seed failure: %v", err)
	}
	if err := store.CreatePasswordReset(ctx, 1, "stale-hash", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("seed reset: %v", err)
	}
	handler := adminUsersHandler(store, nil)

	recorder := apiRequest(t, handler, http.MethodPut, "/api/users/1",
		`{"password":"a new password"}`, testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body)
	}

	store.mu.Lock()
	failed := store.failed[1]
	_, locked := store.locks[1]
	resets := len(store.resets)
	store.mu.Unlock()
	if failed != 0 || locked {
		t.Errorf("failed = %d, locked = %v; want an open account", failed, locked)
	}
	if resets != 0 {
		t.Errorf("outstanding reset codes = %d, want 0", resets)
	}
	if !auth.VerifyPassword(store.passwords[1], "a new password") {
		t.Error("the admin password was not stored")
	}
}
