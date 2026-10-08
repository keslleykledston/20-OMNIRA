// Package provisioning is the operator-facing way to create and change Service Hub relationships:
// hubs, hub members, tenant service contracts and per-agent grants.
//
// There is deliberately no HTTP API for this yet: OMNIRA has no notion of a human "platform administrator" (system
// admin is an internal session mode used by workers and identity provisioning). Until that model exists, an operator
// runs cmd omnira-hubctl on the server, which calls this service.
//
// Every operation is one database transaction in a system session and writes its audit event in the SAME transaction,
// so a change and its audit trail succeed or fail together. All identifiers are validated against persisted state:
// a grant never takes a contract id (it is derived from hub + tenant), queues must belong to the contract's own
// tenant, and an agent can only be granted if they are an active user and a member of the hub.
package provisioning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

var (
	ErrInvalid  = errors.New("invalid request")
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}
func notFound(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNotFound, fmt.Sprintf(format, a...))
}

const (
	RoleAgent = "hub_agent"
	RoleAdmin = "hub_admin"
)

type Service struct {
	pool     *pgxpool.Pool
	operator string
	now      func() time.Time
}

// New requires an operator name: it is stored in every audit event so a change is always attributable to a person.
func New(pool *pgxpool.Pool, operator string) (*Service, error) {
	operator = strings.TrimSpace(operator)
	if operator == "" || utf8.RuneCountInString(operator) > 100 {
		return nil, invalid("an operator name (1-100 characters) is required for the audit trail")
	}
	return &Service{pool: pool, operator: operator, now: time.Now}, nil
}

// tx runs fn in one system-session transaction.
func (s *Service) tx(ctx context.Context, fn func(ctx context.Context, q platformdb.Querier) error) error {
	return platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(c context.Context) error {
		return fn(c, platformdb.QuerierFromContext(c, s.pool))
	})
}

