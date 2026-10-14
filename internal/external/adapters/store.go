package adapters

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnira/omnira/internal/external/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Store is the administration of installations and credentials. It runs inside the HUMAN administrator's request transaction (the row
// policies require integration.read / integration.manage for that person); it never runs in a system session and the external API never calls it.
type Store struct{}

// IssuedCredential is returned once, by the call that created it. Token is the only place the secret ever exists.
type IssuedCredential struct {
	domain.Credential
	Token string
}

type CreateInput struct {
	Name              string
	Scopes            []domain.Scope
	QueueIDs          []uuid.UUID
	ChannelIDs        []uuid.UUID
	RatePerMin        int
	LeaseSeconds      *int
	AllowOutsideHours bool
	CredentialTTL     time.Duration
	CredentialLabel   string
}

// UpdateInput changes the editable fields; nil means "leave as is". Allow-lists, when present, REPLACE the previous ones ([] clears them).
type UpdateInput struct {
	Name              *string
	Scopes            []domain.Scope
	QueueIDs          *[]uuid.UUID
	ChannelIDs        *[]uuid.UUID
	RatePerMin        *int
	LeaseSeconds      *int
	ClearLease        bool
	AllowOutsideHours *bool
}

var ErrReferenceNotFound = errors.New("a queue or channel line of the allow-list does not exist in this company")

const installationColumns = `i.id, i.tenant_id, i.name, i.status, i.scopes, i.rate_limit_per_min, i.lease_seconds, i.allow_outside_hours,
	i.created_by, i.created_at, i.updated_at, i.suspended_at, i.revoked_at,
	COALESCE((SELECT array_agg(q.queue_id::text ORDER BY q.queue_id) FROM external_app_installation_queues q WHERE q.tenant_id = i.tenant_id AND q.installation_id = i.id), ARRAY[]::TEXT[]),
	COALESCE((SELECT array_agg(c.connection_id::text ORDER BY c.connection_id) FROM external_app_installation_channels c WHERE c.tenant_id = i.tenant_id AND c.installation_id = i.id), ARRAY[]::TEXT[])`

func scanInstallation(row pgx.Row) (domain.Installation, error) {
	var in domain.Installation
	var status string
	var scopes, qs, cs []string
	err := row.Scan(&in.ID, &in.TenantID, &in.Name, &status, &scopes, &in.RatePerMin, &in.LeaseSeconds, &in.AllowOutsideHours,
		&in.CreatedBy, &in.CreatedAt, &in.UpdatedAt, &in.SuspendedAt, &in.RevokedAt, &qs, &cs)
	if err != nil {
		return in, err
	}
	in.Status = domain.Status(status)
	for _, s := range scopes {
		in.Scopes = append(in.Scopes, domain.Scope(s))
	}
	in.QueueIDs, in.ChannelIDs = parseUUIDs(qs), parseUUIDs(cs)
	return in, nil
}

func isPgCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

