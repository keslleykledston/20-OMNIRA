package adapters

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/attendance/domain"
	"github.com/omnira/omnira/internal/attendance/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// PostgresRepository runs every statement on the request's tenant-scoped transaction (RLS by membership) and always
// filters by the tenant of the TenantContext as well: defense in depth, never the only guard.
type PostgresRepository struct{ pool *pgxpool.Pool }

var _ ports.Repository = (*PostgresRepository)(nil)

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

func tenantOf(ctx context.Context) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, errors.New("attendance: tenant context required")
	}
	return tc.TenantID, nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

func (r *PostgresRepository) LockConversation(ctx context.Context, id uuid.UUID) (*ports.ConversationFacts, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	f := &ports.ConversationFacts{}
	err = r.q(ctx).QueryRow(ctx, `
		SELECT id, contact_id, status, conversation_kind, assigned_to_user_id
		FROM conversations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, id).
		Scan(&f.ID, &f.ContactID, &f.Status, &f.Kind, &f.AssignedTo)
	if err != nil {
		return nil, notFound(err)
	}
	return f, nil
}

func (r *PostgresRepository) ContactOfConversation(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	var contact *uuid.UUID
	if err := r.q(ctx).QueryRow(ctx, `SELECT contact_id FROM conversations WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&contact); err != nil {
		return uuid.Nil, notFound(err)
	}
	if contact == nil {
		return uuid.Nil, nil
	}
	return *contact, nil
}

const closureColumns = `id, tenant_id, conversation_id, contact_id, closed_by_user_id, source, reason, note, summary, summary_truth,
	local_tickets_closed, tickets_kept, created_at`

func scanClosure(row pgx.Row) (*domain.Closure, error) {
	c := &domain.Closure{}
	if err := row.Scan(&c.ID, &c.TenantID, &c.ConversationID, &c.ContactID, &c.ClosedBy, &c.Source, &c.Reason, &c.Note, &c.Summary,
		&c.SummaryTruth, &c.LocalTicketsClosed, &c.TicketsKept, &c.CreatedAt); err != nil {
		return nil, err
	}
	return c, nil
}

