package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/model"
)

// fakeMedia is both the metadata store and the file side the routes need.
type fakeMedia struct {
	mu      sync.Mutex
	dir     string
	files   map[int64]*model.MediaFile
	next    int64
	deleted []int64
}

func newFakeMedia(t *testing.T) *fakeMedia {
	t.Helper()
	return &fakeMedia{dir: t.TempDir(), files: make(map[int64]*model.MediaFile)}
}

func (f *fakeMedia) MediaFile(_ context.Context, id int64) (*model.MediaFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	file, ok := f.files[id]
	if !ok {
		return nil, model.ErrMediaFileNotFound
	}
	copied := *file
	return &copied, nil
}

func (f *fakeMedia) MediaFiles(
	_ context.Context, deviceID int64, _, _ time.Time,
) ([]model.MediaFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var files []model.MediaFile
	for _, file := range f.files {
		if file.DeviceID == deviceID {
			files = append(files, *file)
		}
	}
	return files, nil
}

func (f *fakeMedia) Path(file *model.MediaFile) (string, error) {
	return filepath.Join(f.dir, file.FileName), nil
}

func (f *fakeMedia) Delete(_ context.Context, file *model.MediaFile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, file.ID)
	f.deleted = append(f.deleted, file.ID)
	return nil
}

func (f *fakeMedia) Save(
	_ context.Context, deviceID int64, mediaType, name string, body io.Reader,
	_ model.Attributes,
) (*model.MediaFile, error) {
	content, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	fileName := "stored-" + name
	if err := os.WriteFile(filepath.Join(f.dir, fileName), content, 0o600); err != nil {
		return nil, err
	}
	file := &model.MediaFile{
		ID:        f.next,
		DeviceID:  deviceID,
		FileName:  fileName,
		Type:      mediaType,
		Size:      int64(len(content)),
		MediaTime: time.Now().UTC(),
	}
	f.files[file.ID] = file
	copied := *file
	return &copied, nil
}

// add stores a file without going through the route.
func (f *fakeMedia) add(t *testing.T, deviceID int64, content string) *model.MediaFile {
	t.Helper()
	file, err := f.Save(context.Background(), deviceID, model.MediaTypeImage, "photo.jpg",
		strings.NewReader(content), nil)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	return file
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

func TestMediaListNeedsADevice(t *testing.T) {
	media := newFakeMedia(t)
	handler := NewServer(nil, WithMedia(media, media),
		withTestToken(auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeReadPositions}, nil)),
	).Handler()

	if status := withToken(t, handler, http.MethodGet, "/api/media", "").Code; status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

func TestMediaListReturnsTheFiles(t *testing.T) {
	media := newFakeMedia(t)
	file := media.add(t, 7, "bytes")
	handler := NewServer(nil, WithMedia(media, media),
		withTestToken(auth.AdminPrincipal()),
	).Handler()

	recorder := withToken(t, handler, http.MethodGet, "/api/media?deviceId=7", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}
	var response []mediaFileResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(response) != 1 || response[0].ID != file.ID || response[0].Type != model.MediaTypeImage {
		t.Fatalf("response = %+v", response)
	}
}

func TestMediaDownloadStreamsTheFile(t *testing.T) {
	media := newFakeMedia(t)
	file := media.add(t, 7, "jpeg bytes")
	handler := NewServer(nil, WithMedia(media, media),
		withTestToken(auth.AdminPrincipal()),
	).Handler()

	recorder := withToken(t, handler, http.MethodGet,
		"/api/media/"+itoa(file.ID), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}
	if body := recorder.Body.String(); body != "jpeg bytes" {
		t.Fatalf("body = %q", body)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "image/jpeg" {
		t.Fatalf("content type = %q", contentType)
	}
}

func TestMediaDownloadRefusesAnotherOwnersFile(t *testing.T) {
	media := newFakeMedia(t)
	file := media.add(t, 9, "bytes")
	// The token only owns device 7.
	handler := NewServer(nil, WithMedia(media, media),
		withTestToken(auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeReadPositions}, []int64{7})),
	).Handler()

	if status := withToken(t, handler, http.MethodGet, "/api/media/"+itoa(file.ID), "").Code; status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
}

func TestMediaDownloadReportsAMissingFile(t *testing.T) {
	media := newFakeMedia(t)
	handler := NewServer(nil, WithMedia(media, media), withTestToken(auth.AdminPrincipal())).Handler()

	if status := withToken(t, handler, http.MethodGet, "/api/media/99", "").Code; status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestMediaUploadStoresTheFile(t *testing.T) {
	media := newFakeMedia(t)
	handler := NewServer(nil, WithMedia(media, media),
		withTestToken(auth.AdminPrincipal()),
	).Handler()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "photo.jpg")
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write([]byte("jpeg bytes")); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/media?deviceId=7", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+testToken)
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}
	var response mediaFileResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.DeviceID != 7 || response.Size != int64(len("jpeg bytes")) {
		t.Fatalf("response = %+v", response)
	}
}

func TestMediaUploadNeedsTheWriteScope(t *testing.T) {
	media := newFakeMedia(t)
	handler := NewServer(nil, WithMedia(media, media),
		withTestToken(auth.NewPrincipal(1, false, []auth.Scope{auth.ScopeReadPositions}, nil)),
	).Handler()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/media?deviceId=7", strings.NewReader(""))
	request.Header.Set("Authorization", "Bearer "+testToken)
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

func TestMediaDeleteRemovesTheRow(t *testing.T) {
	media := newFakeMedia(t)
	file := media.add(t, 7, "bytes")
	handler := NewServer(nil, WithMedia(media, media),
		withTestToken(auth.AdminPrincipal()),
	).Handler()

	recorder := withToken(t, handler, http.MethodDelete, "/api/media/"+itoa(file.ID), "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body)
	}
	if len(media.deleted) != 1 || media.deleted[0] != file.ID {
		t.Fatalf("deleted = %v", media.deleted)
	}
}

func TestMediaRoutesWithoutAService(t *testing.T) {
	handler := NewServer(nil, withTestToken(auth.AdminPrincipal())).Handler()
	for _, target := range []string{"/api/media?deviceId=7", "/api/media/1"} {
		if status := withToken(t, handler, http.MethodGet, target, "").Code; status != http.StatusNotImplemented {
			t.Fatalf("%s status = %d, want 501", target, status)
		}
	}
}