func (Store) lock(ctx context.Context, q platformdb.Querier, tenantID, id uuid.UUID) (domain.Installation, error) {
	in, err := scanInstallation(q.QueryRow(ctx, `SELECT `+installationColumns+` FROM external_app_installations i WHERE i.tenant_id = $1 AND i.id = $2 FOR UPDATE OF i`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return in, domain.ErrNotFound
	}
	return in, err
}

func replaceAllowlists(ctx context.Context, q platformdb.Querier, tenantID, id uuid.UUID, queues, channels *[]uuid.UUID) error {
	if queues != nil {
		if _, err := q.Exec(ctx, `DELETE FROM external_app_installation_queues WHERE tenant_id = $1 AND installation_id = $2`, tenantID, id); err != nil {
			return err
		}
		for _, qid := range *queues {
			if _, err := q.Exec(ctx, `INSERT INTO external_app_installation_queues(tenant_id, installation_id, queue_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, tenantID, id, qid); err != nil {
				if isPgCode(err, "23503") {
					return ErrReferenceNotFound
				}
				return err
			}
		}
	}
	if channels != nil {
		if _, err := q.Exec(ctx, `DELETE FROM external_app_installation_channels WHERE tenant_id = $1 AND installation_id = $2`, tenantID, id); err != nil {
			return err
		}
		for _, cid := range *channels {
			if _, err := q.Exec(ctx, `INSERT INTO external_app_installation_channels(tenant_id, installation_id, connection_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, tenantID, id, cid); err != nil {
				if isPgCode(err, "23503") {
					return ErrReferenceNotFound
				}
				return err
			}
		}
	}
	return nil
}

func nameTakenBy(ctx context.Context, q platformdb.Querier, tenantID uuid.UUID, name string, except uuid.UUID) (bool, error) {
	var taken bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM external_app_installations WHERE tenant_id = $1 AND lower(name) = lower($2) AND status <> 'revoked' AND id <> $3)`, tenantID, name, except).Scan(&taken)
	return taken, err
}

// Create inserts the installation, its allow-lists and its first credential in the caller's transaction.
func (s Store) Create(ctx context.Context, q platformdb.Querier, tenantID, actor uuid.UUID, in CreateInput) (domain.Installation, IssuedCredential, error) {
	if taken, err := nameTakenBy(ctx, q, tenantID, in.Name, uuid.Nil); err != nil {
		return domain.Installation{}, IssuedCredential{}, err
	} else if taken {
		return domain.Installation{}, IssuedCredential{}, domain.ErrNameTaken
	}
	id := uuid.New()
	if _, err := q.Exec(ctx, `
		INSERT INTO external_app_installations(id, tenant_id, name, scopes, rate_limit_per_min, lease_seconds, allow_outside_hours, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		id, tenantID, in.Name, domain.ScopeStrings(in.Scopes), in.RatePerMin, in.LeaseSeconds, in.AllowOutsideHours, actor); err != nil {
		return domain.Installation{}, IssuedCredential{}, err
	}
	if err := replaceAllowlists(ctx, q, tenantID, id, &in.QueueIDs, &in.ChannelIDs); err != nil {
		return domain.Installation{}, IssuedCredential{}, err
	}
	cred, err := s.issue(ctx, q, tenantID, id, actor, in.CredentialLabel, in.CredentialTTL)
	if err != nil {
		return domain.Installation{}, IssuedCredential{}, err
	}
	inst, err := s.Get(ctx, q, tenantID, id)
	return inst, cred, err
}

func (Store) issue(ctx context.Context, q platformdb.Querier, tenantID, installationID, actor uuid.UUID, label string, ttl time.Duration) (IssuedCredential, error) {
	token, keyID, digest, err := domain.NewToken()
	if err != nil {
		return IssuedCredential{}, err
	}
	var c domain.Credential
	c.ID, c.InstallationID, c.KeyID, c.Label = uuid.New(), installationID, keyID, label
	err = q.QueryRow(ctx, `
		INSERT INTO external_app_credentials(id, tenant_id, installation_id, key_id, secret_digest, label, created_by, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7, now() + make_interval(secs => $8))
		RETURNING created_at, expires_at`, c.ID, tenantID, installationID, keyID, digest, label, actor, ttl.Seconds()).Scan(&c.CreatedAt, &c.ExpiresAt)
	if err != nil {
		return IssuedCredential{}, err
	}
	return IssuedCredential{Credential: c, Token: token}, nil
}

// IssueCredential adds a credential (rotation: the next one before the current one is revoked). At most two stay live. Serialised with every
// other change of the installation by the row lock.
func (s Store) IssueCredential(ctx context.Context, q platformdb.Querier, tenantID, installationID, actor uuid.UUID, label string, ttl time.Duration) (IssuedCredential, error) {
	inst, err := s.lock(ctx, q, tenantID, installationID)
	if err != nil {
		return IssuedCredential{}, err
	}
	if inst.Status == domain.StatusRevoked {
		return IssuedCredential{}, domain.ErrInstallationRevoked
	}
	var live int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM external_app_credentials WHERE tenant_id = $1 AND installation_id = $2 AND revoked_at IS NULL AND expires_at > now()`, tenantID, installationID).Scan(&live); err != nil {
		return IssuedCredential{}, err
	}
	if live >= domain.MaxLiveCredentials {
		return IssuedCredential{}, domain.ErrTooManyCredentials
	}
	return s.issue(ctx, q, tenantID, installationID, actor, label, ttl)
}

// RevokeCredential ends one credential now. The installation row is locked first so a concurrent write that holds it FOR SHARE finishes first.
func (s Store) RevokeCredential(ctx context.Context, q platformdb.Querier, tenantID, installationID, credentialID uuid.UUID) error {
	if _, err := s.lock(ctx, q, tenantID, installationID); err != nil {
		return err
	}
	tag, err := q.Exec(ctx, `UPDATE external_app_credentials SET revoked_at = now() WHERE tenant_id = $1 AND installation_id = $2 AND id = $3 AND revoked_at IS NULL`, tenantID, installationID, credentialID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (Store) Get(ctx context.Context, q platformdb.Querier, tenantID, id uuid.UUID) (domain.Installation, error) {
	in, err := scanInstallation(q.QueryRow(ctx, `SELECT `+installationColumns+` FROM external_app_installations i WHERE i.tenant_id = $1 AND i.id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return in, domain.ErrNotFound
	}
	return in, err
}

