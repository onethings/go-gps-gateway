package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"traccar-server_go/go_server/internal/auth"
)

// streamContentTypes maps the HLS file extensions to the media types a player
// expects. Everything else is served as a binary stream.
var streamContentTypes = map[string]string{
	".m3u8": "application/vnd.apple.mpegurl",
	".ts":   "video/mp2t",
	".m4s":  "video/iso.segment",
	".mp4":  "video/mp4",
	".mpd":  "application/dash+xml",
	".key":  "application/octet-stream",
}

// handleStreamInfo answers GET /api/stream/{deviceId}. Traccar's front end asks
// for the stream before it opens a player; the answer tells it the playlist the
// external producer wrote.
func (s *Server) handleStreamInfo(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeReadPositions) {
		return
	}
	if s.streamDir == "" {
		notConfigured(w, "streaming")
		return
	}
	deviceID, ok := s.streamDevice(w, r)
	if !ok {
		return
	}
	if _, err := os.Stat(filepath.Join(s.streamDir, strconv.FormatInt(deviceID, 10), "index.m3u8")); err != nil {
		writeError(w, http.StatusNotFound, errors.New("device has no stream"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"url": "/api/stream/" + strconv.FormatInt(deviceID, 10) + "/index.m3u8",
	})
}

// handleStreamFile serves one playlist or segment of a device stream. The
// files are written by the media server (MediaMTX writes the same layout), so
// this route is a read only view with the device access check in front.
func (s *Server) handleStreamFile(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeReadPositions) {
		return
	}
	if s.streamDir == "" {
		notConfigured(w, "streaming")
		return
	}
	deviceID, ok := s.streamDevice(w, r)
	if !ok {
		return
	}

	name := r.PathValue("file")
	if name == "" || strings.ContainsRune(name, '\\') {
		writeError(w, http.StatusBadRequest, errors.New("invalid stream path"))
		return
	}
	base := filepath.Join(s.streamDir, strconv.FormatInt(deviceID, 10))
	path := filepath.Join(base, filepath.Clean("/"+name))
	// A cleaned path can still climb out with ".." in the middle; compare the
	// resolved path against the device directory before opening it.
	if path != base && !strings.HasPrefix(path, base+string(os.PathSeparator)) {
		writeError(w, http.StatusForbidden, errors.New("stream path is outside the device directory"))
		return
	}

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, errors.New("stream file not found"))
			return
		}
		s.logf("api: open stream file %s: %v", path, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, errors.New("stream file not found"))
		return
	}
	contentType := streamContentTypes[strings.ToLower(filepath.Ext(path))]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
}

// streamDevice reads and authorises the device id of a stream route.
func (s *Server) streamDevice(w http.ResponseWriter, r *http.Request) (int64, bool) {
	deviceID, err := parsePositiveID(r.PathValue("deviceId"), "deviceId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return 0, false
	}
	if !s.canAccessDevice(r, deviceID) {
		writeForbiddenDevice(w, deviceID)
		return 0, false
	}
	return deviceID, true
}
