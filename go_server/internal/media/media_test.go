package media_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/media"
	"traccar-server_go/go_server/internal/model"
)

// fakeStore keeps the metadata rows in memory.
type fakeStore struct {
	mu    sync.Mutex
	files map[int64]*model.MediaFile
	next  int64
	err   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{files: make(map[int64]*model.MediaFile)}
}

func (f *fakeStore) CreateMediaFile(_ context.Context, file *model.MediaFile) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	file.ID = f.next
	copied := *file
	f.files[file.ID] = &copied
	return nil
}

func (f *fakeStore) MediaFile(_ context.Context, id int64) (*model.MediaFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	file, ok := f.files[id]
	if !ok {
		return nil, model.ErrMediaFileNotFound
	}
	copied := *file
	return &copied, nil
}

func (f *fakeStore) MediaFiles(
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

func (f *fakeStore) DeleteMediaFile(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.files[id]; !ok {
		return model.ErrMediaFileNotFound
	}
	delete(f.files, id)
	return nil
}

func newManager(t *testing.T) (*media.Manager, *fakeStore, string) {
	t.Helper()
	directory := t.TempDir()
	store := newFakeStore()
	return &media.Manager{Store: store, Directory: directory}, store, directory
}

func TestSaveWritesTheFileAndTheRow(t *testing.T) {
	manager, store, directory := newManager(t)
	row, err := manager.Save(context.Background(), 7, "image", "../../etc/passwd.jpg",
		strings.NewReader("jpeg bytes"), nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if row.ID == 0 || row.DeviceID != 7 || row.Size != int64(len("jpeg bytes")) {
		t.Fatalf("row = %+v", row)
	}
	if row.Type != model.MediaTypeImage {
		t.Errorf("type = %q", row.Type)
	}
	// The stored name is generated, so a path from the client cannot escape.
	if strings.Contains(row.FileName, "/") || strings.Contains(row.FileName, "..") {
		t.Fatalf("file name = %q", row.FileName)
	}
	if original, _ := row.Attributes[model.AttrOriginalName].(string); original != "passwd.jpg" {
		t.Errorf("original name = %v", row.Attributes[model.AttrOriginalName])
	}

	path, err := manager.Path(row)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if filepath.Dir(path) != directory {
		t.Fatalf("path = %q", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "jpeg bytes" {
		t.Fatalf("content = %q", content)
	}
	if _, ok := store.files[row.ID]; !ok {
		t.Fatal("the row was not stored")
	}
}

func TestSaveRejectsAnUnknownType(t *testing.T) {
	manager, _, _ := newManager(t)
	if _, err := manager.Save(context.Background(), 7, "hologram", "a", strings.NewReader("x"), nil); err == nil {
		t.Fatal("an unknown type was accepted")
	}
}

func TestSaveNormalisesTheType(t *testing.T) {
	manager, _, _ := newManager(t)
	row, err := manager.Save(context.Background(), 7, "video", "clip.mp4", strings.NewReader("x"), nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if row.Type != model.MediaTypeVideo {
		t.Fatalf("type = %q", row.Type)
	}
	if !strings.HasSuffix(row.FileName, ".mp4") {
		t.Fatalf("file name = %q", row.FileName)
	}
}

func TestSaveRemovesTheFileWhenTheRowFails(t *testing.T) {
	manager, store, directory := newManager(t)
	store.err = errors.New("db down")

	if _, err := manager.Save(context.Background(), 7, "image", "a.jpg", strings.NewReader("x"), nil); err == nil {
		t.Fatal("the store failure was swallowed")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("leftover files = %d", len(entries))
	}
}

func TestSaveRefusesABrokenStorage(t *testing.T) {
	manager := &media.Manager{}
	if _, err := manager.Save(context.Background(), 7, "image", "a", strings.NewReader("x"), nil); err == nil {
		t.Fatal("a manager without a directory accepted a file")
	}
}

func TestPathRefusesAnEscapingName(t *testing.T) {
	manager, _, _ := newManager(t)
	if _, err := manager.Path(&model.MediaFile{FileName: "../secret"}); err == nil {
		t.Fatal("an escaping name was accepted")
	}
}

func TestDeleteRemovesBothSides(t *testing.T) {
	manager, store, _ := newManager(t)
	row, err := manager.Save(context.Background(), 7, "image", "a.jpg", strings.NewReader("x"), nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	path, _ := manager.Path(row)

	if err := manager.Delete(context.Background(), row); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the file is still there: %v", err)
	}
	if _, ok := store.files[row.ID]; ok {
		t.Fatal("the row is still there")
	}
}