// List returns the installations, live ones first, newest first.
func (Store) List(ctx context.Context, q platformdb.Querier, tenantID uuid.UUID) ([]domain.Installation, error) {
	rows, err := q.Query(ctx, `SELECT `+installationColumns+` FROM external_app_installations i WHERE i.tenant_id = $1 ORDER BY (i.status = 'revoked'), i.created_at DESC, i.id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Installation{}
	for rows.Next() {
		in, err := scanInstallation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// Credentials lists the public records (never the digest: the application role has no privilege on that column).
func (Store) Credentials(ctx context.Context, q platformdb.Querier, tenantID, installationID uuid.UUID) ([]domain.Credential, error) {
	rows, err := q.Query(ctx, `
		SELECT id, installation_id, key_id, label, created_at, expires_at, revoked_at, last_used_at
		FROM external_app_credentials WHERE tenant_id = $1 AND installation_id = $2 ORDER BY created_at DESC, id`, tenantID, installationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Credential{}
	for rows.Next() {
		var c domain.Credential
		if err := rows.Scan(&c.ID, &c.InstallationID, &c.KeyID, &c.Label, &c.CreatedAt, &c.ExpiresAt, &c.RevokedAt, &c.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Update changes editable fields under the row lock. A revoked installation is final.
func (s Store) Update(ctx context.Context, q platformdb.Querier, tenantID, id uuid.UUID, in UpdateInput) (domain.Installation, error) {
	cur, err := s.lock(ctx, q, tenantID, id)
	if err != nil {
		return cur, err
	}
	if cur.Status == domain.StatusRevoked {
		return cur, domain.ErrInstallationRevoked
	}
	if in.Name != nil {
		if taken, err := nameTakenBy(ctx, q, tenantID, *in.Name, id); err != nil {
			return cur, err
		} else if taken {
			return cur, domain.ErrNameTaken
		}
		if _, err := q.Exec(ctx, `UPDATE external_app_installations SET name = $3, updated_at = now() WHERE tenant_id = $1 AND id = $2`, tenantID, id, *in.Name); err != nil {
			return cur, err
		}
	}
	if in.Scopes != nil {
		if _, err := q.Exec(ctx, `UPDATE external_app_installations SET scopes = $3, updated_at = now() WHERE tenant_id = $1 AND id = $2`, tenantID, id, domain.ScopeStrings(in.Scopes)); err != nil {
			return cur, err
		}
	}
	if in.RatePerMin != nil {
		if _, err := q.Exec(ctx, `UPDATE external_app_installations SET rate_limit_per_min = $3, updated_at = now() WHERE tenant_id = $1 AND id = $2`, tenantID, id, *in.RatePerMin); err != nil {
			return cur, err
		}
	}
	if in.LeaseSeconds != nil || in.ClearLease {
		if _, err := q.Exec(ctx, `UPDATE external_app_installations SET lease_seconds = $3, updated_at = now() WHERE tenant_id = $1 AND id = $2`, tenantID, id, in.LeaseSeconds); err != nil {
			return cur, err
		}
	}
	if in.AllowOutsideHours != nil {
		if _, err := q.Exec(ctx, `UPDATE external_app_installations SET allow_outside_hours = $3, updated_at = now() WHERE tenant_id = $1 AND id = $2`, tenantID, id, *in.AllowOutsideHours); err != nil {
			return cur, err
		}
	}
	if err := replaceAllowlists(ctx, q, tenantID, id, in.QueueIDs, in.ChannelIDs); err != nil {
		return cur, err
	}
	return s.Get(ctx, q, tenantID, id)
}

// Transition names the status changes allowed from the admin API.
type Transition string

const (
	Suspend    Transition = "suspend"
	Reactivate Transition = "reactivate"
	Revoke     Transition = "revoke"
)

var ErrBadTransition = errors.New("this status change is not allowed from the current status")

// Drainer releases what an installation holds when it stops being usable (H2 wires the conversation control; until then nothing is held).
// It runs inside the same transaction as the status change, after the row lock, so no write of the installation can interleave.
type Drainer func(ctx context.Context, q platformdb.Querier, tenantID, installationID uuid.UUID, reason string) (released int, err error)

// Apply changes the status under the row lock. suspend: active -> suspended; reactivate: suspended -> active; revoke: active|suspended ->
// revoked (final) and every credential of the installation is revoked with it. Suspend and revoke drain the conversations the application holds.
func (s Store) Apply(ctx context.Context, q platformdb.Querier, tenantID, id uuid.UUID, t Transition, drain Drainer) (domain.Installation, int, error) {
	cur, err := s.lock(ctx, q, tenantID, id)
	if err != nil {
		return cur, 0, err
	}
	var to domain.Status
	switch {
	case t == Suspend && cur.Status == domain.StatusActive:
		to = domain.StatusSuspended
	case t == Reactivate && cur.Status == domain.StatusSuspended:
		to = domain.StatusActive
	case t == Revoke && cur.Status != domain.StatusRevoked:
		to = domain.StatusRevoked
	default:
		return cur, 0, ErrBadTransition
	}
	released := 0
	if (to == domain.StatusSuspended || to == domain.StatusRevoked) && drain != nil {
		if released, err = drain(ctx, q, tenantID, id, string(t)); err != nil {
			return cur, 0, err
		}
	}
	switch to {
	case domain.StatusSuspended:
		_, err = q.Exec(ctx, `UPDATE external_app_installations SET status = 'suspended', suspended_at = now(), updated_at = now() WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	case domain.StatusActive:
		_, err = q.Exec(ctx, `UPDATE external_app_installations SET status = 'active', suspended_at = NULL, updated_at = now() WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	case domain.StatusRevoked:
		if _, err = q.Exec(ctx, `UPDATE external_app_credentials SET revoked_at = now() WHERE tenant_id = $1 AND installation_id = $2 AND revoked_at IS NULL`, tenantID, id); err == nil {
			_, err = q.Exec(ctx, `UPDATE external_app_installations SET status = 'revoked', revoked_at = now(), updated_at = now() WHERE tenant_id = $1 AND id = $2`, tenantID, id)
		}
	}
	if err != nil {
		return cur, 0, err
	}
	inst, err := s.Get(ctx, q, tenantID, id)
	return inst, released, err
}
