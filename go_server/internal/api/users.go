package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

const (
	maxUserBody  = 1 << 16
	maxUserLimit = 1000
)

// UserStore is the storage contract for the user administration routes. It is
// deliberately separate from DeviceStore so a deployment can expose devices
// without exposing accounts.
type UserStore interface {
	Users(ctx context.Context, filter model.UserFilter) ([]model.User, error)
	User(ctx context.Context, userID int64) (*model.User, error)
	CreateUser(ctx context.Context, create model.UserCreate) (*model.User, error)
	UpdateUser(ctx context.Context, userID int64, update model.UserUpdate) (*model.User, error)
	DeleteUser(ctx context.Context, userID int64) error
	UserDevices(ctx context.Context, userID int64) ([]int64, error)
	AssignDevice(ctx context.Context, userID, deviceID int64) error
	UnassignDevice(ctx context.Context, userID, deviceID int64) error
	Tokens(ctx context.Context, userID int64) ([]model.APIToken, error)
	IssueTokenForUser(ctx context.Context, userID int64, scopes []string, expiresAt *time.Time) (*model.APIToken, string, error)
	RevokeToken(ctx context.Context, userID, tokenID int64) error
	// CredentialByEmail backs POST /api/login and SetPassword backs the
	// password field of PUT /api/users/{id}. CredentialByID backs a caller
	// changing its own password. The hash never leaves storage as part of a
	// model.User.
	CredentialByEmail(ctx context.Context, email string) (*auth.Credential, error)
	CredentialByID(ctx context.Context, userID int64) (*auth.Credential, error)
	SetPassword(ctx context.Context, userID int64, passwordHash string) error
	// RecordLoginFailure counts one wrong password and reports the lock
	// deadline once the account reaches the threshold. A nil deadline means the
	// account is still open. ResetLoginFailures clears both after a success.
	RecordLoginFailure(ctx context.Context, userID int64, threshold int, lockFor time.Duration) (*time.Time, error)
	ResetLoginFailures(ctx context.Context, userID int64) error
	// CreatePasswordReset stores the hash of a reset code, ConsumePasswordReset
	// trades an unused code for an account id, and DeletePasswordResets drops
	// the outstanding codes of an account.
	CreatePasswordReset(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error
	ConsumePasswordReset(ctx context.Context, tokenHash string) (int64, error)
	DeletePasswordResets(ctx context.Context, userID int64) error
	// DeletePasswordReset drops exactly one code, so a failed delivery does not
	// remove a newer code the account requested meanwhile.
	DeletePasswordReset(ctx context.Context, tokenHash string) error
	// RevokeTokensExcept removes every token of a user except keepTokenID. A
	// keepTokenID of zero removes every token.
	RevokeTokensExcept(ctx context.Context, userID, keepTokenID int64) error
}

type userResponse struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	// Admin is the name this API used first; Administrator is the one
	// traccar-web reads, so both carry the same value.
	Admin          bool `json:"admin"`
	Administrator  bool `json:"administrator"`
	Disabled       bool `json:"disabled"`
	Readonly       bool `json:"readonly"`
	DeviceReadonly bool `json:"deviceReadonly"`
	LimitCommands  bool `json:"limitCommands"`
	DisableReports bool `json:"disableReports"`
	FixedEmail     bool `json:"fixedEmail"`
	Temporary      bool `json:"temporary"`
	// DeviceLimit caps the devices the account may hold; -1 is no cap, 0
	// forbids holding any. UserLimit is stored for the Java client but gates
	// nothing, because only administrators create accounts here.
	DeviceLimit    int            `json:"deviceLimit"`
	UserLimit      int            `json:"userLimit"`
	ExpirationTime *time.Time     `json:"expirationTime,omitempty"`
	Attributes     map[string]any `json:"attributes"`
	DeviceCount    int            `json:"deviceCount"`
	TokenCount     int            `json:"tokenCount"`
	CreatedAt      time.Time      `json:"createdAt"`
}