func (s *Service) audit(ctx context.Context, q platformdb.Querier, tenant *uuid.UUID, action, resourceType string, resourceID uuid.UUID, meta map[string]any) error {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["operator"] = s.operator
	meta["via"] = "omnira-hubctl"
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata)
	                      VALUES ($1, $2, NULL, $3, $4, $5, 'success', $6, $7)`,
		uuid.New(), tenant, action, resourceType, resourceID, uuid.NewString(), raw)
	return err
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// ---------------------------------------------------------------- hubs

func (s *Service) CreateHub(ctx context.Context, name, description string) (uuid.UUID, error) {
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	if n := utf8.RuneCountInString(name); n < 1 || n > 200 {
		return uuid.Nil, invalid("hub name must have 1-200 characters")
	}
	if utf8.RuneCountInString(description) > 1000 {
		return uuid.Nil, invalid("description must have at most 1000 characters")
	}
	id := uuid.New()
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if _, err := q.Exec(c, `INSERT INTO service_hubs (id, name, description) VALUES ($1, $2, $3)`, id, name, description); err != nil {
			return err
		}
		return s.audit(c, q, nil, "hub.created", "service_hub", id, map[string]any{"name": name})
	})
	return id, err
}

func (s *Service) SetHubStatus(ctx context.Context, hub uuid.UUID, status string) error {
	if status != "active" && status != "suspended" {
		return invalid("hub status must be active or suspended")
	}
	return s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		var from string
		if err := q.QueryRow(c, `SELECT status FROM service_hubs WHERE id = $1 FOR UPDATE`, hub).Scan(&from); err != nil {
			return mapNoRows(err, "hub %s", hub)
		}
		if from == status {
			return nil
		}
		if _, err := q.Exec(c, `UPDATE service_hubs SET status = $2, updated_at = now() WHERE id = $1`, hub, status); err != nil {
			return err
		}
		return s.audit(c, q, nil, "hub.status_changed", "service_hub", hub, map[string]any{"from": from, "to": status})
	})
}

func mapNoRows(err error, format string, a ...any) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(format, a...)
	}
	return err
}

// ---------------------------------------------------------------- members

// AddMember adds a user to the hub or changes their role. It never grants access to any tenant by itself.
func (s *Service) AddMember(ctx context.Context, hub, user uuid.UUID, role string) error {
	if role != RoleAgent && role != RoleAdmin {
		return invalid("role must be %s or %s", RoleAgent, RoleAdmin)
	}
	return s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := s.requireHub(c, q, hub); err != nil {
			return err
		}
		if err := requireActiveUser(c, q, user); err != nil {
			return err
		}
		var roleID uuid.UUID
		if err := q.QueryRow(c, `SELECT id FROM roles WHERE tenant_id IS NULL AND key = $1 LIMIT 1`, role).Scan(&roleID); err != nil {
			return mapNoRows(err, "system role %s", role)
		}
		var prev *string
		if err := q.QueryRow(c, `SELECT (SELECT r.key FROM hub_memberships hm JOIN roles r ON r.id = hm.role_id WHERE hm.hub_id = $1 AND hm.user_id = $2)`, hub, user).Scan(&prev); err != nil {
			return err
		}
		if prev != nil && *prev == role {
			return nil // idempotent: nothing changes, nothing to audit
		}
		if _, err := q.Exec(c, `INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)
		                        ON CONFLICT (hub_id, user_id) DO UPDATE SET role_id = EXCLUDED.role_id, updated_at = now()`, hub, user, roleID); err != nil {
			return err
		}
		action, meta := "hub.member.added", map[string]any{"user_id": user, "role": role}
		if prev != nil {
			action, meta["previous_role"] = "hub.member.role_changed", *prev
		}
		return s.audit(c, q, nil, action, "hub_membership", hub, meta)
	})
}

// RemoveMember removes the user from the hub. Their grants disappear with it (foreign key cascade); the count is audited.
func (s *Service) RemoveMember(ctx context.Context, hub, user uuid.UUID) (grantsRemoved int, err error) {
	err = s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := q.QueryRow(c, `SELECT count(*) FROM effective_access_grants WHERE hub_id = $1 AND user_id = $2`, hub, user).Scan(&grantsRemoved); err != nil {
			return err
		}
		tag, err := q.Exec(c, `DELETE FROM hub_memberships WHERE hub_id = $1 AND user_id = $2`, hub, user)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return notFound("user %s is not a member of hub %s", user, hub)
		}
		return s.audit(c, q, nil, "hub.member.removed", "hub_membership", hub, map[string]any{"user_id": user, "grants_removed": grantsRemoved})
	})
	if err != nil {
		grantsRemoved = 0
	}
	return grantsRemoved, err
}

// ---------------------------------------------------------------- contracts

type ContractSpec struct {
	Hub, Tenant uuid.UUID
	ValidUntil  *time.Time
	// QueueIDs, when non-nil, restricts the contract to those queues of the tenant (an allowlist). nil = every queue.
	QueueIDs []uuid.UUID
}

func (s *Service) CreateContract(ctx context.Context, spec ContractSpec) (uuid.UUID, error) {
	if spec.Hub == uuid.Nil || spec.Tenant == uuid.Nil {
		return uuid.Nil, invalid("hub and tenant are required")
	}
	if spec.ValidUntil != nil && !spec.ValidUntil.After(s.now()) {
		return uuid.Nil, invalid("valid_until must be in the future")
	}
	var queues []string
	if spec.QueueIDs != nil {
		if len(spec.QueueIDs) == 0 {
			return uuid.Nil, invalid("an empty queue list would expose no queue at all; omit the list to cover every queue")
		}
		seen := map[uuid.UUID]bool{}
		for _, id := range spec.QueueIDs {
			if id == uuid.Nil {
				return uuid.Nil, invalid("queue ids must not be nil")
			}
			if !seen[id] {
				seen[id] = true
				queues = append(queues, id.String())
			}
		}
	}
	id := uuid.New()
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := s.requireHub(c, q, spec.Hub); err != nil {
			return err
		}
		var tenantStatus string
		if err := q.QueryRow(c, `SELECT status FROM tenants WHERE id = $1`, spec.Tenant).Scan(&tenantStatus); err != nil {
			return mapNoRows(err, "tenant %s", spec.Tenant)
		}
		if tenantStatus != "active" {
			return invalid("tenant %s is %s", spec.Tenant, tenantStatus)
		}
		scope := map[string]any{}
		if queues != nil {
			var found int
			if err := q.QueryRow(c, `SELECT count(*) FROM queues WHERE tenant_id = $1 AND id = ANY($2::uuid[])`, spec.Tenant, queues).Scan(&found); err != nil {
				return err
			}
			if found != len(queues) {
				return invalid("every queue must exist and belong to tenant %s", spec.Tenant)
			}
			scope["queue_ids"] = queues
		}
		scopeJSON, err := json.Marshal(scope)
		if err != nil {
			return err
		}
		if _, err := q.Exec(c, `INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, valid_until, service_scope)
		                        VALUES ($1, $2, $3, $4, $5::jsonb)`, id, spec.Hub, spec.Tenant, spec.ValidUntil, string(scopeJSON)); err != nil {
			if isUnique(err) {
				return fmt.Errorf("%w: hub %s already has a contract with tenant %s (change its status instead)", ErrConflict, spec.Hub, spec.Tenant)
			}
			return err
		}
		meta := map[string]any{"hub_id": spec.Hub, "queue_ids": queues}
		if spec.ValidUntil != nil {
			meta["valid_until"] = spec.ValidUntil.UTC().Format(time.RFC3339)
		}
		return s.audit(c, q, &spec.Tenant, "hub.contract.created", "hub_contract", id, meta)
	})
	return id, err
}

func (s *Service) SetContractStatus(ctx context.Context, hub, tenant uuid.UUID, status string) error {
	if status != "active" && status != "suspended" && status != "revoked" {
		return invalid("contract status must be active, suspended or revoked")
	}
	return s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		var id uuid.UUID
		var from string
		var until *time.Time
		if err := q.QueryRow(c, `SELECT id, status, valid_until FROM hub_tenant_service_contracts WHERE hub_id = $1 AND tenant_id = $2 FOR UPDATE`, hub, tenant).Scan(&id, &from, &until); err != nil {
			return mapNoRows(err, "no contract between hub %s and tenant %s", hub, tenant)
		}
		if from == status {
			return nil
		}
		if status == "active" && until != nil && !until.After(s.now()) {
			return invalid("the contract expired on %s; it cannot be reactivated without a new validity", until.UTC().Format(time.RFC3339))
		}
		if _, err := q.Exec(c, `UPDATE hub_tenant_service_contracts SET status = $2, updated_at = now() WHERE id = $1`, id, status); err != nil {
			return err
		}
		return s.audit(c, q, &tenant, "hub.contract.status_changed", "hub_contract", id, map[string]any{"hub_id": hub, "from": from, "to": status})
	})
}

// ---------------------------------------------------------------- grants

type GrantSpec struct {
	Hub, Tenant, User uuid.UUID
	ValidUntil        *time.Time
}

// Grant lets a hub member act on a tenant. The contract is derived from (hub, tenant), never supplied.
// Granting again renews the same grant (reactivates it and updates its validity).
func (s *Service) Grant(ctx context.Context, spec GrantSpec) (uuid.UUID, error) {
	if spec.Hub == uuid.Nil || spec.Tenant == uuid.Nil || spec.User == uuid.Nil {
		return uuid.Nil, invalid("hub, tenant and user are required")
	}
	if spec.ValidUntil != nil && !spec.ValidUntil.After(s.now()) {
		return uuid.Nil, invalid("valid_until must be in the future")
	}
	var id uuid.UUID
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := s.requireHub(c, q, spec.Hub); err != nil {
			return err
		}
		if err := requireActiveUser(c, q, spec.User); err != nil {
			return err
		}
		var member bool
		if err := q.QueryRow(c, `SELECT EXISTS (SELECT 1 FROM hub_memberships WHERE hub_id = $1 AND user_id = $2)`, spec.Hub, spec.User).Scan(&member); err != nil {
			return err
		}
		if !member {
			return invalid("user %s is not a member of hub %s; add them first", spec.User, spec.Hub)
		}
		var contract uuid.UUID
		var status string
		var until *time.Time
		if err := q.QueryRow(c, `SELECT id, status, valid_until FROM hub_tenant_service_contracts WHERE hub_id = $1 AND tenant_id = $2`, spec.Hub, spec.Tenant).Scan(&contract, &status, &until); err != nil {
			return mapNoRows(err, "no contract between hub %s and tenant %s", spec.Hub, spec.Tenant)
		}
		if status != "active" || (until != nil && !until.After(s.now())) {
			return invalid("the contract between hub %s and tenant %s is not active", spec.Hub, spec.Tenant)
		}
		if err := q.QueryRow(c, `INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id, valid_until)
		                         VALUES ($1, $2, $3, $4, $5)
		                         ON CONFLICT (hub_id, user_id, tenant_id, service_contract_id) DO UPDATE SET
		                           status = 'active', valid_until = EXCLUDED.valid_until, grant_version = effective_access_grants.grant_version + 1, updated_at = now()
		                         RETURNING id`, spec.Hub, spec.User, spec.Tenant, contract, spec.ValidUntil).Scan(&id); err != nil {
			return err
		}
		meta := map[string]any{"hub_id": spec.Hub, "user_id": spec.User, "contract_id": contract}
		if spec.ValidUntil != nil {
			meta["valid_until"] = spec.ValidUntil.UTC().Format(time.RFC3339)
		}
		return s.audit(c, q, &spec.Tenant, "hub.grant.granted", "hub_grant", id, meta)
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// RevokeGrant revokes the user's grant on the tenant. Revoking an already revoked grant is a no-op.
func (s *Service) RevokeGrant(ctx context.Context, hub, tenant, user uuid.UUID) error {
	return s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		var id uuid.UUID
		var status string
		if err := q.QueryRow(c, `SELECT id, status FROM effective_access_grants WHERE hub_id = $1 AND tenant_id = $2 AND user_id = $3 FOR UPDATE`, hub, tenant, user).Scan(&id, &status); err != nil {
			return mapNoRows(err, "user %s has no grant on tenant %s in hub %s", user, tenant, hub)
		}
		if status == "revoked" {
			return nil
		}
		if _, err := q.Exec(c, `UPDATE effective_access_grants SET status = 'revoked', grant_version = grant_version + 1, updated_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		return s.audit(c, q, &tenant, "hub.grant.revoked", "hub_grant", id, map[string]any{"hub_id": hub, "user_id": user, "from": status})
	})
}

