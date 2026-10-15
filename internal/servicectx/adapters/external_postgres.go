package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

var _ ports.ExternalRepository = (*PostgresRepository)(nil)

func (r *PostgresRepository) IntegrationSystem(ctx context.Context, integrationID uuid.UUID) (string, bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return "", false, err
	}
	var provider string
	err = r.q(ctx).QueryRow(ctx, `SELECT provider FROM channel_connections WHERE tenant_id=$1 AND id=$2`, tenant, integrationID).Scan(&provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return strings.ToLower(provider), err == nil, err
}

const recordColumns = `id, tenant_id, integration_id, external_system, external_type, external_id, display_ref, external_status, status_class, sync_state,
	source_updated_at, last_synced_at, created_by_user_id, version, created_at, updated_at`

func scanRecord(row pgx.Row) (*domain.ExternalRecord, error) {
	x := &domain.ExternalRecord{}
	var class string
	if err := row.Scan(&x.ID, &x.TenantID, &x.IntegrationID, &x.ExternalSystem, &x.ExternalType, &x.ExternalID, &x.DisplayRef, &x.ExternalStatus, &class, &x.SyncState,
		&x.SourceUpdatedAt, &x.LastSyncedAt, &x.CreatedBy, &x.Version, &x.CreatedAt, &x.UpdatedAt); err != nil {
		return nil, err
	}
	x.StatusClass = domain.StatusClass(class)
	return x, nil
}

func (r *PostgresRepository) oneRecord(ctx context.Context, where string, lock bool, args ...any) (*domain.ExternalRecord, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	sql := `SELECT ` + recordColumns + ` FROM external_records WHERE tenant_id = $1 AND ` + where
	if lock {
		sql += ` FOR UPDATE`
	}
	return scanRecord(r.q(ctx).QueryRow(ctx, sql, append([]any{tenant}, args...)...))
}

func (r *PostgresRepository) GetRecord(ctx context.Context, id uuid.UUID) (*domain.ExternalRecord, error) {
	x, err := r.oneRecord(ctx, `id = $2`, false, id)
	return x, mapErr(err)
}

func (r *PostgresRepository) LockRecord(ctx context.Context, id uuid.UUID) (*domain.ExternalRecord, error) {
	x, err := r.oneRecord(ctx, `id = $2`, true, id)
	return x, mapErr(err)
}

func (r *PostgresRepository) FindRecord(ctx context.Context, integrationID uuid.UUID, externalType, externalID string) (*domain.ExternalRecord, error) {
	x, err := r.oneRecord(ctx, `integration_id = $2 AND external_type = $3 AND external_id = $4`, false, integrationID, externalType, externalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return x, err
}

func (r *PostgresRepository) InsertRecord(ctx context.Context, x *domain.ExternalRecord) (*domain.ExternalRecord, bool, error) {
	tag, err := r.q(ctx).Exec(ctx, `
		INSERT INTO external_records (id, tenant_id, integration_id, external_system, external_type, external_id, display_ref, external_status, status_class, sync_state,
			source_updated_at, last_synced_at, created_by_user_id, version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (tenant_id, integration_id, external_type, external_id) DO NOTHING`,
		x.ID, x.TenantID, x.IntegrationID, x.ExternalSystem, x.ExternalType, x.ExternalID, x.DisplayRef, x.ExternalStatus, string(x.StatusClass), x.SyncState,
		x.SourceUpdatedAt, x.LastSyncedAt, x.CreatedBy, x.Version, x.CreatedAt, x.UpdatedAt)
	if err != nil {
		return nil, false, mapErr(err)
	}
	if tag.RowsAffected() == 1 {
		return x, true, nil
	}
	found, err := r.FindRecord(ctx, x.IntegrationID, x.ExternalType, x.ExternalID)
	if err != nil {
		return nil, false, err
	}
	return found, false, nil
}

func (r *PostgresRepository) ApplySnapshot(ctx context.Context, id uuid.UUID, snap domain.Snapshot, now time.Time) (bool, bool, *domain.ExternalRecord, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return false, false, nil, err
	}
	before, err := r.LockRecord(ctx, id)
	if err != nil {
		return false, false, nil, err
	}
	// older than what was applied: ignored (the database refuses to go back in time too, as a backstop)
	if snap.UpdatedAt != nil && before.SourceUpdatedAt != nil && snap.UpdatedAt.Before(*before.SourceUpdatedAt) {
		return false, false, before, nil
	}
	src := snap.UpdatedAt
	if src == nil {
		src = before.SourceUpdatedAt // a snapshot without the provider's time never moves it
	}
	row := r.q(ctx).QueryRow(ctx, `
		UPDATE external_records SET display_ref=$3, external_status=$4, status_class=$5, sync_state='synced', source_updated_at=$6, last_synced_at=$7
		WHERE tenant_id=$1 AND id=$2 RETURNING `+recordColumns,
		tenant, id, nonEmpty(snap.DisplayRef, before.DisplayRef), snap.ExternalStatus, string(snap.StatusClass), src, now)
	after, err := scanRecord(row)
	if err != nil {
		return false, false, nil, mapErr(err)
	}
	return true, after.StatusClass != before.StatusClass || after.ExternalStatus != before.ExternalStatus, after, nil
}

