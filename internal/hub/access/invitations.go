package access

// Authorizing a person BY E-MAIL (ADR-0039 §3.10), before or after they have an account, in one gesture:
//
//   - the e-mail already belongs to one active account: the person becomes an agent of the hub right now, with exactly the
//     access the administrator picked;
//   - there is no such account: the choice is kept (hub_preauthorizations, 14 days, revocable) and applied the moment
//     someone signs in with that address VERIFIED by the identity provider (ApplyPreauthorizations, called after sign-in).
//
// There is no link and no token: nothing to forward, leak or intercept. What is trusted is the same thing the tenant
// invitations already trust, the identity provider's verified e-mail, plus the authority of the administrator who wrote
// the choice, which is checked AGAIN when it is applied (they must still be an active admin of an active hub).
//
// The administrator can tell whether an account exists (applied vs pending). That is accepted: they are the person who
// provisions the people of this hub, and the listing shows them either way.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnira/omnira/internal/hub/provisioning"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// InvitationTTL is how long a choice waits for the person's first sign-in.
const InvitationTTL = 14 * 24 * time.Hour

const maxInviteAccess = 50

// InviteAccess is one company and how much the person may do there.
type InviteAccess struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Mode     string    `json:"mode"` // read | reply
}

// Invitation is a pending choice, as the panel lists it.
type Invitation struct {
	ID        uuid.UUID      `json:"id"`
	Email     string         `json:"email"`
	Access    []InviteAccess `json:"access"`
	CreatedAt time.Time      `json:"created_at"`
	ExpiresAt time.Time      `json:"expires_at"`
}

// InviteResult: "applied" (the account existed) or "pending" (kept until the first sign-in).
type InviteResult struct {
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func normalizeEmail(email string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(email))
	at := strings.Index(e, "@")
	if len(e) < 3 || len(e) > 254 || at < 1 || at != strings.LastIndex(e, "@") || at == len(e)-1 || strings.ContainsAny(e, " \t\r\n,;<>\"") {
		return "", invalid("a valid e-mail is required")
	}
	return e, nil
}

func checkInviteAccess(access []InviteAccess) error {
	if len(access) > maxInviteAccess {
		return invalid("at most %d companies per authorization", maxInviteAccess)
	}
	seen := map[uuid.UUID]bool{}
	for _, a := range access {
		switch {
		case a.TenantID == uuid.Nil:
			return invalid("every access needs a company")
		case a.Mode != "read" && a.Mode != "reply":
			return invalid("mode must be read or reply")
		case seen[a.TenantID]:
			return invalid("a company can be listed once")
		}
		seen[a.TenantID] = true
	}
	return nil
}

// activeUserByEmail finds the one active account with this exact address. Zero or several both read as "not found".
func activeUserByEmail(ctx context.Context, q platformdb.Querier, email string) (uuid.UUID, bool, error) {
	rows, err := q.Query(ctx, `SELECT id FROM users WHERE lower(email) = $1 AND status = 'active' LIMIT 2`, email)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return uuid.Nil, false, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, false, err
	}
	if len(ids) != 1 {
		return uuid.Nil, false, nil
	}
	return ids[0], true, nil
}

