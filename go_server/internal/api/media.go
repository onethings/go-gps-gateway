package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

// MediaStore is the metadata side of the media files.
type MediaStore interface {
	MediaFile(ctx context.Context, id int64) (*model.MediaFile, error)
	MediaFiles(ctx context.Context, deviceID int64, from, to time.Time) ([]model.MediaFile, error)
}

// MediaFiles serves the bytes and keeps them and the rows in step.
type MediaFiles interface {
	Path(file *model.MediaFile) (string, error)
	Delete(ctx context.Context, file *model.MediaFile) error
	// Save stores an upload for a device. The handler passes the body and the
	// name the client used.
	Save(ctx context.Context, deviceID int64, mediaType, name string,
		body io.Reader, attributes model.Attributes) (*model.MediaFile, error)
}

// WithMedia enables the media routes. manager may be nil, which leaves them
// reporting 501.
func WithMedia(store MediaStore, manager MediaFiles) Option {
	return func(s *Server) {
		s.media = store
		s.mediaFiles = manager
	}
}

func (s *Server) registerMediaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/media", s.handleMediaList)
	mux.HandleFunc("POST /api/media", s.handleMediaUpload)
	mux.HandleFunc("GET /api/media/{id}", s.handleMediaDownload)
	mux.HandleFunc("DELETE /api/media/{id}", s.handleMediaDelete)
}

// handleMediaList lists the files of one device inside a period.
func (s *Server) handleMediaList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeReadPositions) {
		return
	}
	if s.media == nil {
		writeError(w, http.StatusNotImplemented, errors.New("media is not enabled"))
		return
	}

	query := r.URL.Query()
	deviceID, err := requiredIDQuery(query, "deviceId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.canAccessDevice(r, deviceID) {
		writeError(w, http.StatusForbidden, errors.New("device is not shared with you"))
		return
	}
	from, to, err := periodQuery(query)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	files, err := s.media.MediaFiles(ctx, deviceID, from, to)
	if err != nil {
		s.logf("api: query media: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, mediaResponse(files))
}

// handleMediaUpload stores one file for a device. A device token uploads its own
// files; an operator needs the write scope of the device.
func (s *Server) handleMediaUpload(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeWriteDevices) {
		return
	}
	if s.mediaFiles == nil {
		writeError(w, http.StatusNotImplemented, errors.New("media storage is not enabled"))
		return
	}

	deviceID, err := requiredIDQuery(r.URL.Query(), "deviceId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.canAccessDevice(r, deviceID) {
		writeError(w, http.StatusForbidden, errors.New("device is not shared with you"))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("file is required"))
		return
	}
	defer file.Close()

	mediaType := r.URL.Query().Get("type")
	if mediaType == "" {
		mediaType = model.MediaTypeImage
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	stored, err := s.mediaFiles.Save(ctx, deviceID, mediaType, header.Filename, file, nil)
	if err != nil {
		s.logf("api: store media: %v", err)
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, mediaResponse([]model.MediaFile{*stored})[0])
}

// handleMediaDownload streams one stored file back.
func (s *Server) handleMediaDownload(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeReadPositions) {
		return
	}
	if s.media == nil || s.mediaFiles == nil {
		writeError(w, http.StatusNotImplemented, errors.New("media is not enabled"))
		return
	}

	file, ok := s.mediaByID(w, r)
	if !ok {
		return
	}
	path, err := s.mediaFiles.Path(file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusNotFound, errors.New("media file is missing from storage"))
		return
	}

	w.Header().Set("Content-Type", mediaContentType(file.Type))
	w.Header().Set("Content-Disposition", `attachment; filename="`+file.FileName+`"`)
	http.ServeFile(w, r, path)
}

// handleMediaDelete removes the bytes and the row.
func (s *Server) handleMediaDelete(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeWriteDevices) {
		return
	}
	if s.mediaFiles == nil {
		writeError(w, http.StatusNotImplemented, errors.New("media storage is not enabled"))
		return
	}

	file, ok := s.mediaByID(w, r)
	if !ok {
		return
	}
	if err := s.mediaFiles.Delete(r.Context(), file); err != nil {
		s.logf("api: delete media %d: %v", file.ID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mediaByID reads the row and checks that the caller may see its device.
func (s *Server) mediaByID(w http.ResponseWriter, r *http.Request) (*model.MediaFile, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid id"))
		return nil, false
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()

	file, err := s.media.MediaFile(ctx, id)
	if errors.Is(err, model.ErrMediaFileNotFound) {
		writeError(w, http.StatusNotFound, errors.New("media file not found"))
		return nil, false
	}
	if err != nil {
		s.logf("api: query media %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return nil, false
	}
	if !s.canAccessDevice(r, file.DeviceID) {
		writeError(w, http.StatusForbidden, errors.New("device is not shared with you"))
		return nil, false
	}
	return file, true
}

// mediaFileResponse is the JSON shape of one row.
type mediaFileResponse struct {
	ID         int64          `json:"id"`
	DeviceID   int64          `json:"deviceId"`
	FileName   string         `json:"fileName"`
	Type       string         `json:"type"`
	Size       int64          `json:"size"`
	MediaTime  time.Time      `json:"mediaTime"`
	Attributes map[string]any `json:"attributes"`
}

func mediaResponse(files []model.MediaFile) []mediaFileResponse {
	response := make([]mediaFileResponse, 0, len(files))
	for _, file := range files {
		response = append(response, mediaFileResponse{
			ID:         file.ID,
			DeviceID:   file.DeviceID,
			FileName:   file.FileName,
			Type:       file.Type,
			Size:       file.Size,
			MediaTime:  file.MediaTime,
			Attributes: map[string]any(file.Attributes),
		})
	}
	return response
}

// mediaContentType maps the media type onto the header a browser needs.
func mediaContentType(mediaType string) string {
	switch mediaType {
	case model.MediaTypeVideo:
		return "video/mp4"
	case model.MediaTypeAudio:
		return "audio/mpeg"
	default:
		return "image/jpeg"
	}
}

// requiredIDQuery reads a required integer query parameter.
func requiredIDQuery(values url.Values, name string) (int64, error) {
	raw := strings.TrimSpace(values.Get(name))
	if raw == "" {
		return 0, errors.New(name + " is required")
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errors.New(name + " must be a number")
	}
	return value, nil
}

// periodQuery reads from/to, defaulting to the last day like the report routes.
func periodQuery(values url.Values) (time.Time, time.Time, error) {
	to := time.Now()
	from := to.Add(-24 * time.Hour)

	if raw := strings.TrimSpace(firstValue(values, "to")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("to must be an RFC3339 timestamp")
		}
		to = parsed
	}
	if raw := strings.TrimSpace(firstValue(values, "from")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("from must be an RFC3339 timestamp")
		}
		from = parsed
	}
	return from, to, nil
}

func firstValue(values url.Values, name string) string {
	return values.Get(name)
}