func (r *PostgresRepository) MarkSync(ctx context.Context, id uuid.UUID, state string, now time.Time) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.q(ctx).Exec(ctx, `UPDATE external_records SET sync_state=$3, last_synced_at=$4 WHERE tenant_id=$1 AND id=$2`, tenant, id, state, now)
	return mapErr(err)
}

func (r *PostgresRepository) ListRecords(ctx context.Context, f ports.RecordFilter) ([]domain.ExternalRecord, string, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, "", err
	}
	where := []string{"x.tenant_id = $1"}
	args := []any{tenant}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.IntegrationID != nil {
		add("x.integration_id = ?", *f.IntegrationID)
	}
	if f.ExternalType != "" {
		add("x.external_type = ?", f.ExternalType)
	}
	if f.ExternalID != "" {
		add("x.external_id = ?", f.ExternalID)
	}
	if f.StatusClass != "" {
		add("x.status_class = ?", string(f.StatusClass))
	}
	for col, id := range map[string]*uuid.UUID{"session_id": f.SessionID, "case_id": f.CaseID, "task_id": f.TaskID} {
		if id != nil {
			add("EXISTS (SELECT 1 FROM service_external_links l WHERE l.tenant_id = x.tenant_id AND l.external_record_id = x.id AND l.unlinked_at IS NULL AND l."+col+" = ?)", *id)
		}
	}
	if f.Cursor != "" {
		t, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, t, id)
		where = append(where, fmt.Sprintf("(x.created_at, x.id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	cols := make([]string, 0, 16)
	for _, c := range strings.Split(recordColumns, ",") {
		cols = append(cols, "x."+strings.TrimSpace(c))
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT `+strings.Join(cols, ", ")+` FROM external_records x WHERE `+strings.Join(where, " AND ")+
		fmt.Sprintf(` ORDER BY x.created_at DESC, x.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []domain.ExternalRecord
	for rows.Next() {
		x, err := scanRecord(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *x)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}

// ---- links ----

const linkColumns = `id, tenant_id, external_record_id, session_id, case_id, task_id, linked_by_user_id, linked_at, unlinked_at, unlinked_by_user_id`

func scanLink(row pgx.Row) (*domain.ExternalLink, error) {
	l := &domain.ExternalLink{}
	if err := row.Scan(&l.ID, &l.TenantID, &l.ExternalRecordID, &l.SessionID, &l.CaseID, &l.TaskID, &l.LinkedBy, &l.LinkedAt, &l.UnlinkedAt, &l.UnlinkedBy); err != nil {
		return nil, err
	}
	return l, nil
}

func (r *PostgresRepository) InsertLink(ctx context.Context, l *domain.ExternalLink) (bool, *domain.ExternalLink, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return false, nil, err
	}
	// the unique index is partial (active links only), so look first: a link that already stands is not an error
	var target, col string
	switch {
	case l.SessionID != nil:
		col, target = "session_id", l.SessionID.String()
	case l.CaseID != nil:
		col, target = "case_id", l.CaseID.String()
	case l.TaskID != nil:
		col, target = "task_id", l.TaskID.String()
	default:
		return false, nil, domain.Invalid("a link needs a target")
	}
	ex, err := scanLink(r.q(ctx).QueryRow(ctx, `SELECT `+linkColumns+` FROM service_external_links WHERE tenant_id=$1 AND external_record_id=$2 AND `+col+`=$3::uuid AND unlinked_at IS NULL`,
		tenant, l.ExternalRecordID, target))
	if err == nil {
		return false, ex, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, nil, err
	}
	_, err = r.q(ctx).Exec(ctx, `INSERT INTO service_external_links (id, tenant_id, external_record_id, session_id, case_id, task_id, linked_by_user_id, linked_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		l.ID, tenant, l.ExternalRecordID, l.SessionID, l.CaseID, l.TaskID, l.LinkedBy, l.LinkedAt)
	return err == nil, nil, mapErr(err)
}

func (r *PostgresRepository) GetLink(ctx context.Context, id uuid.UUID) (*domain.ExternalLink, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	l, err := scanLink(r.q(ctx).QueryRow(ctx, `SELECT `+linkColumns+` FROM service_external_links WHERE tenant_id=$1 AND id=$2`, tenant, id))
	return l, mapErr(err)
}

func (r *PostgresRepository) Unlink(ctx context.Context, id uuid.UUID, by *uuid.UUID, at time.Time) (bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	tag, err := r.q(ctx).Exec(ctx, `UPDATE service_external_links SET unlinked_at=$3, unlinked_by_user_id=$4 WHERE tenant_id=$1 AND id=$2 AND unlinked_at IS NULL`, tenant, id, at, by)
	if err != nil {
		return false, mapErr(err)
	}
	return tag.RowsAffected() == 1, nil
}

// ---- requests ----

const requestColumns = `id, tenant_id, integration_id, session_id, case_id, operation, title, idempotency_key, request_hash, correlation_key, state, external_record_id,
	failure_class, resolution_source, requested_by_user_id, resolved_by_user_id, resolved_at, version, created_at, updated_at`

func scanRequest(row pgx.Row) (*domain.ExternalRequest, error) {
	x := &domain.ExternalRequest{}
	var state string
	if err := row.Scan(&x.ID, &x.TenantID, &x.IntegrationID, &x.SessionID, &x.CaseID, &x.Operation, &x.Title, &x.IdempotencyKey, &x.RequestHash, &x.CorrelationKey, &state,
		&x.ExternalRecordID, &x.FailureClass, &x.ResolutionSource, &x.RequestedBy, &x.ResolvedBy, &x.ResolvedAt, &x.Version, &x.CreatedAt, &x.UpdatedAt); err != nil {
		return nil, err
	}
	x.State = domain.RequestState(state)
	return x, nil
}

func (r *PostgresRepository) oneRequest(ctx context.Context, where string, lock bool, args ...any) (*domain.ExternalRequest, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	sql := `SELECT ` + requestColumns + ` FROM external_record_requests WHERE tenant_id = $1 AND ` + where
	if lock {
		sql += ` FOR UPDATE`
	}
	return scanRequest(r.q(ctx).QueryRow(ctx, sql, append([]any{tenant}, args...)...))
}

func (r *PostgresRepository) GetRequest(ctx context.Context, id uuid.UUID) (*domain.ExternalRequest, error) {
	x, err := r.oneRequest(ctx, `id = $2`, false, id)
	return x, mapErr(err)
}

func (r *PostgresRepository) LockRequest(ctx context.Context, id uuid.UUID) (*domain.ExternalRequest, error) {
	x, err := r.oneRequest(ctx, `id = $2`, true, id)
	return x, mapErr(err)
}

// AcquireRequest claims the right to call the ERP. Exactly one of the three results is set: the new in-flight request (the caller owns the call); the request this
// Idempotency-Key already made (a retry: the caller must NOT call again); or the request that blocks this session (in flight / outcome unknown).
func (r *PostgresRepository) AcquireRequest(ctx context.Context, x *domain.ExternalRequest) (*domain.ExternalRequest, *domain.ExternalRequest, *domain.ExternalRequest, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := r.LockScopedIdempotency(ctx, "ext-request", x.IdempotencyKey); err != nil {
		return nil, nil, nil, err
	}
	if ex, err := r.oneRequest(ctx, `idempotency_key = $2`, false, x.IdempotencyKey); err == nil {
		return nil, ex, nil, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, err
	}
	// a different key, but this session already has a request that may have been processed
	if b, err := r.oneRequest(ctx, `session_id = $2 AND operation = $3 AND state IN ('in_flight','outcome_unknown')`, false, x.SessionID, x.Operation); err == nil {
		return nil, nil, b, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, err
	}
	tag, err := r.q(ctx).Exec(ctx, `
		INSERT INTO external_record_requests (id, tenant_id, integration_id, session_id, case_id, operation, title, idempotency_key, request_hash, correlation_key, state, requested_by_user_id, version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'in_flight',$11,1,$12,$12)
		ON CONFLICT DO NOTHING`,
		x.ID, tenant, x.IntegrationID, x.SessionID, x.CaseID, x.Operation, x.Title, x.IdempotencyKey, x.RequestHash, x.CorrelationKey, x.RequestedBy, x.CreatedAt)
	if err != nil {
		return nil, nil, nil, mapErr(err)
	}
	if tag.RowsAffected() == 1 {
		x.State, x.Version = domain.RequestInFlight, 1
		return x, nil, nil, nil
	}
	// somebody else won the race on one of the unique indexes: read who
	if ex, err := r.oneRequest(ctx, `idempotency_key = $2`, false, x.IdempotencyKey); err == nil {
		return nil, ex, nil, nil
	}
	if b, err := r.oneRequest(ctx, `session_id = $2 AND operation = $3 AND state IN ('in_flight','outcome_unknown')`, false, x.SessionID, x.Operation); err == nil {
		return nil, nil, b, nil
	}
	return nil, nil, nil, errors.New("servicectx: the request was refused by a unique index and nobody holds it")
}

func (r *PostgresRepository) ListRequests(ctx context.Context, f ports.RequestFilter) ([]domain.ExternalRequest, string, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, "", err
	}
	where := []string{"tenant_id = $1"}
	args := []any{tenant}
	if f.State != "" {
		args = append(args, string(f.State))
		where = append(where, fmt.Sprintf("state = $%d", len(args)))
	}
	if f.SessionID != nil {
		args = append(args, *f.SessionID)
		where = append(where, fmt.Sprintf("session_id = $%d", len(args)))
	}
	if f.Cursor != "" {
		t, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, t, id)
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	rows, err := r.q(ctx).Query(ctx, `SELECT `+requestColumns+` FROM external_record_requests WHERE `+strings.Join(where, " AND ")+
		fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []domain.ExternalRequest
	for rows.Next() {
		x, err := scanRequest(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *x)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}

func (r *PostgresRepository) UpdateRequest(ctx context.Context, x *domain.ExternalRequest, expectedVersion int) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	row := r.q(ctx).QueryRow(ctx, `
		UPDATE external_record_requests SET state=$3, external_record_id=$4, failure_class=$5, resolution_source=$6, resolved_by_user_id=$7, resolved_at=$8
		WHERE tenant_id=$1 AND id=$2 AND version=$9 RETURNING version, updated_at`,
		tenant, x.ID, string(x.State), x.ExternalRecordID, x.FailureClass, x.ResolutionSource, x.ResolvedBy, x.ResolvedAt, expectedVersion)
	if err := row.Scan(&x.Version, &x.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var one int
			if e := r.q(ctx).QueryRow(ctx, `SELECT 1 FROM external_record_requests WHERE tenant_id=$1 AND id=$2`, tenant, x.ID).Scan(&one); e != nil {
				return domain.ErrNotFound
			}
			return domain.ErrConflict
		}
		return mapErr(err)
	}
	return nil
}

// InNewTransaction runs fn in a separate transaction, for the same company and the same actor, committed when fn returns. It never uses the request's transaction.
func (r *PostgresRepository) InNewTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return errors.New("servicectx: tenant context required")
	}
	base := context.WithoutCancel(ctx) // the outcome of a call to the ERP must be written even if the caller went away
	switch tc.Source {
	case tenancydomain.AccessSourceSystem:
		return platformdb.WithSystemTenantSession(base, r.pool, tc.TenantID, func(sc context.Context) error { return fn(sc) })
	case tenancydomain.AccessSourceDirect:
		return platformdb.WithTenantSession(base, r.pool, tc.ActorID, false, func(sc context.Context) error {
			return fn(tenancydomain.WithTenantContext(sc, tc))
		})
	default:
		return domain.ErrForbidden // a Hub delegate never asks an ERP for anything (service.request_external is not delegable)
	}
}

func (r *PostgresRepository) ActiveLinks(ctx context.Context, recordID uuid.UUID) ([]domain.ExternalLink, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT `+linkColumns+` FROM service_external_links WHERE tenant_id=$1 AND external_record_id=$2 AND unlinked_at IS NULL ORDER BY linked_at, id`, tenant, recordID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ExternalLink
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) ExternalRecordsForContext(ctx context.Context, contactID uuid.UUID, customerID *uuid.UUID, needsSelection bool, limit int) ([]domain.ExternalRecord, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	cols := make([]string, 0, 16)
	for _, c := range strings.Split(recordColumns, ",") {
		cols = append(cols, "x."+strings.TrimSpace(c))
	}
	// a record is shown through the sessions of the contact, or through the cases those sessions belong to; the customer of the target decides who may see it
	rows, err := r.q(ctx).Query(ctx, `SELECT `+strings.Join(cols, ", ")+` FROM external_records x
		WHERE x.tenant_id = $1 AND EXISTS (
		  SELECT 1 FROM service_external_links l
		  LEFT JOIN service_sessions s ON s.tenant_id = l.tenant_id AND s.id = l.session_id
		  LEFT JOIN service_cases c ON c.tenant_id = l.tenant_id AND c.id = l.case_id
		  LEFT JOIN service_case_sessions cs ON cs.tenant_id = c.tenant_id AND cs.case_id = c.id
		  LEFT JOIN service_sessions s2 ON s2.tenant_id = cs.tenant_id AND s2.id = cs.session_id
		  WHERE l.tenant_id = x.tenant_id AND l.external_record_id = x.id AND l.unlinked_at IS NULL
		    AND (s.contact_id = $2 OR s2.contact_id = $2)
		    AND (COALESCE(c.customer_id, s.customer_id) IS NULL OR (NOT $4 AND ($3::uuid IS NULL OR COALESCE(c.customer_id, s.customer_id) = $3))))
		ORDER BY x.updated_at DESC, x.id DESC LIMIT $5`, tenant, contactID, customerID, needsSelection, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ExternalRecord
	for rows.Next() {
		x, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}