func (r *PostgresRepository) ClosureByConversation(ctx context.Context, conversationID uuid.UUID) (*domain.Closure, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	c, err := scanClosure(r.q(ctx).QueryRow(ctx, `SELECT `+closureColumns+` FROM conversation_closures WHERE tenant_id=$1 AND conversation_id=$2`, tenant, conversationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

const followUpColumns = `id, tenant_id, contact_id, conversation_id, closure_id, kind, text, owner_user_id, due_at, status, truth,
	created_by_user_id, created_at, resolved_at, resolved_by_user_id, resolution_note`

func scanFollowUp(row pgx.Row) (*domain.FollowUp, error) {
	f := &domain.FollowUp{}
	if err := row.Scan(&f.ID, &f.TenantID, &f.ContactID, &f.ConversationID, &f.ClosureID, &f.Kind, &f.Text, &f.OwnerUserID, &f.DueAt, &f.Status,
		&f.Truth, &f.CreatedBy, &f.CreatedAt, &f.ResolvedAt, &f.ResolvedBy, &f.ResolutionNote); err != nil {
		return nil, err
	}
	return f, nil
}

func (r *PostgresRepository) followUps(ctx context.Context, where string, args ...any) ([]domain.FollowUp, error) {
	rows, err := r.q(ctx).Query(ctx, `SELECT `+followUpColumns+` FROM follow_up_items WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.FollowUp{}
	for rows.Next() {
		f, err := scanFollowUp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) FollowUpsOfClosure(ctx context.Context, closureID uuid.UUID) ([]domain.FollowUp, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return r.followUps(ctx, `tenant_id=$1 AND closure_id=$2 ORDER BY created_at, id`, tenant, closureID)
}

func (r *PostgresRepository) IsActiveMember(ctx context.Context, userID uuid.UUID) (bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	var ok bool
	err = r.q(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE tenant_id=$1 AND user_id=$2 AND status='active')`, tenant, userID).Scan(&ok)
	return ok, err
}

func (r *PostgresRepository) CloseLocalTickets(ctx context.Context, conversationID uuid.UUID) (closed, kept int, err error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return 0, 0, err
	}
	// Only local tickets that belong to this conversation alone are closed. An ERP-linked ticket is authoritative there
	// (ADR-0013) and a topic-scoped one belongs to a subject that may cross conversations (ADR-0017): both are left, and counted.
	err = r.q(ctx).QueryRow(ctx, `
		WITH closed AS (
		  UPDATE tickets SET status='closed', closed_at=now(), updated_at=now()
		  WHERE tenant_id=$1 AND conversation_id=$2 AND status IN ('open','in_progress','waiting')
		    AND external_ticket_id IS NULL AND NOT topic_scoped
		  RETURNING 1)
		SELECT (SELECT count(*) FROM closed)::int,
		       (SELECT count(*) FROM tickets WHERE tenant_id=$1 AND conversation_id=$2 AND status IN ('open','in_progress','waiting')
		          AND (external_ticket_id IS NOT NULL OR topic_scoped))::int`, tenant, conversationID).Scan(&closed, &kept)
	return closed, kept, err
}

func (r *PostgresRepository) MarkClosed(ctx context.Context, conversationID uuid.UUID) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE conversations SET status='closed', closed_at=now(), updated_at=now(), automation_mode='none'
		WHERE tenant_id=$1 AND id=$2 AND status='open'`, tenant, conversationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("attendance: conversation %s was not open", conversationID)
	}
	return nil
}

func (r *PostgresRepository) InsertClosure(ctx context.Context, c *domain.Closure) error {
	_, err := r.q(ctx).Exec(ctx, `
		INSERT INTO conversation_closures (id, tenant_id, conversation_id, contact_id, closed_by_user_id, source, reason, note, summary,
		  summary_truth, local_tickets_closed, tickets_kept, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		c.ID, c.TenantID, c.ConversationID, c.ContactID, c.ClosedBy, c.Source, c.Reason, c.Note, c.Summary, c.SummaryTruth,
		c.LocalTicketsClosed, c.TicketsKept, c.CreatedAt)
	return err
}

func (r *PostgresRepository) InsertFollowUps(ctx context.Context, items []domain.FollowUp) error {
	for _, f := range items {
		if _, err := r.q(ctx).Exec(ctx, `
			INSERT INTO follow_up_items (id, tenant_id, contact_id, conversation_id, closure_id, kind, text, owner_user_id, due_at, status, truth,
			  created_by_user_id, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			f.ID, f.TenantID, f.ContactID, f.ConversationID, f.ClosureID, f.Kind, f.Text, f.OwnerUserID, f.DueAt, f.Status, f.Truth,
			f.CreatedBy, f.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func (r *PostgresRepository) ListClosures(ctx context.Context, contactID uuid.UUID, limit int) ([]domain.Closure, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT `+closureColumns+` FROM conversation_closures WHERE tenant_id=$1 AND contact_id=$2
		ORDER BY created_at DESC, id LIMIT $3`, tenant, contactID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Closure{}
	for rows.Next() {
		c, err := scanClosure(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) ListFollowUps(ctx context.Context, contactID uuid.UUID, status domain.FollowUpStatus, limit int) ([]domain.FollowUp, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	// overdue and dated items first, then the undated ones by recency
	return r.followUps(ctx, `tenant_id=$1 AND contact_id=$2 AND status=$3 ORDER BY (due_at IS NULL), due_at, created_at DESC, id LIMIT $4`,
		tenant, contactID, string(status), limit)
}

func (r *PostgresRepository) LockFollowUp(ctx context.Context, id uuid.UUID) (*domain.FollowUp, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	f, err := scanFollowUp(r.q(ctx).QueryRow(ctx, `SELECT `+followUpColumns+` FROM follow_up_items WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, id))
	if err != nil {
		return nil, notFound(err)
	}
	return f, nil
}

func (r *PostgresRepository) ResolveFollowUp(ctx context.Context, id uuid.UUID, status domain.FollowUpStatus, note string, by uuid.UUID) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE follow_up_items SET status=$3, resolved_at=now(), resolved_by_user_id=$4, resolution_note=$5
		WHERE tenant_id=$1 AND id=$2 AND status='open'`, tenant, id, string(status), by, note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrAlreadyHandled
	}
	return nil
}

// Authorizer answers from the role -> permission matrix by active membership (never by role name).
type Authorizer struct{ pool *pgxpool.Pool }

var _ ports.Authorizer = (*Authorizer)(nil)

func NewAuthorizer(pool *pgxpool.Pool) *Authorizer { return &Authorizer{pool: pool} }

func (a *Authorizer) Has(ctx context.Context, userID uuid.UUID, permission string) (bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	var ok bool
	err = platformdb.QuerierFromContext(ctx, a.pool).QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM memberships m JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`, tenant, userID, permission).Scan(&ok)
	return ok, err
}
