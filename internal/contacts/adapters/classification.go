package adapters

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/contacts/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ClassificationRepository changes a contact's classification and its account links. It runs inside the caller's tenant
// session (one request = one transaction), so every method is atomic with the others of the same request. The database
// also enforces the invariants (deferred trigger, partial unique indexes); this layer turns them into domain errors
// BEFORE the commit so the caller sees a precise reason.
type ClassificationRepository struct{ pool *pgxpool.Pool }

func NewClassificationRepository(pool *pgxpool.Pool) *ClassificationRepository {
	return &ClassificationRepository{pool: pool}
}

func (r *ClassificationRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

// Link is one contact<->account relationship.
type Link struct {
	ID           uuid.UUID
	ContactID    uuid.UUID
	AccountID    uuid.UUID
	AccountName  string
	Relationship domain.RelationshipType
	Status       string
	Primary      bool
	Source       domain.ClassificationSource
	Confidence   *float64
	CreatedAt    time.Time
	EndedAt      *time.Time
}

// Change is what Classify reports so the caller can audit it.
type Change struct {
	PreviousKind domain.ContactKind
	Kind         domain.ContactKind
	// PreviousRole / Role: the internal subtype (empty unless the kind is internal).
	PreviousRole domain.InternalRole
	Role         domain.InternalRole
	Changed      bool
	// ConversationsRecomputed / GroupsRecomputed: how many conversation_kind values the reclassification changed or
	// re-derived (ADR-0018), so the caller can audit it.
	ConversationsRecomputed int
	GroupsRecomputed        int
}

// recomputeKinds re-derives conversation_kind for the contact's 1:1 conversations and for the groups it takes part in.
// It runs in the SAME transaction as the reclassification: nothing observes a customer contact with stale kinds.
func (r *ClassificationRepository) recomputeKinds(ctx context.Context, tenantID, contactID uuid.UUID, ch *Change) error {
	if tc, err := tenancydomain.FromContext(ctx); err == nil && actingForHub(tc) {
		// a Hub agent cannot write conversations: the same two recomputations through the narrow function of migration 110 (it checks the key)
		return r.q(ctx).QueryRow(ctx, `SELECT conversations, groups FROM delegated_recompute_contact_kinds($1,$2)`, tenantID, contactID).Scan(&ch.ConversationsRecomputed, &ch.GroupsRecomputed)
	}
	if err := r.q(ctx).QueryRow(ctx, `SELECT recompute_contact_conversation_kinds($1,$2)`, tenantID, contactID).Scan(&ch.ConversationsRecomputed); err != nil {
		return err
	}
	return r.q(ctx).QueryRow(ctx, `SELECT recompute_groups_for_contact($1,$2)`, tenantID, contactID).Scan(&ch.GroupsRecomputed)
}

// lockContact reads the kind under FOR UPDATE so concurrent reclassifications serialise.
func (r *ClassificationRepository) lockContact(ctx context.Context, tenantID, contactID uuid.UUID) (domain.ContactKind, error) {
	k, _, err := r.lockContactFull(ctx, tenantID, contactID)
	return k, err
}

func (r *ClassificationRepository) lockContactFull(ctx context.Context, tenantID, contactID uuid.UUID) (domain.ContactKind, domain.InternalRole, error) {
	var k string
	var role *string
	err := r.q(ctx).QueryRow(ctx, `SELECT kind, internal_role FROM contacts WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, contactID).Scan(&k, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", domain.ErrContactNotFound
	}
	if role == nil {
		return domain.ContactKind(k), "", err
	}
	return domain.ContactKind(k), domain.InternalRole(*role), err
}

func (r *ClassificationRepository) activeLinks(ctx context.Context, tenantID, contactID uuid.UUID) (int, error) {
	var n int
	err := r.q(ctx).QueryRow(ctx, `SELECT count(*) FROM contact_account_links WHERE tenant_id=$1 AND contact_id=$2 AND status='active'`, tenantID, contactID).Scan(&n)
	return n, err
}

// Classify sets the kind with its source and actor. unclassified -> customer (or any -> customer) must bring at least
// one account link in the same call or already hold an active one: the transition and the link are one transaction.
// Reclassifying away from customer keeps the links (history is never erased) unless endLinks is true.
func (r *ClassificationRepository) Classify(ctx context.Context, tenantID, actorID, contactID uuid.UUID, kind domain.ContactKind, source domain.ClassificationSource, links []domain.AccountLinkInput, endLinks bool) (Change, error) {
	return r.ClassifyWithRole(ctx, tenantID, actorID, contactID, kind, source, links, endLinks, "")
}

// ClassifyWithRole is Classify for every kind, including internal, which needs its role (team, partner or supplier) and
// can only be decided by a person (source manual): automation, imports and AI suggestions never mark anyone internal,
// because an internal contact gets no bot, ticket or SLA and a customer mislabeled that way would be silently ignored.
// The role is meaningless for any other kind and is refused there.
func (r *ClassificationRepository) ClassifyWithRole(ctx context.Context, tenantID, actorID, contactID uuid.UUID, kind domain.ContactKind, source domain.ClassificationSource, links []domain.AccountLinkInput, endLinks bool, role domain.InternalRole) (Change, error) {
	if !kind.Valid() || !source.Valid() {
		return Change{}, domain.ErrInvalidInput
	}
	if kind == domain.KindInternal {
		if !role.Valid() {
			return Change{}, domain.ErrInternalNeedsRole
		}
		if source != domain.SourceManual {
			return Change{}, domain.ErrInvalidInput
		}
	} else if role != "" {
		return Change{}, domain.ErrInvalidInput
	}
	prev, prevRole, err := r.lockContactFull(ctx, tenantID, contactID)
	if err != nil {
		return Change{}, err
	}
	if tc, terr := tenancydomain.FromContext(ctx); terr == nil && actingForHub(tc) && prev == domain.KindInternal {
		// the instance decided this person is internal (no bot, ticket or SLA): that is not a Hub agent's call to undo either
		return Change{}, domain.ErrInternalIsTheInstancesCall
	}
	for _, in := range links {
		if _, err := r.upsertLink(ctx, tenantID, actorID, contactID, in, source); err != nil {
			return Change{}, err
		}
	}
	if kind == domain.KindCustomer {
		n, err := r.activeLinks(ctx, tenantID, contactID)
		if err != nil {
			return Change{}, err
		}
		if n == 0 {
			return Change{}, domain.ErrCustomerNeedsAccount
		}
	}
	if endLinks && kind != domain.KindCustomer {
		if _, err := r.q(ctx).Exec(ctx, `UPDATE contact_account_links SET status='ended', ended_at=now(), is_primary=false, updated_at=now() WHERE tenant_id=$1 AND contact_id=$2 AND status='active'`, tenantID, contactID); err != nil {
			return Change{}, err
		}
	}
	ch := Change{PreviousKind: prev, Kind: kind, PreviousRole: prevRole, Role: role, Changed: prev != kind || prevRole != role}
	if ch.Changed {
		var roleArg *string
		if role != "" {
			v := string(role)
			roleArg = &v
		}
		if _, err := r.q(ctx).Exec(ctx, `UPDATE contacts SET kind=$3, internal_role=$6, classification_source=$4, classified_at=now(), classified_by_user_id=$5, updated_at=now() WHERE tenant_id=$1 AND id=$2`,
			tenantID, contactID, string(kind), string(source), nullableUUID(actorID), roleArg); err != nil {
			return Change{}, mapPG(err)
		}
		// the role alone never changes a conversation's kind: only a kind change needs the derived kinds re-derived
		if prev != kind {
			if err := r.recomputeKinds(ctx, tenantID, contactID, &ch); err != nil {
				return Change{}, err
			}
		}
	}
	return ch, nil
}

func nullableUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func mapPG(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23514" {
		return domain.ErrCustomerNeedsAccount
	}
	return err
}

// LinkAccount adds (or refreshes) an active link. Idempotent per (contact, account); making it primary demotes the
// previous primary in the same statement flow. The account must exist in the tenant and not be archived.
func (r *ClassificationRepository) LinkAccount(ctx context.Context, tenantID, actorID, contactID uuid.UUID, in domain.AccountLinkInput, source domain.ClassificationSource) (*Link, error) {
	if !source.Valid() {
		return nil, domain.ErrInvalidInput
	}
	if _, err := r.lockContact(ctx, tenantID, contactID); err != nil {
		return nil, err
	}
	return r.upsertLink(ctx, tenantID, actorID, contactID, in, source)
}

func (r *ClassificationRepository) upsertLink(ctx context.Context, tenantID, actorID, contactID uuid.UUID, in domain.AccountLinkInput, source domain.ClassificationSource) (*Link, error) {
	if in.Source != "" {
		source = in.Source
	}
	if !source.Valid() {
		return nil, domain.ErrInvalidInput
	}
	rel := in.Relationship
	if rel == "" {
		rel = domain.RelOther
	}
	if !rel.Valid() || in.AccountID == uuid.Nil || (in.Confidence != nil && (*in.Confidence < 0 || *in.Confidence > 1)) {
		return nil, domain.ErrInvalidInput
	}
	var ok bool
	if err := r.q(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customer_accounts WHERE tenant_id=$1 AND id=$2 AND status <> 'archived')`, tenantID, in.AccountID).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrAccountMissing
	}
	if in.Primary { // at most one active primary per contact
		if _, err := r.q(ctx).Exec(ctx, `UPDATE contact_account_links SET is_primary=false, updated_at=now() WHERE tenant_id=$1 AND contact_id=$2 AND status='active' AND is_primary AND account_id <> $3`, tenantID, contactID, in.AccountID); err != nil {
			return nil, err
		}
	}
	var verifiedBy *uuid.UUID
	if in.VerifiedByActor {
		verifiedBy = nullableUUID(actorID)
	}
	var id uuid.UUID
	err := r.q(ctx).QueryRow(ctx, `
		INSERT INTO contact_account_links (tenant_id, contact_id, account_id, relationship_type, is_primary, source, confidence, created_by_user_id, verified_by_user_id, verified_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, CASE WHEN $9::uuid IS NULL THEN NULL ELSE now() END)
		ON CONFLICT (tenant_id, contact_id, account_id) WHERE status='active' DO UPDATE
		   SET relationship_type = EXCLUDED.relationship_type, is_primary = EXCLUDED.is_primary OR contact_account_links.is_primary,
		       updated_at = now()
		RETURNING id`,
		tenantID, contactID, in.AccountID, string(rel), in.Primary, string(source), in.Confidence, nullableUUID(actorID), verifiedBy).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.getLink(ctx, tenantID, id)
}