type tokenResponse struct {
	ID        int64      `json:"id"`
	UserID    int64      `json:"userId"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

type tokensResponse struct {
	Tokens []tokenResponse `json:"tokens"`
	Count  int             `json:"count"`
}

// tokenIssueResponse is the only place the plaintext token ever appears.
type tokenIssueResponse struct {
	tokenResponse
	Token string `json:"token"`
}

type userCreateRequest struct {
	Email string `json:"email"`
	Name  string `json:"name"`
	// Admin and Administrator are the two spellings of the same flag;
	// traccar-web posts the latter.
	Admin         bool  `json:"admin"`
	Administrator *bool `json:"administrator"`
	// Password is optional: a share link creates an account without one, while
	// registration and the admin dialog set it.
	Password string `json:"password"`
	// DeviceLimit and UserLimit are administrator only; nil means no cap.
	DeviceLimit *int `json:"deviceLimit"`
	UserLimit   *int `json:"userLimit"`
}

// userUpdateRequest is a partial update: absent fields stay unchanged.
type userUpdateRequest struct {
	Name *string `json:"name"`
	// Admin and Administrator are the two spellings of the same flag;
	// traccar-web posts the latter.
	Admin         *bool `json:"admin"`
	Administrator *bool `json:"administrator"`
	Disabled      *bool `json:"disabled"`
	// Attributes replaces the account attribute map, which is where the web
	// interface keeps per-user preferences.
	Attributes *map[string]any `json:"attributes"`
	// Password is written straight to users.password_hash; it is never echoed
	// back and an empty string is rejected rather than stored.
	Password *string `json:"password"`
	// DeviceLimit and UserLimit are administrator only.
	DeviceLimit *int `json:"deviceLimit"`
	UserLimit   *int `json:"userLimit"`
}

type tokenIssueRequest struct {
	Scopes []string `json:"scopes"`
	// ExpiresIn is a Go duration such as "720h"; empty means "never expires".
	ExpiresIn string `json:"expiresIn"`
}

func newUserResponse(user model.User) userResponse {
	attributes := map[string]any(user.Attributes)
	if attributes == nil {
		attributes = map[string]any{}
	}
	response := userResponse{
		ID:            user.ID,
		Email:         user.Email,
		Name:          user.Name,
		Admin:         user.Admin,
		Administrator: user.Admin,
		Disabled:      user.Disabled,
		Temporary:     user.Temporary,
		Attributes:    attributes,
		DeviceCount:   user.DeviceCount,
		TokenCount:    user.TokenCount,
		DeviceLimit:   user.DeviceLimit,
		UserLimit:     user.UserLimit,
		CreatedAt:     user.CreatedAt.UTC(),
	}
	// Share accounts keep their deadline in attributes, which is what the
	// scheduled cleanup reads.
	if raw := user.Attributes.GetString("expirationTime", ""); raw != "" {
		if expires, err := time.Parse(time.RFC3339, raw); err == nil {
			expires = expires.UTC()
			response.ExpirationTime = &expires
		}
	}
	return response
}

func newTokenResponse(token model.APIToken) tokenResponse {
	scopes := token.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	response := tokenResponse{
		ID:        token.ID,
		UserID:    token.UserID,
		Scopes:    scopes,
		CreatedAt: token.CreatedAt.UTC(),
	}
	if token.ExpiresAt != nil {
		expiresAt := token.ExpiresAt.UTC()
		response.ExpiresAt = &expiresAt
	}
	return response
}

// usersConfigured reports whether the user routes are mounted.
func (s *Server) usersConfigured(w http.ResponseWriter) bool {
	if s.users == nil {
		writeError(w, http.StatusNotImplemented, errors.New("users are not configured"))
		return false
	}
	return true
}

func (s *Server) devicesConfigured(w http.ResponseWriter) bool {
	if s.devices == nil {
		writeError(w, http.StatusNotImplemented, errors.New("devices are not configured"))
		return false
	}
	return true
}

// requireAdmin guards the account routes. They are not scope based: only an
// administrator may see or change accounts, which also keeps a token holding
// "*" from escalating itself.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.isAdmin(r) {
		return true
	}
	writeError(w, http.StatusForbidden, errors.New("admin privileges are required"))
	return false
}

// isSelf reports whether the caller is the account in the path. The legacy
// shared key has no user id, so it never matches.
func (s *Server) isSelf(r *http.Request, userID int64) bool {
	principal := auth.FromContext(r.Context())
	return principal != nil && principal.UserID != 0 && principal.UserID == userID
}

// requireTokenAccess guards listing and revoking the tokens of one account. An
// administrator reaches every account; a non-admin reaches only its own, and
// only with a token carrying "*", so a narrowly scoped token cannot enumerate
// or revoke the account's other credentials.
//
// Issuing a token is not included: that stays admin-only, because a caller
// could otherwise mint scopes it does not hold itself.
func (s *Server) requireTokenAccess(w http.ResponseWriter, r *http.Request, userID int64) bool {
	if s.isAdmin(r) {
		return true
	}
	principal := auth.FromContext(r.Context())
	if s.isSelf(r, userID) && principal.Has(auth.ScopeAll) {
		return true
	}
	writeError(w, http.StatusForbidden,
		errors.New(`only an administrator, or a "*" token of that account, may list or revoke its tokens`))
	return false
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) || !s.usersConfigured(w) {
		return
	}

	query := r.URL.Query()
	limit, err := parseLimit(query, maxUserLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	offset, err := parseOffset(query)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	users, err := s.users.Users(ctx, model.UserFilter{Limit: limit, Offset: offset})
	if err != nil {
		s.logf("api: query users: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	response := make([]userResponse, 0, len(users))
	for _, user := range users {
		response = append(response, newUserResponse(user))
	}
	writeJSON(w, http.StatusOK, response)
}

// handleUser serves GET /api/users/{id}. traccar-web fetches the account it is
// about to edit, and an ordinary user may read only its own record.
func (s *Server) handleUser(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) {
		return
	}
	userID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.isAdmin(r) && !s.isSelf(r, userID) {
		writeError(w, http.StatusForbidden, errors.New("admin privileges are required"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	user, err := s.users.User(ctx, userID)
	if err != nil {
		s.writeUserLookupError(w, userID, err)
		return
	}
	writeJSON(w, http.StatusOK, newUserResponse(*user))
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) {
		return
	}

	var request userCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	email := strings.TrimSpace(request.Email)
	if !validEmail(email) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid email %q", email))
		return
	}

	var passwordHash string

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	// The Java server lets an anonymous caller create the very first account,
	// which becomes an administrator, and allows further accounts while
	// server.registration is on. That is what makes the web form work.
	firstUser := s.newServer(ctx)
	if !firstUser && !s.isAdmin(r) {
		registration, err := s.registrationEnabled(ctx)
		if err != nil {
			s.logf("api: read registration setting: %v", err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
		if !registration {
			writeError(w, http.StatusForbidden, errors.New("admin privileges are required"))
			return
		}
	}

	admin := request.Admin
	if request.Administrator != nil {
		admin = *request.Administrator
	}
	// Only an administrator may hand out the flag; the first account always
	// gets it.
	if !s.isAdmin(r) {
		admin = firstUser
	}

	if request.Password != "" {
		hash, err := auth.HashPassword(request.Password)
		if errors.Is(err, auth.ErrEmptyPassword) || errors.Is(err, auth.ErrPasswordTooLong) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err != nil {
			s.logf("api: hash password for %q: %v", email, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
		passwordHash = hash
	}

	create := model.UserCreate{
		Email: email,
		Name:  strings.TrimSpace(request.Name),
		Admin: admin,
	}
	if s.isAdmin(r) {
		create.DeviceLimit = request.DeviceLimit
		create.UserLimit = request.UserLimit
	}

	user, err := s.users.CreateUser(ctx, create)
	if errors.Is(err, model.ErrDuplicateUser) {
		writeError(w, http.StatusConflict, fmt.Errorf("user %q already exists", email))
		return
	}
	if err != nil {
		s.logf("api: create user %q: %v", email, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	if passwordHash != "" {
		if err := s.users.SetPassword(ctx, user.ID, passwordHash); err != nil {
			s.logf("api: set password of user %d: %v", user.ID, err)
			// Do not leave a half-created account behind.
			if deleteErr := s.users.DeleteUser(ctx, user.ID); deleteErr != nil {
				s.logf("api: roll back user %d: %v", user.ID, deleteErr)
			}
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
	}

	s.audit(r, model.ActionCreate, "user", user.ID)
	writeJSON(w, http.StatusCreated, newUserResponse(*user))
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) {
		return
	}

	userID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// An administrator edits any account; everybody else edits only its own,
	// which is how traccar-web saves the preferences dialog.
	self := s.isSelf(r, userID)
	if !self && !s.isAdmin(r) {
		writeError(w, http.StatusForbidden, errors.New("admin privileges are required"))
		return
	}

	var request userUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	admin := request.Admin
	if request.Administrator != nil {
		admin = request.Administrator
	}
	disabled := request.Disabled
	if !s.isAdmin(r) {
		// Editing your own profile must not grant powers or hide the account
		// from the administrator.
		admin = nil
		disabled = nil
	}
	if disabled != nil && *disabled && self {
		writeError(w, http.StatusConflict, errors.New("cannot disable your own account"))
		return
	}
	if admin != nil && !*admin && self {
		writeError(w, http.StatusConflict, errors.New("cannot revoke your own admin flag"))
		return
	}

	update := model.UserUpdate{Admin: admin, Disabled: disabled}
	if s.isAdmin(r) {
		update.DeviceLimit = request.DeviceLimit
		update.UserLimit = request.UserLimit
	}
	if request.Name != nil {
		name := strings.TrimSpace(*request.Name)
		update.Name = &name
	}
	if request.Attributes != nil {
		attributes := model.Attributes(*request.Attributes)
		update.Attributes = &attributes
	}

	// Hashed before the row is touched: bcrypt takes a noticeable moment, and a
	// rejected password must not leave the other fields already changed.
	var passwordHash string
	if request.Password != nil {
		hash, err := auth.HashPassword(*request.Password)
		if errors.Is(err, auth.ErrEmptyPassword) || errors.Is(err, auth.ErrPasswordTooLong) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err != nil {
			s.logf("api: hash password of user %d: %v", userID, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
		passwordHash = hash
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	user, err := s.users.UpdateUser(ctx, userID, update)
	if errors.Is(err, model.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown user %d", userID))
		return
	}
	if err != nil {
		s.logf("api: update user %d: %v", userID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	if passwordHash != "" {
		if err := s.users.SetPassword(ctx, userID, passwordHash); err != nil {
			s.logf("api: set password of user %d: %v", userID, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
		// A new password is also how an administrator unlocks an account that
		// exhausted its attempts, and it makes any mailed reset code obsolete.
		if err := s.users.ResetLoginFailures(ctx, userID); err != nil {
			s.logf("api: reset login failures of user %d: %v", userID, err)
		}
		if err := s.users.DeletePasswordResets(ctx, userID); err != nil {
			s.logf("api: delete password resets of user %d: %v", userID, err)
		}
	}

	s.audit(r, model.ActionEdit, "user", user.ID)
	writeJSON(w, http.StatusOK, newUserResponse(*user))
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) || !s.usersConfigured(w) {
		return
	}

	userID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.isSelf(r, userID) {
		writeError(w, http.StatusConflict, errors.New("cannot delete your own account"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	if err := s.users.DeleteUser(ctx, userID); err != nil {
		if errors.Is(err, model.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, fmt.Errorf("unknown user %d", userID))
			return
		}
		s.logf("api: delete user %d: %v", userID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	s.audit(r, model.ActionRemove, "user", userID)

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUserDevices(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) || !s.usersConfigured(w) || !s.devicesConfigured(w) {
		return
	}

	userID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	if _, err := s.users.User(ctx, userID); err != nil {
		s.writeUserLookupError(w, userID, err)
		return
	}

	deviceIDs, err := s.users.UserDevices(ctx, userID)
	if err != nil {
		s.logf("api: query user %d devices: %v", userID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	devices, err := s.devices.Devices(ctx, model.DeviceFilter{DeviceIDs: deviceIDs, Limit: maxDeviceLimit})
	if err != nil {
		s.logf("api: query devices of user %d: %v", userID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	response := make([]deviceResponse, 0, len(devices))
	for _, device := range devices {
		response = append(response, newDeviceResponse(device, true))
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleAssignDevice(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) || !s.usersConfigured(w) || !s.devicesConfigured(w) {
		return
	}

	userID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	deviceID, err := positiveID(r, "deviceId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	user, err := s.users.User(ctx, userID)
	if err != nil {
		s.writeUserLookupError(w, userID, err)
		return
	}
	exceeded, err := s.deviceLimitReached(ctx, user, deviceID)
	if err != nil {
		s.logf("api: check device limit of user %d: %v", userID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if exceeded != nil {
		writeError(w, http.StatusForbidden, exceeded)
		return
	}
	// AssignDevice would otherwise surface a foreign key violation as a 500.
	if ok, err := s.deviceExists(ctx, deviceID); err != nil {
		s.logf("api: lookup device %d: %v", deviceID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	} else if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown device %d", deviceID))
		return
	}

	if err := s.users.AssignDevice(ctx, userID, deviceID); err != nil {
		s.logf("api: assign device %d to user %d: %v", deviceID, userID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	s.auditWithAttributes(r, model.ActionLink, "device", deviceID,
		model.Attributes{"ownerType": "user", "ownerId": userID})

	w.WriteHeader(http.StatusNoContent)
}

// deviceLimitReached applies users.device_limit, the same rule the Java server
// uses: 0 forbids holding any device, a positive value caps the count, and a
// negative value is no cap. A device the account already holds is not counted
// again, so re-sending an assignment that is already in place still succeeds.
func (s *Server) deviceLimitReached(
	ctx context.Context, user *model.User, deviceID int64,
) (denied, failure error) {
	if user.DeviceLimit < 0 {
		return nil, nil
	}
	if user.DeviceLimit == 0 {
		return errors.New("this account may not hold devices"), nil
	}
	if user.DeviceCount < user.DeviceLimit {
		return nil, nil
	}

	assigned, err := s.users.UserDevices(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	for _, id := range assigned {
		if id == deviceID {
			return nil, nil
		}
	}
	return fmt.Errorf("the account already holds the limit of %d devices", user.DeviceLimit), nil
}

func (s *Server) handleUnassignDevice(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) || !s.usersConfigured(w) {
		return
	}

	userID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	deviceID, err := positiveID(r, "deviceId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	if _, err := s.users.User(ctx, userID); err != nil {
		s.writeUserLookupError(w, userID, err)
		return
	}
	if err := s.users.UnassignDevice(ctx, userID, deviceID); err != nil {
		s.logf("api: unassign device %d from user %d: %v", deviceID, userID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	s.auditWithAttributes(r, model.ActionUnlink, "device", deviceID,
		model.Attributes{"ownerType": "user", "ownerId": userID})

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) {
		return
	}

	userID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.requireTokenAccess(w, r, userID) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	if _, err := s.users.User(ctx, userID); err != nil {
		s.writeUserLookupError(w, userID, err)
		return
	}

	tokens, err := s.users.Tokens(ctx, userID)
	if err != nil {
		s.logf("api: query user %d tokens: %v", userID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	response := tokensResponse{Tokens: make([]tokenResponse, 0, len(tokens)), Count: len(tokens)}
	for _, token := range tokens {
		response.Tokens = append(response.Tokens, newTokenResponse(token))
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleIssueToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) || !s.usersConfigured(w) {
		return
	}

	userID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	var request tokenIssueRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBody)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	scopes, err := auth.ParseScopes(strings.Join(request.Scopes, ","))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	names := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		names = append(names, string(scope))
	}

	var expiresAt *time.Time
	if strings.TrimSpace(request.ExpiresIn) != "" {
		ttl, err := time.ParseDuration(request.ExpiresIn)
		if err != nil || ttl <= 0 {
			writeError(w, http.StatusBadRequest,
				fmt.Errorf("expiresIn must be a positive duration such as 720h, got %q", request.ExpiresIn))
			return
		}
		deadline := s.now().Add(ttl)
		expiresAt = &deadline
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	token, plaintext, err := s.users.IssueTokenForUser(ctx, userID, names, expiresAt)
	if errors.Is(err, model.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown user %d", userID))
		return
	}
	if err != nil {
		s.logf("api: issue token for user %d: %v", userID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	s.audit(r, model.ActionToken, "token", token.ID)
	writeJSON(w, http.StatusCreated, tokenIssueResponse{
		tokenResponse: newTokenResponse(*token),
		Token:         plaintext,
	})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	if !s.usersConfigured(w) {
		return
	}

	userID, err := positiveID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tokenID, err := positiveID(r, "tokenId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.requireTokenAccess(w, r, userID) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	if err := s.users.RevokeToken(ctx, userID, tokenID); err != nil {
		if errors.Is(err, model.ErrTokenNotFound) {
			writeError(w, http.StatusNotFound, fmt.Errorf("unknown token %d for user %d", tokenID, userID))
			return
		}
		s.logf("api: revoke token %d: %v", tokenID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	s.audit(r, model.ActionRemove, "token", tokenID)

	w.WriteHeader(http.StatusNoContent)
}

// writeUserLookupError maps a user lookup to 404 or 500.
func (s *Server) writeUserLookupError(w http.ResponseWriter, userID int64, err error) {
	if errors.Is(err, model.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown user %d", userID))
		return
	}
	s.logf("api: query user %d: %v", userID, err)
	writeError(w, http.StatusInternalServerError, errors.New("internal error"))
}

// deviceExists reports whether the device id is known. It reuses the device
// list contract instead of adding a one-row lookup to DeviceStore.
func (s *Server) deviceExists(ctx context.Context, deviceID int64) (bool, error) {
	devices, err := s.devices.Devices(ctx, model.DeviceFilter{
		DeviceIDs: []int64{deviceID},
		Limit:     1,
	})
	if err != nil {
		return false, err
	}
	return len(devices) > 0, nil
}

// validEmail is a deliberately loose check: an address needs an "@" and no
// whitespace. Anything stricter belongs to a verification flow.
func validEmail(email string) bool {
	if email == "" || strings.ContainsAny(email, " \t\r\n") {
		return false
	}
	at := strings.LastIndex(email, "@")
	return at > 0 && at < len(email)-1
}

// positiveID parses a positive integer path value.
func positiveID(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid %s %q", name, raw)
	}
	return id, nil
}
