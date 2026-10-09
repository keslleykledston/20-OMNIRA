// Package access is the Hub's people-and-permissions control plane (ADR-0039): which instances (companies) exist, who
// administers each one, which agents work where, and how much each agent may do there.
//
// Only an active admin of the hub may use it, decided by the database for the AUTHENTICATED user (never by the client),
// asked again inside every writing transaction. Writes that touch the delegation itself (hub members, grants) go through
// provisioning.Service with provisioning.WithActor, so there is ONE definition of "grant", the actor is audited, and what
// is reserved to hubctl (creating/removing hub admins) stays out of reach of a button.
package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/provisioning"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

var (
	// ErrForbidden: not an active admin of this hub (the HTTP layer answers a uniform 404).
	ErrForbidden = errors.New("access: forbidden")
	// ErrInvalid: the request is well-formed but refused (422).
	ErrInvalid = errors.New("access: invalid request")
	// ErrNotFound: the instance/agent named in the request is not part of this hub (404).
	ErrNotFound = errors.New("access: not found")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Service is safe for concurrent use.
type Service struct {
	pool *pgxpool.Pool
	prov *provisioning.Service
}

func New(pool *pgxpool.Pool) (*Service, error) {
	prov, err := provisioning.New(pool, "access-panel")
	if err != nil {
		return nil, err
	}
	return &Service{pool: pool, prov: prov}, nil
}

func (s *Service) tx(ctx context.Context, actor, hub uuid.UUID, fn func(ctx context.Context, q platformdb.Querier) error) error {
	if actor == uuid.Nil || hub == uuid.Nil {
		return ErrForbidden
	}
	return platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(c context.Context) error {
		q := platformdb.QuerierFromContext(c, s.pool)
		ok, err := lockAuthority(c, q, hub, actor)
		if err != nil {
			return err
		}
		if !ok {
			return ErrForbidden
		}
		return fn(c, q)
	})
}

// ------------------------------------------------------------------ read model

type Person struct {
	UserID uuid.UUID `json:"user_id"`
	Email  string    `json:"email"`
	Name   string    `json:"name"`
}

type Instance struct {
	TenantID       uuid.UUID `json:"tenant_id"`
	Name           string    `json:"name"`
	TenantStatus   string    `json:"tenant_status"`
	ContractStatus string    `json:"contract_status"`
	Admins         []Person  `json:"admins"`
	DirectAgents   int       `json:"direct_agents"` // active members of the company who are not administrators
	HubAgents      int       `json:"hub_agents"`    // people with a live grant through this hub
}

type Grant struct {
	TenantID   uuid.UUID  `json:"tenant_id"`
	Mode       string     `json:"mode"` // read | reply (a revoked/suspended grant is not listed: it is "none")
	ValidUntil *time.Time `json:"valid_until,omitempty"`
}

type Agent struct {
	Person
	HubRole string  `json:"hub_role"` // hub_agent | hub_admin
	Grants  []Grant `json:"grants"`
	// DirectInstances: companies where the person is an active member in their own right (not through this hub).
	DirectInstances []uuid.UUID `json:"direct_instances"`
	// Instances is how many distinct companies the person works in (grants through this hub + direct memberships).
	Instances int `json:"instances"`
}

type Overview struct {
	HubID     uuid.UUID  `json:"hub_id"`
	HubName   string     `json:"hub_name"`
	Instances []Instance `json:"instances"`
	Agents    []Agent    `json:"agents"`
	// Invitations are the authorizations waiting for a person's first sign-in (ADR-0039 §3.10).
	Invitations []Invitation `json:"invitations"`
}