// ---------------------------------------------------------------- reading

type Summary struct {
	Hub       HubInfo
	Members   []MemberInfo
	Contracts []ContractInfo
	Grants    []GrantInfo
}
type HubInfo struct {
	ID           uuid.UUID
	Name, Status string
}
type MemberInfo struct {
	UserID      uuid.UUID
	Email, Role string
}
type ContractInfo struct {
	TenantID   uuid.UUID
	TenantName string
	Status     string
	ValidUntil *time.Time
	QueueIDs   []string
	Restricted bool
}
type GrantInfo struct {
	UserID, TenantID uuid.UUID
	Status           string
	ValidUntil       *time.Time
}

// Describe is read-only and is the operator's way to check what a hub can currently do.
func (s *Service) Describe(ctx context.Context, hub uuid.UUID) (Summary, error) {
	var out Summary
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := q.QueryRow(c, `SELECT id, name, status FROM service_hubs WHERE id = $1`, hub).Scan(&out.Hub.ID, &out.Hub.Name, &out.Hub.Status); err != nil {
			return mapNoRows(err, "hub %s", hub)
		}
		rows, err := q.Query(c, `SELECT hm.user_id, COALESCE(u.email, ''), r.key FROM hub_memberships hm JOIN users u ON u.id = hm.user_id JOIN roles r ON r.id = hm.role_id WHERE hm.hub_id = $1 ORDER BY r.key, u.email`, hub)
		if err != nil {
			return err
		}
		for rows.Next() {
			var m MemberInfo
			if err := rows.Scan(&m.UserID, &m.Email, &m.Role); err != nil {
				rows.Close()
				return err
			}
			out.Members = append(out.Members, m)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = q.Query(c, `SELECT c.tenant_id, COALESCE(NULLIF(t.trade_name, ''), t.legal_name), c.status, c.valid_until, c.service_scope
		                        FROM hub_tenant_service_contracts c JOIN tenants t ON t.id = c.tenant_id WHERE c.hub_id = $1 ORDER BY 2`, hub)
		if err != nil {
			return err
		}
		for rows.Next() {
			var ci ContractInfo
			var scope map[string]any
			if err := rows.Scan(&ci.TenantID, &ci.TenantName, &ci.Status, &ci.ValidUntil, &scope); err != nil {
				rows.Close()
				return err
			}
			if list, ok := scope["queue_ids"].([]any); ok {
				ci.Restricted = true
				for _, v := range list {
					if sv, ok := v.(string); ok {
						ci.QueueIDs = append(ci.QueueIDs, sv)
					}
				}
			}
			out.Contracts = append(out.Contracts, ci)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = q.Query(c, `SELECT user_id, tenant_id, status, valid_until FROM effective_access_grants WHERE hub_id = $1 ORDER BY tenant_id, user_id`, hub)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g GrantInfo
			if err := rows.Scan(&g.UserID, &g.TenantID, &g.Status, &g.ValidUntil); err != nil {
				return err
			}
			out.Grants = append(out.Grants, g)
		}
		return rows.Err()
	})
	return out, err
}