// Invite authorizes the e-mail. See the file comment.
func (s *Service) Invite(ctx context.Context, actor, hub uuid.UUID, email string, access []InviteAccess) (InviteResult, error) {
	e, err := normalizeEmail(email)
	if err != nil {
		return InviteResult{}, err
	}
	if err := checkInviteAccess(access); err != nil {
		return InviteResult{}, err
	}
	var user uuid.UUID
	var exists bool
	var res InviteResult
	err = s.tx(ctx, actor, hub, func(c context.Context, q platformdb.Querier) error {
		for _, a := range access {
			if err := requireContract(c, q, hub, a.TenantID); err != nil {
				return err
			}
		}
		var err error
		if user, exists, err = activeUserByEmail(c, q, e); err != nil || exists {
			return err
		}
		// no account yet: keep the choice. Asking again for the same address replaces the previous choice.
		if _, err := q.Exec(c, `UPDATE hub_preauthorizations SET status = 'revoked', updated_at = now()
		                        WHERE hub_id = $1 AND email = $2 AND status = 'pending'`, hub, e); err != nil {
			return err
		}
		raw, err := json.Marshal(append([]InviteAccess{}, access...))
		if err != nil {
			return err
		}
		expires := time.Now().UTC().Add(InvitationTTL)
		var id uuid.UUID
		if err := q.QueryRow(c, `INSERT INTO hub_preauthorizations (hub_id, email, grants, created_by, expires_at)
		                         VALUES ($1, $2, $3, $4, $5) RETURNING id`, hub, e, raw, actor, expires).Scan(&id); err != nil {
			return err
		}
		res = InviteResult{Status: "pending", ExpiresAt: &expires}
		return s.auditHub(c, q, &actor, "hub.preauthorization.created", id, map[string]any{"hub_id": hub, "companies": len(access)})
	})
	if err != nil {
		return InviteResult{}, err
	}
	if !exists {
		return res, nil
	}
	// the account exists: the same writes the panel's other buttons make, by the same service, as this administrator
	if _, err := s.applyTo(ctx, actor, hub, user, access, false); err != nil {
		return InviteResult{}, err
	}
	return InviteResult{Status: "applied"}, nil
}

// applyTo makes the person an agent of the hub (unless they already administer it: that role is hubctl's to change) and
// sets each access. tolerant: a company that stopped being offered meanwhile is skipped and counted instead of failing the
// rest (used when applying a choice that waited days); otherwise the first refusal is returned.
func (s *Service) applyTo(ctx context.Context, creator, hub, user uuid.UUID, access []InviteAccess, tolerant bool) (skipped int, err error) {
	pctx := provisioning.WithActor(ctx, creator)
	var role *string
	if err := platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(c context.Context) error {
		var k string
		switch err := platformdb.QuerierFromContext(c, s.pool).QueryRow(c, `SELECT r.key FROM hub_memberships hm JOIN roles r ON r.id = hm.role_id
		                                                                   WHERE hm.hub_id = $1 AND hm.user_id = $2`, hub, user).Scan(&k); {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return err
		}
		role = &k
		return nil
	}); err != nil {
		return 0, err
	}
	if role == nil || *role != provisioning.RoleAdmin {
		if err := s.prov.AddMember(pctx, hub, user, provisioning.RoleAgent); err != nil {
			return 0, mapProv(err)
		}
	}
	for _, a := range access {
		// the company must be offered NOW: provisioning itself would still grant on a suspended company (the read policy would
		// hide it, but the row would be there)
		if err := platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(c context.Context) error {
			return requireContract(c, platformdb.QuerierFromContext(c, s.pool), hub, a.TenantID)
		}); err != nil {
			if tolerant && errors.Is(err, ErrNotFound) {
				skipped++
				continue
			}
			return skipped, err
		}
		_, err := s.prov.Grant(pctx, provisioning.GrantSpec{Hub: hub, Tenant: a.TenantID, User: user, CanReply: a.Mode == "reply", Renew: true})
		if err == nil {
			continue
		}
		if tolerant && (errors.Is(err, provisioning.ErrNotFound) || errors.Is(err, provisioning.ErrInvalid) || errors.Is(err, provisioning.ErrConflict)) {
			skipped++
			continue
		}
		return skipped, mapProv(err)
	}
	return skipped, nil
}