func (s *Service) Overview(ctx context.Context, actor, hub uuid.UUID) (Overview, error) {
	out := Overview{HubID: hub, Instances: []Instance{}, Agents: []Agent{}, Invitations: []Invitation{}}
	err := s.tx(ctx, actor, hub, func(c context.Context, q platformdb.Querier) error {
		if err := q.QueryRow(c, `SELECT name FROM service_hubs WHERE id = $1`, hub).Scan(&out.HubName); err != nil {
			return err
		}
		rows, err := q.Query(c, `
			SELECT t.id, COALESCE(NULLIF(t.trade_name, ''), t.legal_name), t.status, k.status,
			       (SELECT count(*) FROM memberships m JOIN roles r ON r.id = m.role_id
			         WHERE m.tenant_id = t.id AND m.status = 'active' AND r.key <> 'tenant_admin'),
			       (SELECT count(DISTINCT g.user_id) FROM effective_access_grants g
			         WHERE g.hub_id = k.hub_id AND g.tenant_id = t.id AND g.status = 'active' AND (g.valid_until IS NULL OR g.valid_until > now()))
			FROM hub_tenant_service_contracts k JOIN tenants t ON t.id = k.tenant_id
			WHERE k.hub_id = $1 ORDER BY lower(COALESCE(NULLIF(t.trade_name, ''), t.legal_name)), t.id`, hub)
		if err != nil {
			return err
		}
		idx := map[uuid.UUID]int{}
		for rows.Next() {
			var in Instance
			if err := rows.Scan(&in.TenantID, &in.Name, &in.TenantStatus, &in.ContractStatus, &in.DirectAgents, &in.HubAgents); err != nil {
				rows.Close()
				return err
			}
			in.Admins = []Person{}
			idx[in.TenantID] = len(out.Instances)
			out.Instances = append(out.Instances, in)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		admins, err := q.Query(c, `
			SELECT m.tenant_id, u.id, COALESCE(u.email, ''), COALESCE(u.display_name, '')
			FROM memberships m JOIN roles r ON r.id = m.role_id JOIN users u ON u.id = m.user_id
			WHERE r.key = 'tenant_admin' AND m.status = 'active' AND u.status = 'active'
			  AND m.tenant_id IN (SELECT tenant_id FROM hub_tenant_service_contracts WHERE hub_id = $1)
			ORDER BY lower(COALESCE(u.email, ''))`, hub)
		if err != nil {
			return err
		}
		for admins.Next() {
			var tenant uuid.UUID
			var p Person
			if err := admins.Scan(&tenant, &p.UserID, &p.Email, &p.Name); err != nil {
				admins.Close()
				return err
			}
			if i, ok := idx[tenant]; ok {
				out.Instances[i].Admins = append(out.Instances[i].Admins, p)
			}
		}
		admins.Close()
		if err := admins.Err(); err != nil {
			return err
		}

		people, err := q.Query(c, `
			SELECT u.id, COALESCE(u.email, ''), COALESCE(u.display_name, ''), r.key
			FROM hub_memberships hm JOIN roles r ON r.id = hm.role_id JOIN users u ON u.id = hm.user_id
			WHERE hm.hub_id = $1 ORDER BY r.key, lower(COALESCE(u.email, ''))`, hub)
		if err != nil {
			return err
		}
		aidx := map[uuid.UUID]int{}
		for people.Next() {
			var a Agent
			if err := people.Scan(&a.UserID, &a.Email, &a.Name, &a.HubRole); err != nil {
				people.Close()
				return err
			}
			a.Grants, a.DirectInstances = []Grant{}, []uuid.UUID{}
			aidx[a.UserID] = len(out.Agents)
			out.Agents = append(out.Agents, a)
		}
		people.Close()
		if err := people.Err(); err != nil {
			return err
		}
		grants, err := q.Query(c, `
			SELECT g.user_id, g.tenant_id, g.can_reply, g.valid_until
			FROM effective_access_grants g
			WHERE g.hub_id = $1 AND g.status = 'active' AND (g.valid_until IS NULL OR g.valid_until > now())`, hub)
		if err != nil {
			return err
		}
		for grants.Next() {
			var user uuid.UUID
			var g Grant
			var reply bool
			if err := grants.Scan(&user, &g.TenantID, &reply, &g.ValidUntil); err != nil {
				grants.Close()
				return err
			}
			g.Mode = "read"
			if reply {
				g.Mode = "reply"
			}
			if i, ok := aidx[user]; ok {
				out.Agents[i].Grants = append(out.Agents[i].Grants, g)
			}
		}
		grants.Close()
		if err := grants.Err(); err != nil {
			return err
		}
		direct, err := q.Query(c, `
			SELECT m.user_id, m.tenant_id FROM memberships m
			WHERE m.status = 'active' AND m.user_id IN (SELECT user_id FROM hub_memberships WHERE hub_id = $1)`, hub)
		if err != nil {
			return err
		}
		for direct.Next() {
			var user, tenant uuid.UUID
			if err := direct.Scan(&user, &tenant); err != nil {
				direct.Close()
				return err
			}
			if i, ok := aidx[user]; ok {
				out.Agents[i].DirectInstances = append(out.Agents[i].DirectInstances, tenant)
			}
		}
		direct.Close()
		if err := direct.Err(); err != nil {
			return err
		}
		for i := range out.Agents {
			seen := map[uuid.UUID]bool{}
			for _, g := range out.Agents[i].Grants {
				seen[g.TenantID] = true
			}
			for _, t := range out.Agents[i].DirectInstances {
				seen[t] = true
			}
			out.Agents[i].Instances = len(seen)
		}
		out.Invitations, err = s.pendingInvitations(c, q, hub)
		return err
	})
	return out, err
}

// ------------------------------------------------------------------ people

// userByEmail resolves an EXACT e-mail to one active account. Every way of not matching (unknown, ambiguous, inactive) is
// the same answer, so the screen cannot be used to probe which e-mails exist or in what state.
func userByEmail(ctx context.Context, q platformdb.Querier, email string) (uuid.UUID, error) {
	email = strings.TrimSpace(email)
	if email == "" || !strings.Contains(email, "@") {
		return uuid.Nil, invalid("a valid e-mail is required")
	}
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM users WHERE lower(email) = lower($1) AND status = 'active'`, email).Scan(&n); err != nil {
		return uuid.Nil, err
	}
	if n != 1 {
		return uuid.Nil, invalid("no eligible account for that e-mail (the person must already have an OMNIRA account)")
	}
	var id uuid.UUID
	if err := q.QueryRow(ctx, `SELECT id FROM users WHERE lower(email) = lower($1) AND status = 'active'`, email).Scan(&id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func mapProv(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, provisioning.ErrForbidden):
		return ErrForbidden
	case errors.Is(err, provisioning.ErrNotFound):
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case errors.Is(err, provisioning.ErrInvalid), errors.Is(err, provisioning.ErrConflict):
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return err
}

// AddAgent makes an existing account an agent of the hub. It grants access to NO company: that is SetAccess.
func (s *Service) AddAgent(ctx context.Context, actor, hub uuid.UUID, email string) (Person, error) {
	var user uuid.UUID
	if err := s.tx(ctx, actor, hub, func(c context.Context, q platformdb.Querier) error {
		var err error
		user, err = userByEmail(c, q, email)
		return err
	}); err != nil {
		return Person{}, err
	}
	if err := s.prov.AddMember(provisioning.WithActor(ctx, actor), hub, user, provisioning.RoleAgent); err != nil {
		return Person{}, mapProv(err)
	}
	var p Person
	err := s.tx(ctx, actor, hub, func(c context.Context, q platformdb.Querier) error {
		return q.QueryRow(c, `SELECT id, COALESCE(email, ''), COALESCE(display_name, '') FROM users WHERE id = $1`, user).Scan(&p.UserID, &p.Email, &p.Name)
	})
	return p, err
}

// RemoveAgent takes an agent out of the hub; their grants go with them. Hub admins cannot be removed here.
func (s *Service) RemoveAgent(ctx context.Context, actor, hub, user uuid.UUID) error {
	_, err := s.prov.RemoveMember(provisioning.WithActor(ctx, actor), hub, user)
	return mapProv(err)
}

// SetAccess is the matrix cell: none | read | reply for one agent on one instance of this hub. Setting it is an explicit
// act, so it may widen (it renews a revoked grant); the validity is exactly what was sent.
func (s *Service) SetAccess(ctx context.Context, actor, hub, user, tenant uuid.UUID, mode string, validUntil *time.Time) error {
	if mode != "none" && mode != "read" && mode != "reply" {
		return invalid("mode must be none, read or reply")
	}
	pctx := provisioning.WithActor(ctx, actor)
	if mode == "none" {
		err := s.prov.RevokeGrant(pctx, hub, tenant, user)
		if errors.Is(err, provisioning.ErrNotFound) {
			return nil // already none
		}
		return mapProv(err)
	}
	_, err := s.prov.Grant(pctx, provisioning.GrantSpec{Hub: hub, Tenant: tenant, User: user, ValidUntil: validUntil, CanReply: mode == "reply", Renew: true})
	return mapProv(err)
}

// ------------------------------------------------------------------ instance administrators

// AddInstanceAdmin makes an existing account an administrator of one instance of this hub.
func (s *Service) AddInstanceAdmin(ctx context.Context, actor, hub, tenant uuid.UUID, email string) (Person, error) {
	var p Person
	err := s.tx(ctx, actor, hub, func(c context.Context, q platformdb.Querier) error {
		if err := requireContract(c, q, hub, tenant); err != nil {
			return err
		}
		user, err := userByEmail(c, q, email)
		if err != nil {
			return err
		}
		var role uuid.UUID
		if err := q.QueryRow(c, `SELECT id FROM roles WHERE tenant_id IS NULL AND key = 'tenant_admin'`).Scan(&role); err != nil {
			return err
		}
		var prev *string
		if err := q.QueryRow(c, `SELECT (SELECT r.key || ':' || m.status FROM memberships m JOIN roles r ON r.id = m.role_id WHERE m.tenant_id = $1 AND m.user_id = $2)`, tenant, user).Scan(&prev); err != nil {
			return err
		}
		if prev != nil && *prev == "tenant_admin:active" {
			return s.loadPerson(c, q, user, &p) // idempotent
		}
		if _, err := q.Exec(c, `
			INSERT INTO memberships (tenant_id, user_id, role_id, status) VALUES ($1, $2, $3, 'active')
			ON CONFLICT (tenant_id, user_id) DO UPDATE SET role_id = EXCLUDED.role_id, status = 'active', updated_at = now()`, tenant, user, role); err != nil {
			return err
		}
		if err := s.audit(c, q, actor, tenant, "hub.instance.admin_added", user, map[string]any{"hub_id": hub, "previous": prev}); err != nil {
			return err
		}
		return s.loadPerson(c, q, user, &p)
	})
	return p, err
}

// RemoveInstanceAdmin withdraws administration of an instance. The last active administrator cannot be removed: an
// instance with no administrator could not be managed by anyone but the hub.
func (s *Service) RemoveInstanceAdmin(ctx context.Context, actor, hub, tenant, user uuid.UUID) error {
	return s.tx(ctx, actor, hub, func(c context.Context, q platformdb.Querier) error {
		if err := requireContract(c, q, hub, tenant); err != nil {
			return err
		}
		// Same key as PATCH /team: the panel and the company's own team screen cannot both pass "not the last one".
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended('omnira.tenant-admins:' || $1::text, 0))`, tenant); err != nil {
			return err
		}
		// lock the administrators of this instance so two removals cannot both pass the "not the last one" check
		rows, err := q.Query(c, `SELECT m.user_id FROM memberships m JOIN roles r ON r.id = m.role_id
		                         WHERE m.tenant_id = $1 AND r.key = 'tenant_admin' AND m.status = 'active' FOR UPDATE OF m`, tenant)
		if err != nil {
			return err
		}
		var admins []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			admins = append(admins, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		isAdmin := false
		for _, id := range admins {
			if id == user {
				isAdmin = true
			}
		}
		if !isAdmin {
			return fmt.Errorf("%w: that person is not an administrator of this instance", ErrNotFound)
		}
		if len(admins) < 2 {
			return invalid("an instance needs at least one administrator; add another one first")
		}
		var agent uuid.UUID
		if err := q.QueryRow(c, `SELECT id FROM roles WHERE tenant_id IS NULL AND key = 'tenant_agent'`).Scan(&agent); err != nil {
			return err
		}
		// demoted to agent, not expelled: removing someone from the company is the company's own decision
		if _, err := q.Exec(c, `UPDATE memberships SET role_id = $3, updated_at = now() WHERE tenant_id = $1 AND user_id = $2`, tenant, user, agent); err != nil {
			return err
		}
		return s.audit(c, q, actor, tenant, "hub.instance.admin_removed", user, map[string]any{"hub_id": hub})
	})
}

func requireContract(ctx context.Context, q platformdb.Querier, hub, tenant uuid.UUID) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hub_tenant_service_contracts k JOIN tenants t ON t.id = k.tenant_id
	                                          WHERE k.hub_id = $1 AND k.tenant_id = $2 AND k.status = 'active' AND t.status = 'active')`, hub, tenant).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: no active instance %s in this hub", ErrNotFound, tenant)
	}
	return nil
}

func (s *Service) loadPerson(ctx context.Context, q platformdb.Querier, user uuid.UUID, p *Person) error {
	err := q.QueryRow(ctx, `SELECT id, COALESCE(email, ''), COALESCE(display_name, '') FROM users WHERE id = $1`, user).Scan(&p.UserID, &p.Email, &p.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *Service) audit(ctx context.Context, q platformdb.Querier, actor, tenant uuid.UUID, action string, resource uuid.UUID, meta map[string]any) error {
	meta["via"] = "omnira-access-panel"
	raw, err := jsonMarshal(meta)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata)
	                      VALUES ($1, $2, $3, $4, 'membership', $5, 'success', $6, $7)`,
		uuid.New(), tenant, actor, action, resource.String(), uuid.NewString(), raw)
	return err
}
