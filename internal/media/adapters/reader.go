package adapters

import (
	"context"
	"errors"
	"io/fs"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/media/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Reader serves cleared media to authenticated operators. It runs inside the request's tenant session, so
// RLS decides which rows exist; it never opens anything but the clean area.
type Reader struct {
	pool  *pgxpool.Pool
	store *FileStore
}

var _ ports.MediaReader = (*Reader)(nil)

func NewReader(pool *pgxpool.Pool, store *FileStore) *Reader {
	return &Reader{pool: pool, store: store}
}

func (r *Reader) Open(ctx context.Context, tenantID, messageID uuid.UUID) (*ports.ServedMedia, error) {
	var id uuid.UUID
	var status, mime, sha string
	var size int64
	var purged bool
	err := platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, `
		SELECT id, status, mime, size_bytes, sha256, file_purged_at IS NOT NULL
		FROM message_media WHERE tenant_id=$1 AND message_id=$2`, tenantID, messageID).Scan(&id, &status, &mime, &size, &sha, &purged)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrMediaNotFound
	}
	if err != nil {
		return nil, err
	}
	out := &ports.ServedMedia{Status: status, Mime: mime, Size: size, SHA256: sha}
	if purged {
		out.Status = "purged"
		return out, nil
	}
	if status != string(ports.StatusClean) {
		return out, nil
	}
	f, err := r.store.OpenClean(tenantID.String(), id.String())
	if errors.Is(err, fs.ErrNotExist) {
		out.Status = string(ports.StatusSourceGone)
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.File = f
	return out, nil
}
