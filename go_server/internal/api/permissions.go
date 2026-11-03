package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

const maxPermissionBody = 1 << 16

// PermissionStore is what the permission routes need. The postgres store
// satisfies it: permissions.Store carries the pairs, and Groups backs the
// existence check POST /api/share/group does.
type PermissionStore interface {
	permissions.Store
	Groups(ctx context.Context, filter model.ResourceFilter) ([]*model.Group, error)
}

// WithPermissions enables GET/POST/DELETE /api/permissions and the share
// routes. Without it they report 501.
func WithPermissions(store PermissionStore) Option {
	return func(s *Server) {
		if store != nil {
			s.permissionStore = store
			s.permissions = permissions.NewService(store)
		}
	}
}

// WithShareDisabled refuses POST /api/share/*. It is the switch an operator
// turns off when public share links are not wanted, the counterpart of the Java
// server's web.deviceShareDisable.
func WithShareDisabled(disabled bool) Option {
	return func(s *Server) { s.shareDisabled = disabled }
}

// handlePermissions serves GET, POST and DELETE /api/permissions. A permission
// is a two key object such as {"userId":3,"deviceId":7}; the first key is the
// owner. GET answers a bare JSON array, which is what traccar-web reads.
func (s *Server) handlePermissions(w http.ResponseWriter, r *http.Request) {
	if s.permissions == nil {
		writeError(w, http.StatusNotImplemented, errors.New("permissions are not configured"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.listPermissions(w, r)
	case http.MethodPost:
		s.changePermissions(w, r, true)
	case http.MethodDelete:
		s.changePermissions(w, r, false)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

// listPermissions answers GET /api/permissions?userId=3&deviceId=7. One of the
// two may be left out, which makes it a wildcard: ?userId=3 lists everything
// that account was granted.
func (s *Server) listPermissions(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeReadPermissions) {
		return
	}

	owner, ownerID, property, propertyID, err := permissionQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	if err := s.checkPairAccess(ctx, r, owner, ownerID, property, propertyID); err != nil {
		writePermissionError(w, err)
		return
	}

	pairs, err := s.permissions.Permissions(ctx, owner, ownerID, property, propertyID)
	if err != nil {
		s.logf("api: query permissions: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if pairs == nil {
		pairs = []permissions.Permission{}
	}
	writeJSON(w, http.StatusOK, pairs)
}

// changePermissions serves the single object and the bulk array body of POST
// and DELETE. Both answer 204 on success, like the Java resource.
func (s *Server) changePermissions(w http.ResponseWriter, r *http.Request, add bool) {
	if !s.authorize(w, r, auth.ScopeWritePermissions) {
		return
	}

	pairs, err := decodePermissions(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	// Every pair is checked before the first write, so a rejected entry leaves
	// the stored permissions untouched.
	for _, pair := range pairs {
		if err := s.checkPairAccess(ctx, r, pair.Owner, pair.OwnerID, pair.Property, pair.PropertyID); err != nil {
			writePermissionError(w, err)
			return
		}
	}

	for _, pair := range pairs {
		if add {
			err = s.permissions.Grant(ctx, pair)
		} else {
			err = s.permissions.Revoke(ctx, pair)
		}
		if err != nil {
			if errors.Is(err, permissions.ErrInvalidPermission) {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			s.logf("api: change permission %s: %v", pair, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
		action := model.ActionLink
		if !add {
			action = model.ActionUnlink
		}
		s.auditWithAttributes(r, action, string(pair.Property), pair.PropertyID,
			model.Attributes{
				"ownerType": string(pair.Owner),
				"ownerId":   pair.OwnerID,
			})
	}

	w.WriteHeader(http.StatusNoContent)
}

// checkPairAccess is the "越權一律 403" rule: a non-admin may only read or change
// a pair when it can reach both ends of it.
func (s *Server) checkPairAccess(ctx context.Context, r *http.Request, owner permissions.Object, ownerID int64, property permissions.Object, propertyID int64) error {
	if s.isAdmin(r) {
		return nil
	}
	principal := auth.FromContext(r.Context())
	var userID int64
	if principal != nil {
		userID = principal.UserID
	}
	if userID == 0 {
		return fmt.Errorf("%w: the shared API key is not an account", permissions.ErrAccessDenied)
	}

	// A wildcard end would widen the answer to other accounts' grants, so only
	// the caller's own grants may be listed with one.
	if ownerID == 0 || propertyID == 0 {
		if owner == permissions.ObjectUser && ownerID == userID {
			return nil
		}
		return fmt.Errorf("%w: a wildcard list is admin only", permissions.ErrAccessDenied)
	}

	for _, end := range []struct {
		object permissions.Object
		id     int64
	}{{owner, ownerID}, {property, propertyID}} {
		ok, err := s.permissions.CanAccess(ctx, userID, false, end.object, end.id)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: %s %d", permissions.ErrAccessDenied, end.object, end.id)
		}
	}
	return nil
}

// writePermissionError maps the permission errors onto status codes.
func writePermissionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, permissions.ErrAccessDenied):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, permissions.ErrInvalidPermission):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
	}
}

// permissionQuery reads the object parameters of a GET in the order they appear
// in the query string. The order is the contract: the first one is the owner,
// exactly like a permission body.
func permissionQuery(rawQuery string) (owner permissions.Object, ownerID int64, property permissions.Object, propertyID int64, err error) {
	type parameter struct {
		object permissions.Object
		id     int64
	}
	parameters := make([]parameter, 0, 2)

	for _, field := range strings.Split(rawQuery, "&") {
		if field == "" {
			continue
		}
		key, rawValue, _ := strings.Cut(field, "=")
		if !strings.HasSuffix(key, "Id") {
			continue
		}
		if decoded, decodeErr := url.QueryUnescape(rawValue); decodeErr == nil {
			rawValue = decoded
		}
		object, parseErr := permissions.ParseObject(key)
		if parseErr != nil {
			return "", 0, "", 0, parseErr
		}
		var id int64
		if rawValue != "" {
			id, parseErr = strconv.ParseInt(rawValue, 10, 64)
			if parseErr != nil || id <= 0 {
				return "", 0, "", 0, fmt.Errorf("%w: %s must be a positive id", permissions.ErrInvalidPermission, key)
			}
		}
		parameters = append(parameters, parameter{object: object, id: id})
	}

	switch len(parameters) {
	case 1:
		return parameters[0].object, parameters[0].id, "", 0, nil
	case 2:
		if parameters[0].object == parameters[1].object {
			return "", 0, "", 0, fmt.Errorf("%w: %s cannot be paired with itself", permissions.ErrInvalidPermission, parameters[0].object)
		}
		return parameters[0].object, parameters[0].id, parameters[1].object, parameters[1].id, nil
	default:
		return "", 0, "", 0, fmt.Errorf("%w: expected 1 or 2 object parameters, got %d", permissions.ErrInvalidPermission, len(parameters))
	}
}

// decodePermissions reads the body of POST/DELETE /api/permissions: one object
// or an array of them, all with the same key set.
func decodePermissions(w http.ResponseWriter, r *http.Request) ([]permissions.Permission, error) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPermissionBody))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid body: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("invalid body: trailing data after the permission body")
	}

	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var bodies []json.RawMessage
		if err := json.Unmarshal(raw, &bodies); err != nil {
			return nil, fmt.Errorf("invalid body: %w", err)
		}
		if len(bodies) == 0 {
			return nil, fmt.Errorf("%w: the body is empty", permissions.ErrInvalidPermission)
		}
		pairs := make([]permissions.Permission, 0, len(bodies))
		for _, body := range bodies {
			pair, err := decodePermission(body)
			if err != nil {
				return nil, err
			}
			pairs = append(pairs, pair)
		}
		// A bulk request is one kind of link repeated; mixing shapes would make
		// a partial failure impossible to describe.
		for _, pair := range pairs[1:] {
			if pair.Owner != pairs[0].Owner || pair.Property != pairs[0].Property {
				return nil, fmt.Errorf("%w: every entry must use the same object types", permissions.ErrInvalidPermission)
			}
		}
		return pairs, nil
	}

	pair, err := decodePermission(raw)
	if err != nil {
		return nil, err
	}
	return []permissions.Permission{pair}, nil
}

