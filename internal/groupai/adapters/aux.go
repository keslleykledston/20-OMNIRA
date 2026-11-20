package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"time"

	"crypto/hkdf"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/channels/adapters/crypto"
	"github.com/omnira/omnira/internal/groupai/application"
	mediaports "github.com/omnira/omnira/internal/media/ports"
	servicectxapp "github.com/omnira/omnira/internal/servicectx/application"
	servicectxdomain "github.com/omnira/omnira/internal/servicectx/domain"
)

// ImportCipher seals the raw of an import with AES-256-GCM (ADR-0009), under a key DERIVED from the credentials key with HKDF and a purpose label:
// the import files never share a key with the stored credentials, and rotating one does not require touching the other.
type ImportCipher struct{ c *crypto.AESGCM }

var _ application.Cipher = (*ImportCipher)(nil)

const importKeyInfo = "omnira/groupai/import-raw/v1"

func NewImportCipher(master []byte) (*ImportCipher, error) {
	if len(master) != 32 {
		return nil, fmt.Errorf("groupai: the master key must be 32 bytes, got %d", len(master))
	}
	key, err := hkdf.Key(sha256.New, master, nil, importKeyInfo, 32)
	if err != nil {
		return nil, err
	}
	c, err := crypto.NewAESGCM(key)
	if err != nil {
		return nil, err
	}
	return &ImportCipher{c: c}, nil
}

// NewImportCipherFromEnv reads the same OMNIRA_CREDENTIALS_KEY the credential cipher uses (base64, 32 bytes). Missing or malformed is an error:
// there is no silent fallback.
func NewImportCipherFromEnv() (*ImportCipher, error) {
	v := os.Getenv("OMNIRA_CREDENTIALS_KEY")
	if v == "" {
		return nil, errors.New("OMNIRA_CREDENTIALS_KEY not set")
	}
	key, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, fmt.Errorf("invalid OMNIRA_CREDENTIALS_KEY base64: %w", err)
	}
	return NewImportCipher(key)
}

func (i *ImportCipher) Seal(plain []byte) ([]byte, error)  { return i.c.Encrypt(plain) }
func (i *ImportCipher) Open(sealed []byte) ([]byte, error) { return i.c.Decrypt(sealed) }

// AVScanner adapts the media module's antivirus port. Any error is "not scanned" and the caller refuses the file.
type AVScanner struct{ s mediaports.Scanner }

var _ application.Scanner = (*AVScanner)(nil)

func NewAVScanner(s mediaports.Scanner) *AVScanner { return &AVScanner{s: s} }

func (a *AVScanner) Scan(ctx context.Context, data []byte) (bool, string, error) {
	v, err := a.s.Scan(ctx, data)
	if err != nil {
		return false, "", err
	}
	return v.Infected, v.Signature, nil
}

// ServiceBridge turns an approved proposal into a case or a task through the service module. The module reads the session of the person who approved
// (their keys: service.create), so the AI and the proposal never hold a permission of their own.
type ServiceBridge struct{ svc *servicectxapp.Service }

var _ application.ServiceBridge = (*ServiceBridge)(nil)

func NewServiceBridge(svc *servicectxapp.Service) *ServiceBridge { return &ServiceBridge{svc: svc} }

func mapServiceErr(err error) error {
	if errors.Is(err, servicectxdomain.ErrForbidden) {
		return application.ErrForbiddenCreation
	}
	return err
}

func (b *ServiceBridge) OpenCase(ctx context.Context, title, description, priority, key string) (uuid.UUID, error) {
	res, err := b.svc.OpenCase(ctx, servicectxdomain.CaseOpenInput{Title: title, Description: description, Priority: servicectxdomain.Priority(priority), IdempotencyKey: key})
	if err != nil {
		return uuid.Nil, mapServiceErr(err)
	}
	return res.Case.ID, nil
}

func (b *ServiceBridge) CreateTask(ctx context.Context, title, description, priority string, due *time.Time, key string) (uuid.UUID, error) {
	res, err := b.svc.CreateTask(ctx, servicectxdomain.TaskOpenInput{Title: title, Description: description, Priority: servicectxdomain.Priority(priority), DueAt: due,
		OriginType: servicectxdomain.OriginAI, IdempotencyKey: key})
	if err != nil {
		return uuid.Nil, mapServiceErr(err)
	}
	return res.Task.ID, nil
}
