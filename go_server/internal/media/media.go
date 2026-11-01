// Package media stores the files a device uploads. The bytes go to the media
// directory and the metadata row carries the index the REST resource and the
// media event rule read.
package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// MaxFileSize caps one upload. The Java server has no cap of its own, but a
// device that uploads a broken length would otherwise fill the disk.
const MaxFileSize = 32 << 20

// Store is the metadata storage.
type Store interface {
	CreateMediaFile(ctx context.Context, file *model.MediaFile) error
	MediaFile(ctx context.Context, id int64) (*model.MediaFile, error)
	MediaFiles(ctx context.Context, deviceID int64, from, to time.Time) ([]model.MediaFile, error)
	DeleteMediaFile(ctx context.Context, id int64) error
}

// Manager writes the files and keeps the rows in step.
type Manager struct {
	Store Store
	// Directory is where the bytes are written. Empty disables the service.
	Directory string
	Now       func() time.Time
}

// Enabled reports whether the manager can store a file.
func (m *Manager) Enabled() bool { return m != nil && m.Store != nil && m.Directory != "" }

// Save stores one uploaded file and returns its row. The stored name is
// generated, so a name from the device can never point outside the directory.
func (m *Manager) Save(
	ctx context.Context, deviceID int64, mediaType, originalName string,
	reader io.Reader, attributes model.Attributes,
) (*model.MediaFile, error) {
	if !m.Enabled() {
		return nil, fmt.Errorf("media: storage is not configured")
	}
	mediaType = normaliseType(mediaType)
	if mediaType == "" {
		return nil, fmt.Errorf("media: unknown type")
	}
	if deviceID == 0 {
		return nil, fmt.Errorf("media: device is required")
	}

	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	fileName := generateName(now, mediaType, filepath.Ext(originalName))
	path := filepath.Join(m.Directory, fileName)

	if err := os.MkdirAll(m.Directory, 0o755); err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(reader, MaxFileSize+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(path)
		if copyErr != nil {
			return nil, fmt.Errorf("media: write: %w", copyErr)
		}
		return nil, fmt.Errorf("media: write: %w", closeErr)
	}
	if written > MaxFileSize {
		_ = os.Remove(path)
		return nil, fmt.Errorf("media: file is larger than %d bytes", MaxFileSize)
	}

	if attributes == nil {
		attributes = model.Attributes{}
	}
	if originalName != "" {
		attributes[model.AttrOriginalName] = filepath.Base(originalName)
	}
	row := &model.MediaFile{
		DeviceID:   deviceID,
		FileName:   fileName,
		Type:       mediaType,
		Size:       written,
		MediaTime:  now.UTC(),
		Attributes: attributes,
	}
	if err := m.Store.CreateMediaFile(ctx, row); err != nil {
		// The row is the index: a file without one is unreachable, so it goes.
		_ = os.Remove(path)
		return nil, err
	}
	return row, nil
}

// Path returns the location of a stored file, refusing anything that would
// escape the directory even if a row was written by hand.
func (m *Manager) Path(file *model.MediaFile) (string, error) {
	if m == nil || m.Directory == "" || file == nil {
		return "", fmt.Errorf("media: storage is not configured")
	}
	name := filepath.Base(file.FileName)
	if name != file.FileName || name == "." || name == ".." {
		return "", fmt.Errorf("media: invalid file name %q", file.FileName)
	}
	return filepath.Join(m.Directory, name), nil
}

// Delete removes the bytes and then the row.
func (m *Manager) Delete(ctx context.Context, file *model.MediaFile) error {
	if !m.Enabled() || file == nil {
		return fmt.Errorf("media: storage is not configured")
	}
	path, err := m.Path(file)
	if err == nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("media: %w", removeErr)
		}
	}
	return m.Store.DeleteMediaFile(ctx, file.ID)
}

// normaliseType maps the type the client sends onto the three supported values.
func normaliseType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case model.MediaTypeImage, "photo", "picture":
		return model.MediaTypeImage
	case model.MediaTypeVideo, "movie":
		return model.MediaTypeVideo
	case model.MediaTypeAudio, "sound":
		return model.MediaTypeAudio
	}
	return ""
}

// generateName builds a unique, sortable name: the timestamp keeps a directory
// listing readable and the random suffix keeps two uploads apart.
func generateName(now time.Time, mediaType, extension string) string {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		// crypto/rand only fails when the platform has no entropy at all, which
		// would break far more than a file name; fall back to the clock.
		suffix = []byte(fmt.Sprintf("%012d", now.UnixNano()))
	}
	clean := strings.ToLower(strings.TrimSpace(extension))
	if len(clean) > 8 || strings.ContainsAny(clean, "/\\") {
		clean = ""
	}
	return now.UTC().Format("20060102T150405") + "-" +
		hex.EncodeToString(suffix) + "-" + mediaType + clean
}
