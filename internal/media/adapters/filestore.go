package adapters

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/media/ports"
)

// FileStore keeps media bytes on disk. The layout separates two areas on purpose:
//
//	<root>/quarantine/<tenant>/<id>   written by the worker, never read by the API
//	<root>/clean/<tenant>/<id>        only files the antivirus cleared; the API serves from here
//
// Paths are built from UUIDs only; the sender's file name never reaches the file system.
type FileStore struct{ root string }

var _ ports.Store = (*FileStore)(nil)

func NewFileStore(root string) (*FileStore, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, errors.New("media store: an absolute directory is required")
	}
	for _, d := range []string{"quarantine", "clean"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			return nil, fmt.Errorf("media store: %w", err)
		}
	}
	return &FileStore{root: root}, nil
}

// OpenFileStore is the API-side constructor: it creates nothing (the mount may be read-only) and only checks that
// the directory exists. Reading cleared files is all the API ever does.
func OpenFileStore(root string) (*FileStore, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, errors.New("media store: an absolute directory is required")
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("media store: %s is not a directory", root)
	}
	return &FileStore{root: root}, nil
}

func (s *FileStore) path(area string, w ports.Work) string {
	return filepath.Join(s.root, area, w.TenantID.String(), w.ID.String())
}

func (s *FileStore) PutQuarantine(w ports.Work, data []byte) error {
	p := s.path("quarantine", w)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *FileStore) GetQuarantine(w ports.Work) ([]byte, error) {
	return os.ReadFile(s.path("quarantine", w))
}

func (s *FileStore) Promote(w ports.Work) error {
	dst := s.path("clean", w)
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	return os.Rename(s.path("quarantine", w), dst)
}

func (s *FileStore) Remove(w ports.Work) error {
	for _, area := range []string{"quarantine", "clean"} {
		if err := os.Remove(s.path(area, w)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// OpenClean returns the cleared file for serving. It never looks in quarantine.
func (s *FileStore) OpenClean(tenantID, id string) (*os.File, error) {
	return os.Open(filepath.Join(s.root, "clean", tenantID, id))
}

// OpenOutbound opens a file an operator sent (ADR-0024); the API only ever reads this area to show the operators their own sent files.
func (s *FileStore) OpenOutbound(tenantID, id uuid.UUID) (*os.File, error) {
	// O_NOFOLLOW: the area is written by the API, so a planted symlink must not turn this into a read of another file.
	return os.OpenFile(filepath.Join(s.root, "outbound", tenantID.String(), id.String()), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

// ReadClean returns the bytes of a cleared file, refusing anything larger than maxBytes. It never reads quarantine.
func (s *FileStore) ReadClean(tenantID, mediaID uuid.UUID, maxBytes int64) ([]byte, error) {
	f, err := s.OpenClean(tenantID.String(), mediaID.String())
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("media store: file larger than allowed")
	}
	return data, nil
}

var _ ports.CleanFiles = (*FileStore)(nil)
