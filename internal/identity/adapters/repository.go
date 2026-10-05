package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/identity/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Repository runs inside the caller's tenant session (RLS); every statement also filters by tenant_id.
type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

const identityColumns = `id, tenant_id, user_id, identity_type, scope, raw_value, normalized_value, status, verification_source, verified_at, revoked_at, created_at`

func scanIdentity(row pgx.Row) (*domain.Identity, error) {
	var i domain.Identity
	var typ, status string
	var src *string
	if err := row.Scan(&i.ID, &i.TenantID, &i.UserID, &typ, &i.Scope, &i.Raw, &i.Normalized, &status, &src, &i.VerifiedAt, &i.RevokedAt, &i.CreatedAt); err != nil {
		return nil, err
	}
	i.Type, i.Status = domain.Type(typ), domain.Status(status)
	if src != nil {
		s := domain.VerificationSource(*src)
		i.VerificationSource = &s
	}
	return &i, nil
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// Create stores a PENDING identity: pending never matches an inbound message. The user must be a member of the tenant.
func (r *Repository) Create(ctx context.Context, tenantID, actorID, userID uuid.UUID, typ domain.Type, scope, raw, normalized string) (*domain.Identity, error) {
	if !typ.Valid() {
		return nil, domain.ErrInvalidIdentity
	}
	var member bool
	if err := r.q(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE tenant_id=$1 AND user_id=$2)`, tenantID, userID).Scan(&member); err != nil {
		return nil, err
	}
	if !member {
		return nil, domain.ErrUserNotInTenant
	}
	i, err := scanIdentity(r.q(ctx).QueryRow(ctx, `
		INSERT INTO user_channel_identities (tenant_id, user_id, identity_type, scope, raw_value, normalized_value, created_by_user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING `+identityColumns, tenantID, userID, string(typ), scope, raw, normalized, nullable(actorID)))
	if isUnique(err) {
		return nil, domain.ErrDuplicate
	}
	return i, err
}

func nullable(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func (r *Repository) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Identity, error) {
	i, err := scanIdentity(r.q(ctx).QueryRow(ctx, `SELECT `+identityColumns+` FROM user_channel_identities WHERE tenant_id=$1 AND id=$2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return i, err
}

// List returns identities newest first, optionally of one user and/or one status.
func (r *Repository) List(ctx context.Context, tenantID uuid.UUID, userID *uuid.UUID, status *domain.Status) ([]domain.Identity, error) {
	rows, err := r.q(ctx).Query(ctx, `SELECT `+identityColumns+` FROM user_channel_identities
		WHERE tenant_id=$1 AND ($2::uuid IS NULL OR user_id=$2) AND ($3::text IS NULL OR status=$3)
		ORDER BY created_at DESC, id LIMIT 200`, tenantID, userID, statusArg(status))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Identity{}
	for rows.Next() {
		i, err := scanIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

func statusArg(s *domain.Status) *string {
	if s == nil {
		return nil
	}
	v := string(*s)
	return &v
}

// VerifyResult carries the conflicts opened by the verification so the caller can audit them.
type VerifyResult struct {
	Identity  *domain.Identity
	Conflicts []domain.Conflict
}

// Verify makes a pending identity verified and opens one conflict per existing external Contact that matches it. The
// verification itself is not blocked by a conflict: the resolver keeps customer automation OFF for that identity until a
// human resolves it. Another verified owner of the same identity is domain.ErrAlreadyVerified.
func (r *Repository) Verify(ctx context.Context, tenantID, actorID, id uuid.UUID, source domain.VerificationSource) (*VerifyResult, error) {
	if !source.Valid() {
		return nil, domain.ErrSourceNotAllowed
	}
	cur, err := r.lock(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if cur.Status != domain.StatusPending {
		return nil, domain.ErrInvalidState
	}
	if _, err := r.q(ctx).Exec(ctx, `UPDATE user_channel_identities SET status='verified', verification_source=$3, verified_at=now(), verified_by_user_id=$4, updated_at=now() WHERE tenant_id=$1 AND id=$2`,
		tenantID, id, string(source), nullable(actorID)); err != nil {
		if isUnique(err) {
			return nil, domain.ErrAlreadyVerified
		}
		return nil, err
	}
	res := &VerifyResult{}
	if res.Identity, err = r.Get(ctx, tenantID, id); err != nil {
		return nil, err
	}
	contacts, err := r.matchingContacts(ctx, res.Identity)
	if err != nil {
		return nil, err
	}
	for _, c := range contacts {
		var conf domain.Conflict
		err := r.q(ctx).QueryRow(ctx, `
			INSERT INTO identity_resolution_conflicts (tenant_id, identity_id, contact_id) VALUES ($1,$2,$3)
			ON CONFLICT (tenant_id, identity_id, contact_id) WHERE status='open' DO NOTHING
			RETURNING id, identity_id, contact_id, status, detected_at`, tenantID, id, c).Scan(&conf.ID, &conf.IdentityID, &conf.ContactID, &conf.Status, &conf.DetectedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		res.Conflicts = append(res.Conflicts, conf)
	}
	// ADR-0018: an open conflict makes the contact's conversations "unclassified"; a newly verified identity may turn a
	// group's participant internal. Re-derive in the same transaction.
	for _, c := range res.Conflicts {
		if err := r.recompute(ctx, tenantID, &c.ContactID); err != nil {
			return nil, err
		}
	}
	if err := r.recompute(ctx, tenantID, nil); err != nil {
		return nil, err
	}
	return res, nil
}

// recompute re-derives conversation_kind: for one contact's conversations (when given) and for every group of the tenant.
func (r *Repository) recompute(ctx context.Context, tenantID uuid.UUID, contactID *uuid.UUID) error {
	if contactID != nil {
		var n int
		if err := r.q(ctx).QueryRow(ctx, `SELECT recompute_contact_conversation_kinds($1,$2)`, tenantID, *contactID).Scan(&n); err != nil {
			return err
		}
	}
	var n int
	return r.q(ctx).QueryRow(ctx, `SELECT recompute_tenant_group_kinds($1)`, tenantID).Scan(&n)
}

// matchingContacts finds the tenant's external contacts the identity also names (by phone, e-mail or participant link).
func (r *Repository) matchingContacts(ctx context.Context, i *domain.Identity) ([]uuid.UUID, error) {
	var q string
	var args []any
	switch i.Type {
	case domain.TypePhone:
		q, args = `SELECT id FROM contacts WHERE tenant_id=$1 AND phone_e164=$2`, []any{i.TenantID, i.Normalized}
	case domain.TypeEmail:
		q, args = `SELECT id FROM contacts WHERE tenant_id=$1 AND email <> '' AND lower(btrim(email))=$2`, []any{i.TenantID, i.Normalized}
	default:
		q, args = `SELECT DISTINCT cp.contact_id FROM channel_participants cp
			WHERE cp.tenant_id=$1 AND cp.contact_id IS NOT NULL AND lower(cp.external_participant_id)=$2
			  AND lower(cp.provider) || ':' || cp.channel_connection_id::text = $3`, []any{i.TenantID, i.Normalized, i.Scope}
	}
	rows, err := r.q(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *Repository) lock(ctx context.Context, tenantID, id uuid.UUID) (*domain.Identity, error) {
	i, err := scanIdentity(r.q(ctx).QueryRow(ctx, `SELECT `+identityColumns+` FROM user_channel_identities WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return i, err
}

// Revoke stops an identity from matching anything. Its open conflicts are resolved as identity_revoked.
func (r *Repository) Revoke(ctx context.Context, tenantID, actorID, id uuid.UUID) (*domain.Identity, error) {
	cur, err := r.lock(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if cur.Status == domain.StatusRevoked {
		return nil, domain.ErrInvalidState
	}
	if _, err := r.q(ctx).Exec(ctx, `UPDATE user_channel_identities SET status='revoked', revoked_at=now(), revoked_by_user_id=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id, nullable(actorID)); err != nil {
		return nil, err
	}
	rows, err := r.q(ctx).Query(ctx, `UPDATE identity_resolution_conflicts SET status='resolved', resolution='identity_revoked', resolved_at=now(), resolved_by_user_id=$3 WHERE tenant_id=$1 AND identity_id=$2 AND status='open' RETURNING contact_id`, tenantID, id, nullable(actorID))
	if err != nil {
		return nil, err
	}
	var contacts []uuid.UUID
	for rows.Next() {
		var c uuid.UUID
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return nil, err
		}
		contacts = append(contacts, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range contacts {
		if err := r.recompute(ctx, tenantID, &contacts[i]); err != nil {
			return nil, err
		}
	}
	if err := r.recompute(ctx, tenantID, nil); err != nil {
		return nil, err
	}
	return r.Get(ctx, tenantID, id)
}

const conflictColumns = `id, identity_id, contact_id, status, resolution, note, detected_at, resolved_at`

func scanConflict(row pgx.Row) (*domain.Conflict, error) {
	var c domain.Conflict
	var res *string
	if err := row.Scan(&c.ID, &c.IdentityID, &c.ContactID, &c.Status, &res, &c.Note, &c.DetectedAt, &c.ResolvedAt); err != nil {
		return nil, err
	}
	if res != nil {
		v := domain.Resolution(*res)
		c.Resolution = &v
	}
	return &c, nil
}

func (r *Repository) ListConflicts(ctx context.Context, tenantID uuid.UUID, openOnly bool) ([]domain.Conflict, error) {
	q := `SELECT ` + conflictColumns + ` FROM identity_resolution_conflicts WHERE tenant_id=$1`
	if openOnly {
		q += ` AND status='open'`
	}
	rows, err := r.q(ctx).Query(ctx, q+` ORDER BY detected_at DESC, id LIMIT 200`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Conflict{}
	for rows.Next() {
		c, err := scanConflict(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ResolveConflict closes a conflict. identity_revoked also revokes the identity (the human decided it was wrong).
func (r *Repository) ResolveConflict(ctx context.Context, tenantID, actorID, conflictID uuid.UUID, res domain.Resolution, note string) (*domain.Conflict, error) {
	if !res.Valid() {
		return nil, domain.ErrInvalidResolution
	}
	c, err := scanConflict(r.q(ctx).QueryRow(ctx, `SELECT `+conflictColumns+` FROM identity_resolution_conflicts WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, conflictID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrConflictNotFound
	}
	if err != nil {
		return nil, err
	}
	if c.Status != "open" {
		return nil, domain.ErrConflictNotOpen
	}
	var n *string
	if note != "" {
		n = &note
	}
	if res == domain.ResolutionIdentityRevoked {
		if _, err := r.Revoke(ctx, tenantID, actorID, c.IdentityID); err != nil && !errors.Is(err, domain.ErrInvalidState) {
			return nil, err
		}
	}
	// Revoke resolves every open conflict of the identity; a confirmation resolves just this one.
	if _, err := r.q(ctx).Exec(ctx, `UPDATE identity_resolution_conflicts SET status='resolved', resolution=$3, note=$4, resolved_at=now(), resolved_by_user_id=$5 WHERE tenant_id=$1 AND id=$2 AND status='open'`,
		tenantID, conflictID, string(res), n, nullable(actorID)); err != nil {
		return nil, err
	}
	if err := r.recompute(ctx, tenantID, &c.ContactID); err != nil {
		return nil, err
	}
	return scanConflict(r.q(ctx).QueryRow(ctx, `SELECT `+conflictColumns+` FROM identity_resolution_conflicts WHERE tenant_id=$1 AND id=$2`, tenantID, conflictID))
}

// FindVerified is what the inbound resolver calls: the ACTIVE staff member that owns a verified identity, or nil. A
// revoked/inactive membership never matches; a pending or revoked identity never matches.
func (r *Repository) FindVerified(ctx context.Context, tenantID uuid.UUID, typ domain.Type, scope, normalized string) (*domain.Match, error) {
	var m domain.Match
	err := r.q(ctx).QueryRow(ctx, `
		SELECT i.id, i.user_id,
		       EXISTS(SELECT 1 FROM identity_resolution_conflicts c WHERE c.tenant_id=i.tenant_id AND c.identity_id=i.id AND c.status='open'),
		       EXISTS(SELECT 1 FROM identity_resolution_conflicts c WHERE c.tenant_id=i.tenant_id AND c.identity_id=i.id AND c.resolution='confirmed_internal')
		FROM user_channel_identities i
		JOIN memberships m ON m.tenant_id=i.tenant_id AND m.user_id=i.user_id AND m.status='active'
		WHERE i.tenant_id=$1 AND i.identity_type=$2 AND i.scope=$3 AND i.normalized_value=$4 AND i.status='verified'`,
		tenantID, string(typ), scope, normalized).Scan(&m.IdentityID, &m.UserID, &m.HasOpenConflict, &m.ConflictConfirmed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}
