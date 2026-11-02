package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"traccar-server_go/go_server/internal/auth"
)

// maxDeviceImage caps an uploaded image. A phone photo is a few megabytes, so
// five leaves room without letting a caller fill the disk with one request.
const maxDeviceImage = 5 << 20

// deviceImagePath is the file of one device. The id is numeric, so joining it
// cannot escape the directory.
func (s *Server) deviceImagePath(deviceID int64) string {
	return filepath.Join(s.deviceImages, strconv.FormatInt(deviceID, 10))
}

// handleDeviceImageGet serves the uploaded image of a device.
func (s *Server) handleDeviceImageGet(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeReadDevices) {
		return
	}
	if s.deviceImages == "" {
		notConfigured(w, "device images")
		return
	}
	device, ok := s.deviceFromPath(w, r)
	if !ok {
		return
	}

	path := s.deviceImagePath(device.ID)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, errors.New("device has no image"))
			return
		}
		s.logf("api: open device image %s: %v", path, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	defer file.Close()

	header := make([]byte, 512)
	read, _ := io.ReadFull(file, header)
	contentType := http.DetectContentType(header[:read])
	if !strings.HasPrefix(contentType, "image/") {
		contentType = "application/octet-stream"
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, "", device.CreatedAt, file)
}

// handleDeviceImageUpload stores the image of a device. A multipart body uses
// the "file" part, which is what the web interface sends; any other body is
// treated as the image itself so a script can PUT a file directly.
func (s *Server) handleDeviceImageUpload(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeWriteDevices) {
		return
	}
	if s.deviceImages == "" {
		notConfigured(w, "device images")
		return
	}
	device, ok := s.deviceFromPath(w, r)
	if !ok {
		return
	}

	body, err := deviceImageBody(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("image body is empty"))
		return
	}
	if contentType := http.DetectContentType(body); !strings.HasPrefix(contentType, "image/") {
		writeError(w, http.StatusBadRequest, errors.New("body is not an image"))
		return
	}

	if err := os.MkdirAll(s.deviceImages, 0o755); err != nil {
		s.logf("api: create device image directory: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	path := s.deviceImagePath(device.ID)
	temporary, err := os.CreateTemp(s.deviceImages, ".upload-*")
	if err != nil {
		s.logf("api: create device image temp file: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(body); err != nil {
		temporary.Close()
		s.logf("api: write device image: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if err := temporary.Close(); err != nil {
		s.logf("api: close device image: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		s.logf("api: replace device image %s: %v", path, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeviceImageDelete removes the image of a device.
func (s *Server) handleDeviceImageDelete(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, auth.ScopeWriteDevices) {
		return
	}
	if s.deviceImages == "" {
		notConfigured(w, "device images")
		return
	}
	device, ok := s.deviceFromPath(w, r)
	if !ok {
		return
	}
	if err := os.Remove(s.deviceImagePath(device.ID)); err != nil && !os.IsNotExist(err) {
		s.logf("api: delete device image of %d: %v", device.ID, err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deviceImageBody reads the upload, preferring the multipart "file" part.
func deviceImageBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	limited := http.MaxBytesReader(w, r.Body, maxDeviceImage)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(maxDeviceImage); err != nil {
			return nil, errors.New("invalid multipart body")
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			return nil, errors.New("multipart body needs a file part")
		}
		defer file.Close()
		return io.ReadAll(io.LimitReader(file, maxDeviceImage))
	}
	return io.ReadAll(limited)
}