// ApplyPreauthorizations is called after someone signs in. It applies every unexpired choice waiting for the address of
// this account, but only when the identity provider has VERIFIED that address, and only while the administrator who wrote
// the choice is still an active admin of an active hub (otherwise the choice is voided). Safe to call on every sign-in and
// concurrently: the pending rows are locked, so a second caller waits and then finds nothing left.
func (s *Service) ApplyPreauthorizations(ctx context.Context, user uuid.UUID) (applied int, err error) {
	if user == uuid.Nil {
		return 0, nil
	}
	err = platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(c context.Context) error {
		q := platformdb.QuerierFromContext(c, s.pool)
		var email string
		switch err := q.QueryRow(c, `
			SELECT lower(u.email) FROM users u
			WHERE u.id = $1 AND u.status = 'active' AND u.email IS NOT NULL AND u.email <> ''
			  AND EXISTS (SELECT 1 FROM user_identities i WHERE i.user_id = u.id AND lower(i.email) = lower(u.email) AND i.email_verified)`, user).Scan(&email); {
		case errors.Is(err, pgx.ErrNoRows):
			return nil // unknown, inactive or not verified: nothing to apply
		case err != nil:
			return err
		}
		rows, err := q.Query(c, `SELECT id, hub_id, grants, created_by FROM hub_preauthorizations
		                         WHERE email = $1 AND status = 'pending' AND expires_at > now() ORDER BY created_at FOR UPDATE`, email)
		if err != nil {
			return err
		}
		type pending struct {
			id, hub, creator uuid.UUID
			access           []InviteAccess
		}
		var list []pending
		for rows.Next() {
			var p pending
			var raw []byte
			if err := rows.Scan(&p.id, &p.hub, &raw, &p.creator); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(raw, &p.access); err != nil {
				rows.Close()
				return fmt.Errorf("access: stored authorization %s is unreadable: %w", p.id, err)
			}
			list = append(list, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, p := range list {
			var live bool
			if err := q.QueryRow(c, `SELECT is_hub_admin($1, $2) AND EXISTS (SELECT 1 FROM service_hubs WHERE id = $1 AND status = 'active')
			                                AND EXISTS (SELECT 1 FROM users WHERE id = $2 AND status = 'active')`, p.hub, p.creator).Scan(&live); err != nil {
				return err
			}
			if !live {
				// the authority behind this choice is gone: it never takes effect
				if _, err := q.Exec(c, `UPDATE hub_preauthorizations SET status = 'void', updated_at = now() WHERE id = $1`, p.id); err != nil {
					return err
				}
				if err := s.auditHub(c, q, &user, "hub.preauthorization.voided", p.id, map[string]any{"hub_id": p.hub, "created_by": p.creator}); err != nil {
					return err
				}
				continue
			}
			skipped, err := s.applyTo(ctx, p.creator, p.hub, user, p.access, true)
			if err != nil {
				return err
			}
			if _, err := q.Exec(c, `UPDATE hub_preauthorizations SET status = 'applied', applied_user_id = $2, applied_at = now(), updated_at = now() WHERE id = $1`, p.id, user); err != nil {
				return err
			}
			if err := s.auditHub(c, q, &user, "hub.preauthorization.applied", p.id,
				map[string]any{"hub_id": p.hub, "created_by": p.creator, "companies": len(p.access), "skipped": skipped}); err != nil {
				return err
			}
			applied++
		}
		return nil
	})
	return applied, err
}

// RevokeInvitation cancels a choice that has not been applied yet.
func (s *Service) RevokeInvitation(ctx context.Context, actor, hub, id uuid.UUID) error {
	return s.tx(ctx, actor, hub, func(c context.Context, q platformdb.Querier) error {
		tag, err := q.Exec(c, `UPDATE hub_preauthorizations SET status = 'revoked', updated_at = now()
		                       WHERE id = $1 AND hub_id = $2 AND status = 'pending'`, id, hub)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: no pending authorization %s in this hub", ErrNotFound, id)
		}
		return s.auditHub(c, q, &actor, "hub.preauthorization.revoked", id, map[string]any{"hub_id": hub})
	})
}

func (s *Service) pendingInvitations(ctx context.Context, q platformdb.Querier, hub uuid.UUID) ([]Invitation, error) {
	rows, err := q.Query(ctx, `SELECT id, email, grants, created_at, expires_at FROM hub_preauthorizations
	                           WHERE hub_id = $1 AND status = 'pending' AND expires_at > now() ORDER BY created_at DESC, id`, hub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var inv Invitation
		var raw []byte
		if err := rows.Scan(&inv.ID, &inv.Email, &raw, &inv.CreatedAt, &inv.ExpiresAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &inv.Access); err != nil {
			return nil, err
		}
		if inv.Access == nil {
			inv.Access = []InviteAccess{}
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *Service) auditHub(ctx context.Context, q platformdb.Querier, actor *uuid.UUID, action string, resource uuid.UUID, meta map[string]any) error {
	meta["via"] = "omnira-access-panel"
	raw, err := jsonMarshal(meta)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata)
	                      VALUES ($1, NULL, $2, $3, 'hub_preauthorization', $4, 'success', $5, $6)`,
		uuid.New(), actor, action, resource.String(), uuid.NewString(), raw)
	return err
}