func (r *ClassificationRepository) getLink(ctx context.Context, tenantID, id uuid.UUID) (*Link, error) {
	return scanLink(r.q(ctx).QueryRow(ctx, linkSelect+` WHERE l.tenant_id=$1 AND l.id=$2`, tenantID, id))
}

const linkSelect = `SELECT l.id, l.contact_id, l.account_id, a.name, l.relationship_type, l.status, l.is_primary, l.source, l.confidence, l.created_at, l.ended_at
	FROM contact_account_links l JOIN customer_accounts a ON a.tenant_id=l.tenant_id AND a.id=l.account_id`

func scanLink(row pgx.Row) (*Link, error) {
	var l Link
	var rel, src string
	if err := row.Scan(&l.ID, &l.ContactID, &l.AccountID, &l.AccountName, &rel, &l.Status, &l.Primary, &src, &l.Confidence, &l.CreatedAt, &l.EndedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrLinkNotFound
		}
		return nil, err
	}
	l.Relationship, l.Source = domain.RelationshipType(rel), domain.ClassificationSource(src)
	return &l, nil
}

// ListLinks returns the contact's links, active first (ended ones are history).
func (r *ClassificationRepository) ListLinks(ctx context.Context, tenantID, contactID uuid.UUID, includeEnded bool) ([]Link, error) {
	q := linkSelect + ` WHERE l.tenant_id=$1 AND l.contact_id=$2`
	if !includeEnded {
		q += ` AND l.status='active'`
	}
	rows, err := r.q(ctx).Query(ctx, q+` ORDER BY l.status, l.is_primary DESC, a.name, l.id`, tenantID, contactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// EndLink soft-ends a link (never a physical delete). Ending the LAST active link of a customer is refused unless
// reclassifyTo is given: the contact is then reclassified in the same transaction.
func (r *ClassificationRepository) EndLink(ctx context.Context, tenantID, actorID, contactID, linkID uuid.UUID, reclassifyTo *domain.ClassificationSource, kindAfter domain.ContactKind) (Change, error) {
	prev, err := r.lockContact(ctx, tenantID, contactID)
	if err != nil {
		return Change{}, err
	}
	tag, err := r.q(ctx).Exec(ctx, `UPDATE contact_account_links SET status='ended', ended_at=now(), is_primary=false, updated_at=now() WHERE tenant_id=$1 AND contact_id=$2 AND id=$3 AND status='active'`, tenantID, contactID, linkID)
	if err != nil {
		return Change{}, err
	}
	if tag.RowsAffected() == 0 {
		return Change{}, domain.ErrLinkNotFound
	}
	ch := Change{PreviousKind: prev, Kind: prev}
	if prev == domain.KindCustomer {
		n, err := r.activeLinks(ctx, tenantID, contactID)
		if err != nil {
			return Change{}, err
		}
		if n == 0 {
			if reclassifyTo == nil || kindAfter == domain.KindCustomer || kindAfter == domain.KindInternal || !kindAfter.Valid() {
				return Change{}, domain.ErrLastLink
			}
			if _, err := r.q(ctx).Exec(ctx, `UPDATE contacts SET kind=$3, classification_source=$4, classified_at=now(), classified_by_user_id=$5, updated_at=now() WHERE tenant_id=$1 AND id=$2`,
				tenantID, contactID, string(kindAfter), string(*reclassifyTo), nullableUUID(actorID)); err != nil {
				return Change{}, mapPG(err)
			}
			ch.Kind, ch.Changed = kindAfter, true
			if err := r.recomputeKinds(ctx, tenantID, contactID, &ch); err != nil {
				return Change{}, err
			}
		}
	}
	return ch, nil
}

// SetPrimary makes one active link the contact's primary (the others stay active).
func (r *ClassificationRepository) SetPrimary(ctx context.Context, tenantID, contactID, linkID uuid.UUID) error {
	if _, err := r.lockContact(ctx, tenantID, contactID); err != nil {
		return err
	}
	if _, err := r.q(ctx).Exec(ctx, `UPDATE contact_account_links SET is_primary=false, updated_at=now() WHERE tenant_id=$1 AND contact_id=$2 AND status='active' AND is_primary AND id <> $3`, tenantID, contactID, linkID); err != nil {
		return err
	}
	tag, err := r.q(ctx).Exec(ctx, `UPDATE contact_account_links SET is_primary=true, updated_at=now() WHERE tenant_id=$1 AND contact_id=$2 AND id=$3 AND status='active'`, tenantID, contactID, linkID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrLinkNotFound
	}
	return nil
}