func decodePermission(body []byte) (permissions.Permission, error) {
	var pair permissions.Permission
	if err := json.Unmarshal(body, &pair); err != nil {
		return permissions.Permission{}, err
	}
	return pair, nil
}

// shareScopes is what a share link may do: read what was shared, nothing else.
var shareScopes = []string{
	string(auth.ScopeReadPositions),
	string(auth.ScopeReadDevices),
	string(auth.ScopeReadAlerts),
}

type shareResponse struct {
	Token     string     `json:"token"`
	UserID    int64      `json:"userId"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// handleShareDevice and handleShareGroup trade a device or a group for a read
// only token and a temporary account that owns it. The shape follows the Java
// ShareResource: the account is created once, and asking for the same share
// again answers 409.
func (s *Server) handleShareDevice(w http.ResponseWriter, r *http.Request) {
	s.share(w, r, permissions.ObjectDevice)
}

func (s *Server) handleShareGroup(w http.ResponseWriter, r *http.Request) {
	s.share(w, r, permissions.ObjectGroup)
}

func (s *Server) share(w http.ResponseWriter, r *http.Request, object permissions.Object) {
	if !s.usersConfigured(w) {
		return
	}
	if s.permissions == nil {
		writeError(w, http.StatusNotImplemented, errors.New("permissions are not configured"))
		return
	}
	if !s.authorize(w, r, auth.ScopeWriteShare) {
		return
	}
	if s.shareDisabled {
		writeError(w, http.StatusForbidden, errors.New("sharing is disabled"))
		return
	}

	// The share account is named after the caller's address, so a caller
	// without an account (the legacy shared key) cannot share at all.
	principal := auth.FromContext(r.Context())
	if !s.authEnabled() || principal == nil || principal.UserID == 0 {
		writeError(w, http.StatusForbidden, errors.New("sharing needs a user account"))
		return
	}
	userID := principal.UserID

	request, err := parseShareRequest(w, r, object, s.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	owner, err := s.users.User(ctx, userID)
	if err != nil {
		s.writeUserLookupError(w, userID, err)
		return
	}
	if owner.Temporary {
		writeError(w, http.StatusForbidden, errors.New("a share account cannot share"))
		return
	}

	target, err := s.shareTarget(ctx, object, request.objectID)
	if err != nil {
		if errors.Is(err, model.ErrDeviceNotFound) || errors.Is(err, model.ErrResourceNotFound) {
			writeError(w, http.StatusNotFound, fmt.Errorf("unknown %s %d", object, request.objectID))
			return
		}
		s.logf("api: share %s %d: %v", object, request.objectID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	allowed, err := s.permissions.CanAccess(ctx, userID, owner.Admin, object, request.objectID)
	if err != nil {
		s.logf("api: check share access: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if !allowed {
		writePermissionError(w, fmt.Errorf("%w: %s %d", permissions.ErrAccessDenied, object, request.objectID))
		return
	}

	email := shareEmail(owner.Email, object, request.objectID, target.uniqueID)
	existing, err := s.users.Users(ctx, model.UserFilter{Email: email, Limit: 1})
	if err != nil {
		s.logf("api: lookup share account: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if len(existing) > 0 {
		writeError(w, http.StatusConflict, fmt.Errorf("this %s was already shared", object))
		return
	}

	account, err := s.users.CreateUser(ctx, model.UserCreate{
		Email:      email,
		Name:       target.name,
		Temporary:  true,
		Attributes: shareAttributes(request.expiration),
	})
	if err != nil {
		if errors.Is(err, model.ErrDuplicateUser) {
			writeError(w, http.StatusConflict, fmt.Errorf("this %s was already shared", object))
			return
		}
		s.logf("api: create share account: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	pair, err := permissions.New(permissions.ObjectUser, account.ID, object, request.objectID)
	if err != nil {
		s.logf("api: share pair: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if err := s.permissions.Grant(ctx, pair); err != nil {
		s.logf("api: grant share %s: %v", pair, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	token, plaintext, err := s.users.IssueTokenForUser(ctx, account.ID, shareScopes, request.expiration)
	if err != nil {
		s.logf("api: issue share token: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	response := shareResponse{Token: plaintext, UserID: account.ID}
	if token.ExpiresAt != nil {
		expiresAt := token.ExpiresAt.UTC()
		response.ExpiresAt = &expiresAt
	}
	writeJSON(w, http.StatusOK, response)
}

// shareAttributes is what a share account stores on the row. The expiration is
// kept as the users API spells it, an ISO 8601 timestamp under expirationTime,
// so Expiration and the temporary cleanup read the same key. A share without an
// expiration has no attribute and is never deleted.
func shareAttributes(expiration *time.Time) model.Attributes {
	if expiration == nil {
		return nil
	}
	return model.Attributes{"expirationTime": expiration.UTC().Format(time.RFC3339)}
}

// shareTarget is the display name of the shared object plus, for a device, the
// unique id the share address is built from.
type shareTarget struct {
	name     string
	uniqueID string
}

// shareTarget resolves and verifies the shared object.
func (s *Server) shareTarget(ctx context.Context, object permissions.Object, objectID int64) (shareTarget, error) {
	if object == permissions.ObjectDevice {
		if s.devices == nil {
			return shareTarget{}, errors.New("devices are not configured")
		}
		devices, err := s.devices.Devices(ctx, model.DeviceFilter{DeviceIDs: []int64{objectID}, Limit: 1})
		if err != nil {
			return shareTarget{}, err
		}
		if len(devices) == 0 {
			return shareTarget{}, model.ErrDeviceNotFound
		}
		return shareTarget{name: devices[0].Name, uniqueID: devices[0].UniqueID}, nil
	}

	groups, err := s.permissionStore.Groups(ctx, model.ResourceFilter{IDs: []int64{objectID}, Limit: 1})
	if err != nil {
		return shareTarget{}, err
	}
	if len(groups) == 0 {
		return shareTarget{}, model.ErrResourceNotFound
	}
	return shareTarget{name: groups[0].Name}, nil
}

// shareRequest is the form body of a share call.
type shareRequest struct {
	objectID   int64
	expiration *time.Time
}

// parseShareRequest reads the form body: an id and an optional expiration in
// RFC3339 or as a bare date (midnight UTC).
func parseShareRequest(w http.ResponseWriter, r *http.Request, object permissions.Object, now time.Time) (shareRequest, error) {
	if err := r.ParseForm(); err != nil {
		return shareRequest{}, fmt.Errorf("invalid form: %w", err)
	}
	rawID := r.PostFormValue(object.Key())
	if rawID == "" {
		// A share link is built for one object; a bare "id" is accepted as a
		// convenience for hand written calls.
		rawID = r.PostFormValue("id")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil || id <= 0 {
		return shareRequest{}, fmt.Errorf("%w: %s must be a positive id", permissions.ErrInvalidPermission, object.Key())
	}

	request := shareRequest{objectID: id}
	raw := strings.TrimSpace(r.PostFormValue("expiration"))
	if raw == "" {
		return request, nil
	}
	expiration, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		expiration, err = time.Parse("2006-01-02", raw)
		if err != nil {
			return shareRequest{}, fmt.Errorf("invalid expiration %q", raw)
		}
	}
	expiration = expiration.UTC()
	if !expiration.After(now) {
		return shareRequest{}, errors.New("expiration must be in the future")
	}
	request.expiration = &expiration
	return request, nil
}

// shareEmail builds the address that identifies a share account. A device uses
// its unique id, which survives a rename, so the same device always maps to the
// same account.
func shareEmail(ownerEmail string, object permissions.Object, objectID int64, uniqueID string) string {
	switch object {
	case permissions.ObjectDevice:
		if uniqueID != "" {
			return ownerEmail + ":" + uniqueID
		}
		return fmt.Sprintf("%s:device:%d", ownerEmail, objectID)
	default:
		return fmt.Sprintf("%s:%s:%d", ownerEmail, object, objectID)
	}
}
