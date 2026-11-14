package adapters

import (
	"context"
	"errors"
	"io/fs"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	mediaadapters "github.com/omnira/omnira/internal/media/adapters"
	mediaports "github.com/omnira/omnira/internal/media/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// GroupMediaReader serves a group's cleared image/PDF to a member with group.read. Runs inside the
// REQUEST'S tenant session (RLS decides which row exists), same shape as internal/media/adapters.Reader for
// 1:1 — only ever opens the SAME shared clean area, never quarantine, never another tenant's file.
type GroupMediaReader struct {
	pool  *pgxpool.Pool
	store *mediaadapters.FileStore
}

func NewGroupMediaReader(pool *pgxpool.Pool, store *mediaadapters.FileStore) *GroupMediaReader {
	return &GroupMediaReader{pool: pool, store: store}
}

func (r *GroupMediaReader) Open(ctx context.Context, tenantID, groupMessageID uuid.UUID) (*mediaports.ServedMedia, error) {
	var id uuid.UUID
	var status, mime, sha string
	var size int64
	var purged bool
	err := platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, `
		SELECT id, status, mime, size_bytes, sha256, file_purged_at IS NOT NULL
		FROM wa_group_message_media WHERE tenant_id=$1 AND group_message_id=$2`, tenantID, groupMessageID).
		Scan(&id, &status, &mime, &size, &sha, &purged)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, mediaports.ErrMediaNotFound
	}
	if err != nil {
		return nil, err
	}
	out := &mediaports.ServedMedia{ID: id, Status: status, Mime: mime, Size: size, SHA256: sha}
	if purged {
		out.Status = "purged"
		return out, nil
	}
	if status != string(mediaports.StatusClean) {
		return out, nil
	}
	f, err := r.store.OpenClean(tenantID.String(), id.String())
	if errors.Is(err, fs.ErrNotExist) {
		out.Status = string(mediaports.StatusSourceGone)
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.File = f
	return out, nil
}