// FindUserByEmail resolves an operator-supplied e-mail to exactly one user (case-insensitive). Zero or several matches are errors:
// provisioning never guesses who a grant is for.
func (s *Service) FindUserByEmail(ctx context.Context, email string) (uuid.UUID, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return uuid.Nil, invalid("e-mail is required")
	}
	var ids []uuid.UUID
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		rows, err := q.Query(c, `SELECT id FROM users WHERE lower(email) = lower($1) LIMIT 2`, email)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return uuid.Nil, err
	}
	switch len(ids) {
	case 0:
		return uuid.Nil, notFound("no user with e-mail %s", email)
	case 1:
		return ids[0], nil
	default:
		return uuid.Nil, invalid("e-mail %s matches more than one user; use --user with the id", email)
	}
}

// ---------------------------------------------------------------- helpers

func (s *Service) requireHub(ctx context.Context, q platformdb.Querier, hub uuid.UUID) error {
	var status string
	if err := q.QueryRow(ctx, `SELECT status FROM service_hubs WHERE id = $1`, hub).Scan(&status); err != nil {
		return mapNoRows(err, "hub %s", hub)
	}
	if status != "active" {
		return invalid("hub %s is %s", hub, status)
	}
	return nil
}

func requireActiveUser(ctx context.Context, q platformdb.Querier, user uuid.UUID) error {
	var status string
	if err := q.QueryRow(ctx, `SELECT status FROM users WHERE id = $1`, user).Scan(&status); err != nil {
		return mapNoRows(err, "user %s", user)
	}
	if status != "active" {
		return invalid("user %s is %s", user, status)
	}
	return nil
}
