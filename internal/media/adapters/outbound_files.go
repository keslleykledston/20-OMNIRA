package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	messagesports "github.com/omnira/omnira/internal/messages/ports"
)

// OutboundFiles keeps the files operators upload to send to customers (ADR-0024), apart from inbound media:
//
//	<root>/outbound/<tenant>/<attachment id>      written by the API after the antivirus cleared it; read by the worker that delivers it
//
// Paths are built from UUIDs only: the operator's file name never reaches the file system.
type OutboundFiles struct{ dir string }

// NewOutboundFiles prepares <mediaRoot>/outbound. The API needs write access to that directory only (a nested read-write mount over the
// read-only inbound media mount), so a compromised API cannot plant files in the area inbound media is served from.
func NewOutboundFiles(mediaRoot string) (*OutboundFiles, error) {
	if mediaRoot == "" || !filepath.IsAbs(mediaRoot) {
		return nil, errors.New("outbound media: an absolute media directory is required")
	}
	dir := filepath.Join(mediaRoot, "outbound")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("outbound media: %w", err)
	}
	return &OutboundFiles{dir: dir}, nil
}

func (o *OutboundFiles) path(tenantID, id uuid.UUID) string {
	return filepath.Join(o.dir, tenantID.String(), id.String())
}

// Put writes the file atomically and refuses to overwrite.
func (o *OutboundFiles) Put(tenantID, id uuid.UUID, data []byte) error {
	p := o.path(tenantID, id)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	tmp := p + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if _, err := os.Lstat(p); err == nil {
		_ = os.Remove(tmp)
		return errors.New("outbound media: file already exists")
	}
	return os.Rename(tmp, p)
}

func (o *OutboundFiles) Remove(tenantID, id uuid.UUID) error {
	if err := os.Remove(o.path(tenantID, id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ReadVerified returns the file only if it has exactly the recorded size and SHA-256: what is delivered is what was cleared.
func (o *OutboundFiles) ReadVerified(tenantID, id uuid.UUID, size int64, sha256Hex string) ([]byte, error) {
	f, err := os.Open(o.path(tenantID, id))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, size+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != size || hex.EncodeToString(sum[:]) != sha256Hex {
		return nil, errors.New("outbound media: the stored file does not match its record")
	}
	return data, nil
}

// Open returns the file for serving it back to the operator that sent it.
func (o *OutboundFiles) Open(tenantID, id uuid.UUID) (*os.File, error) {
	return os.Open(o.path(tenantID, id))
}

// virusScanner adapts the inbound pipeline's clamd client to the messages module's scanner port.
type virusScanner struct{ clam *ClamAV }

// NewVirusScanner connects the upload path to the same clamd the inbound media pipeline uses.
func NewVirusScanner(addr string) messagesports.VirusScanner {
	return virusScanner{clam: NewClamAV(addr)}
}

func (v virusScanner) Scan(ctx context.Context, data []byte) (messagesports.VirusVerdict, error) {
	verdict, err := v.clam.Scan(ctx, data)
	if err != nil {
		return messagesports.VirusVerdict{}, err
	}
	return messagesports.VirusVerdict{Infected: verdict.Infected, Signature: verdict.Signature}, nil
}

var _ messagesports.AttachmentFiles = (*OutboundFiles)(nil)
