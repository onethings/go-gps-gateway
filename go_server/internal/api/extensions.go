package api

import (
	"context"
	"errors"
	"net/http"

	"traccar-server_go/go_server/internal/model"
)

// AttributeStore reads the computed attribute definitions. The test route uses
// it to show what an expression produces before an operator saves it.
type AttributeStore interface {
	Attributes(ctx context.Context, filter model.ResourceFilter) ([]*model.Attribute, error)
}

// DeviceTOTPStore keeps the two factor secret of an account. The secret never
// leaves the server: the generation route returns it once so the user can scan
// it, and the only way to use it afterwards is a valid code.
type DeviceTOTPStore interface {
	UserTOTP(ctx context.Context, userID int64) (secret string, enabled bool, err error)
	SetUserTOTP(ctx context.Context, userID int64, secret string, enabled bool) error
}

// WithComputedAttributes enables POST /api/attributes/computed/test. Without
// it the route reports 501.
func WithComputedAttributes(store AttributeStore) Option {
	return func(s *Server) { s.computedAttributes = store }
}

// WithDeviceImages enables GET/POST/DELETE /api/devices/{id}/image. The
// directory is where the uploaded images are kept.
func WithDeviceImages(directory string) Option {
	return func(s *Server) { s.deviceImages = directory }
}

// WithStreams enables the HLS routes. The directory is the root the stream
// producer writes its playlists and segments into: a file named
// <directory>/<deviceId>/index.m3u8 is served as
// /api/stream/<deviceId>/index.m3u8.
func WithStreams(directory string) Option {
	return func(s *Server) { s.streamDir = directory }
}

// WithTOTP enables the two factor routes. Without it they report 501.
func WithTOTP(store DeviceTOTPStore) Option {
	return func(s *Server) { s.totp = store }
}

// registerExtensionRoutes mounts the routes the Java server has and the
// milestones before this one left out.
func (s *Server) registerExtensionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/attributes/computed/test", s.handleComputedTest)
	mux.HandleFunc("POST /api/attributes/computed/test", s.handleComputedTest)

	mux.HandleFunc("GET /api/devices/{id}/accumulators", s.handleAccumulators)
	mux.HandleFunc("PUT /api/devices/{id}/accumulators", s.handleAccumulatorsUpdate)

	mux.HandleFunc("GET /api/devices/{id}/image", s.handleDeviceImageGet)
	mux.HandleFunc("POST /api/devices/{id}/image", s.handleDeviceImageUpload)
	mux.HandleFunc("DELETE /api/devices/{id}/image", s.handleDeviceImageDelete)

	mux.HandleFunc("POST /api/permissions/bulk", s.handlePermissions)
	mux.HandleFunc("DELETE /api/permissions/bulk", s.handlePermissions)

	mux.HandleFunc("POST /api/password/update", s.handlePasswordUpdate)

	mux.HandleFunc("GET /api/server/cache", s.handleServerCache)

	mux.HandleFunc("GET /api/stream/{deviceId}", s.handleStreamInfo)
	mux.HandleFunc("GET /api/stream/{deviceId}/{file...}", s.handleStreamFile)

	mux.HandleFunc("GET /api/users/{id}/totp", s.handleTOTPStatus)
	mux.HandleFunc("POST /api/users/{id}/totp", s.handleTOTPGenerate)
	mux.HandleFunc("PUT /api/users/{id}/totp", s.handleTOTPEnable)
	mux.HandleFunc("DELETE /api/users/{id}/totp", s.handleTOTPDisable)
	mux.HandleFunc("POST /api/users/totp", s.handleTOTPVerifySelf)

	mux.HandleFunc("GET /api/session/openid/auth", s.handleOpenIDAuth)
	mux.HandleFunc("GET /api/session/openid/callback", s.handleOpenIDCallback)
}

// notConfigured writes the 501 a route answers when its dependency is absent.
func notConfigured(w http.ResponseWriter, what string) {
	writeError(w, http.StatusNotImplemented, errors.New(what+" is not configured"))
}

// deviceFromPath reads the {id} of a device route and checks access.
func (s *Server) deviceFromPath(w http.ResponseWriter, r *http.Request) (*model.Device, bool) {
	deviceID, err := parsePositiveID(r.PathValue("id"), "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return nil, false
	}
	if !s.canAccessDevice(r, deviceID) {
		writeForbiddenDevice(w, deviceID)
		return nil, false
	}
	if s.devices == nil {
		notConfigured(w, "devices")
		return nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()
	device, err := s.devices.Device(ctx, deviceID)
	if err != nil {
		if errors.Is(err, model.ErrDeviceNotFound) {
			writeError(w, http.StatusNotFound, err)
			return nil, false
		}
		s.logf("api: load device %d: %v", deviceID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return nil, false
	}
	return device, true
}
