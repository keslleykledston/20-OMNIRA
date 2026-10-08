// Package companies is the control plane's company management (ADR-0038 phase 1): a platform operator creates companies
// (tenants) inside a Hub, switches them on and off, and issues or withdraws their capabilities.
//
// Authority: the operator's identity comes from the authenticated session, never from a request field. Every call is
// re-authorized INSIDE its own system transaction: the user is an active platform operator AND a hub_admin of an active
// hub, and the company they touch already has a contract with that hub. The HTTP layer additionally asks the same
// question in the caller's own RLS session first (defence in depth); a denial there never reaches this package.
//
// Every change and its audit event are one transaction.
package companies

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/entitlements"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

var (
	// ErrForbidden: not an active platform operator, not an admin of this (active) hub, or the company is not the hub's.
	// Callers must answer it exactly like "not found".
	ErrForbidden = errors.New("companies: not allowed")
	ErrInvalid   = errors.New("companies: invalid request")
	// ErrKeyMismatch: the Idempotency-Key was already used for a different request.
	ErrKeyMismatch = errors.New("companies: idempotency key reused with a different request")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

type Company struct {
	ID               uuid.UUID       `json:"id"`
	LegalName        string          `json:"legal_name"`
	TradeName        string          `json:"trade_name"`
	DisplayName      string          `json:"display_name"`
	Status           string          `json:"status"`
	ContractStatus   string          `json:"contract_status"`
	Capabilities     map[string]bool `json:"capabilities"`
	Channels         int             `json:"channels"`
	Integrations     int             `json:"integrations"`
	OpenConversation int             `json:"open_conversations"`
	Agents           int             `json:"agents"`
	CreatedAt        time.Time       `json:"created_at"`
}

type CreateInput struct {
	LegalName         string
	TradeName         string
	TaxID             string
	InitialAdminEmail string
}

type UpdateInput struct {
	Status       string          // "" = unchanged; active | suspended
	Capabilities map[string]bool // nil/empty = unchanged
}

type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func (s *Service) tx(ctx context.Context, fn func(c context.Context, q platformdb.Querier) error) error {
	return platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(c context.Context) error {
		return fn(c, platformdb.QuerierFromContext(c, s.pool))
	})
}

// authorize is the in-transaction gate. The user must be an active platform operator and a hub_admin of an ACTIVE hub.
func authorize(ctx context.Context, q platformdb.Querier, operator, hub uuid.UUID) error {
	if operator == uuid.Nil || hub == uuid.Nil {
		return ErrForbidden
	}
	var ok bool
	err := q.QueryRow(ctx, `
		SELECT is_platform_operator($1) AND is_hub_admin($2, $1)
		       AND EXISTS (SELECT 1 FROM service_hubs WHERE id = $2 AND status = 'active')`, operator, hub).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

func requireContract(ctx context.Context, q platformdb.Querier, hub, tenant uuid.UUID) (contractStatus string, err error) {
	err = q.QueryRow(ctx, `SELECT status FROM hub_tenant_service_contracts WHERE hub_id = $1 AND tenant_id = $2`, hub, tenant).Scan(&contractStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrForbidden
	}
	return contractStatus, err
}

func audit(ctx context.Context, q platformdb.Querier, tenant, operator uuid.UUID, action string, meta map[string]any) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata)
	                      VALUES ($1, $2, $3, $4, 'tenant', $5, 'success', $6, $7)`,
		uuid.New(), tenant, operator, action, tenant.String(), uuid.NewString(), raw)
	return err
}

const companyColumns = `
	t.id, t.legal_name, COALESCE(t.trade_name, ''), COALESCE(NULLIF(t.trade_name, ''), t.legal_name), t.status, c.status, t.created_at,
	(SELECT count(*) FROM channel_connections cc WHERE cc.tenant_id = t.id AND cc.channel = 'whatsapp'),
	(SELECT count(*) FROM channel_connections cc WHERE cc.tenant_id = t.id AND cc.channel = 'erp'),
	(SELECT count(*) FROM conversations cv WHERE cv.tenant_id = t.id AND cv.status <> 'closed'),
	(SELECT count(*) FROM effective_access_grants g WHERE g.tenant_id = t.id AND g.hub_id = c.hub_id AND g.status = 'active')`

func scanCompany(row pgx.Row) (Company, error) {
	var co Company
	err := row.Scan(&co.ID, &co.LegalName, &co.TradeName, &co.DisplayName, &co.Status, &co.ContractStatus, &co.CreatedAt,
		&co.Channels, &co.Integrations, &co.OpenConversation, &co.Agents)
	return co, err
}

// withCapabilities fills the EFFECTIVE value of every known switch (default on).
func withCapabilities(ctx context.Context, q platformdb.Querier, cos []Company) error {
	if len(cos) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(cos))
	idx := map[uuid.UUID]int{}
	for i := range cos {
		ids[i] = cos[i].ID
		idx[cos[i].ID] = i
		cos[i].Capabilities = map[string]bool{}
		for _, c := range entitlements.Registry {
			cos[i].Capabilities[c.Key] = true
		}
	}
	rows, err := q.Query(ctx, `SELECT tenant_id, capability, enabled FROM tenant_entitlements WHERE tenant_id = ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t uuid.UUID
		var key string
		var on bool
		if err := rows.Scan(&t, &key, &on); err != nil {
			return err
		}
		if entitlements.Known(key) {
			cos[idx[t]].Capabilities[key] = on
		}
	}
	return rows.Err()
}

// List returns the hub's companies (every contract status, so a suspended company can be found and re-activated).
func (s *Service) List(ctx context.Context, operator, hub uuid.UUID) ([]Company, error) {
	var out []Company
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator, hub); err != nil {
			return err
		}
		rows, err := q.Query(c, `SELECT `+companyColumns+`
			FROM hub_tenant_service_contracts c JOIN tenants t ON t.id = c.tenant_id
			WHERE c.hub_id = $1 ORDER BY lower(COALESCE(NULLIF(t.trade_name, ''), t.legal_name)), t.id`, hub)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			co, err := scanCompany(rows)
			if err != nil {
				return err
			}
			out = append(out, co)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		return withCapabilities(c, q, out)
	})
	if out == nil {
		out = []Company{}
	}
	return out, err
}

func (s *Service) get(ctx context.Context, q platformdb.Querier, hub, tenant uuid.UUID) (Company, error) {
	co, err := scanCompany(q.QueryRow(ctx, `SELECT `+companyColumns+`
		FROM hub_tenant_service_contracts c JOIN tenants t ON t.id = c.tenant_id WHERE c.hub_id = $1 AND c.tenant_id = $2`, hub, tenant))
	if errors.Is(err, pgx.ErrNoRows) {
		return Company{}, ErrForbidden
	}
	if err != nil {
		return Company{}, err
	}
	list := []Company{co}
	if err := withCapabilities(ctx, q, list); err != nil {
		return Company{}, err
	}
	return list[0], nil
}

func clean(in CreateInput) (CreateInput, error) {
	in.LegalName, in.TradeName = strings.TrimSpace(in.LegalName), strings.TrimSpace(in.TradeName)
	in.TaxID, in.InitialAdminEmail = strings.TrimSpace(in.TaxID), strings.TrimSpace(in.InitialAdminEmail)
	if n := utf8.RuneCountInString(in.LegalName); n < 2 || n > 200 {
		return in, invalid("legal_name must have 2-200 characters")
	}
	if utf8.RuneCountInString(in.TradeName) > 200 {
		return in, invalid("trade_name must have at most 200 characters")
	}
	if utf8.RuneCountInString(in.TaxID) > 32 {
		return in, invalid("tax_id must have at most 32 characters")
	}
	if in.InitialAdminEmail != "" && (len(in.InitialAdminEmail) > 254 || !strings.Contains(in.InitialAdminEmail, "@")) {
		return in, invalid("initial_admin_email is not an e-mail address")
	}
	return in, nil
}

func requestHash(hub uuid.UUID, in CreateInput) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{hub.String(), in.LegalName, in.TradeName, in.TaxID, strings.ToLower(in.InitialAdminEmail)}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// Create makes a company inside the hub: tenant, default queue, a contract with this hub (no grants: access is
// issued separately), and optionally its first administrator (an EXISTING active user, by exact e-mail).
// replayed is true when the Idempotency-Key had already created this company.
func (s *Service) Create(ctx context.Context, operator, hub uuid.UUID, in CreateInput, key string) (co Company, replayed bool, err error) {
	if len(key) < 8 || len(key) > 128 {
		return Company{}, false, invalid("Idempotency-Key must have 8-128 characters")
	}
	in, err = clean(in)
	if err != nil {
		return Company{}, false, err
	}
	hash := requestHash(hub, in)
	err = s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator, hub); err != nil {
			return err
		}
		// Serialize retries of the same key, then replay or refuse.
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, operator.String()+"/"+key); err != nil {
			return err
		}
		var prevHash string
		var prevTenant uuid.UUID
		perr := q.QueryRow(c, `SELECT request_hash, created_tenant_id FROM company_creation_requests WHERE operator_id = $1 AND idempotency_key = $2`, operator, key).Scan(&prevHash, &prevTenant)
		switch {
		case perr == nil:
			if prevHash != hash {
				return ErrKeyMismatch
			}
			replayed = true
			co, perr = s.get(c, q, hub, prevTenant)
			return perr
		case !errors.Is(perr, pgx.ErrNoRows):
			return perr
		}
		var adminID *uuid.UUID
		if in.InitialAdminEmail != "" {
			var id uuid.UUID
			var status string
			var n int
			if err := q.QueryRow(c, `SELECT count(*) FROM users WHERE lower(email) = lower($1)`, in.InitialAdminEmail).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return invalid("initial_admin_email must match exactly one existing user (invite new people from inside the company afterwards)")
			}
			if err := q.QueryRow(c, `SELECT id, status FROM users WHERE lower(email) = lower($1)`, in.InitialAdminEmail).Scan(&id, &status); err != nil {
				return err
			}
			if status != "active" {
				return invalid("the initial administrator's account is %s", status)
			}
			adminID = &id
		}
		tenant := uuid.New()
		if _, err := q.Exec(c, `INSERT INTO tenants (id, legal_name, trade_name, tax_id, status) VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), 'active')`,
			tenant, in.LegalName, in.TradeName, in.TaxID); err != nil {
			return err
		}
		if _, err := q.Exec(c, `INSERT INTO queues (tenant_id, name, mode, is_default) VALUES ($1, 'Default', 'manual', true)`, tenant); err != nil {
			return err
		}
		if _, err := q.Exec(c, `INSERT INTO hub_tenant_service_contracts (hub_id, tenant_id) VALUES ($1, $2)`, hub, tenant); err != nil {
			return err
		}
		if adminID != nil {
			if _, err := q.Exec(c, `INSERT INTO memberships (tenant_id, user_id, role_id, status)
			                        SELECT $1, $2, id, 'active' FROM roles WHERE key = 'tenant_admin' AND tenant_id IS NULL`, tenant, *adminID); err != nil {
				return err
			}
		}
		if _, err := q.Exec(c, `INSERT INTO company_creation_requests (operator_id, idempotency_key, request_hash, created_tenant_id) VALUES ($1, $2, $3, $4)`,
			operator, key, hash, tenant); err != nil {
			return err
		}
		meta := map[string]any{"hub_id": hub, "legal_name": in.LegalName, "via": "hub-admin-api"}
		if adminID != nil {
			meta["initial_admin_user_id"] = *adminID
		}
		if err := audit(c, q, tenant, operator, "platform.company.created", meta); err != nil {
			return err
		}
		co, err = s.get(c, q, hub, tenant)
		return err
	})
	if err != nil {
		return Company{}, false, err
	}
	return co, replayed, nil
}

// Update changes the company's status and/or its capability switches in one transaction.
func (s *Service) Update(ctx context.Context, operator, hub, tenant uuid.UUID, in UpdateInput) (Company, error) {
	if in.Status != "" && in.Status != "active" && in.Status != "suspended" {
		return Company{}, invalid("status must be active or suspended")
	}
	if in.Status == "" && len(in.Capabilities) == 0 {
		return Company{}, invalid("nothing to change")
	}
	for k := range in.Capabilities {
		if !entitlements.Known(k) {
			return Company{}, invalid("unknown capability %q", k)
		}
	}
	var out Company
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator, hub); err != nil {
			return err
		}
		if _, err := requireContract(c, q, hub, tenant); err != nil {
			return err
		}
		var from string
		if err := q.QueryRow(c, `SELECT status FROM tenants WHERE id = $1 FOR UPDATE`, tenant).Scan(&from); err != nil {
			return err
		}
		if in.Status != "" && in.Status != from {
			if from != "active" && from != "suspended" {
				return invalid("a company that is %s cannot be switched from here", from)
			}
			if _, err := q.Exec(c, `UPDATE tenants SET status = $2, updated_at = now() WHERE id = $1`, tenant, in.Status); err != nil {
				return err
			}
			if err := audit(c, q, tenant, operator, "platform.company.status_changed", map[string]any{"hub_id": hub, "from": from, "to": in.Status}); err != nil {
				return err
			}
		}
		for _, cap := range entitlements.Registry { // deterministic order
			want, ok := in.Capabilities[cap.Key]
			if !ok {
				continue
			}
			var prev *bool
			if err := q.QueryRow(c, `SELECT (SELECT enabled FROM tenant_entitlements WHERE tenant_id = $1 AND capability = $2 FOR UPDATE)`, tenant, cap.Key).Scan(&prev); err != nil {
				return err
			}
			effective := prev == nil || *prev
			if _, err := q.Exec(c, `INSERT INTO tenant_entitlements (tenant_id, capability, enabled, updated_by) VALUES ($1, $2, $3, $4)
			                        ON CONFLICT (tenant_id, capability) DO UPDATE SET enabled = EXCLUDED.enabled, updated_by = EXCLUDED.updated_by, updated_at = now()`,
				tenant, cap.Key, want, operator); err != nil {
				return err
			}
			if effective != want {
				if err := audit(c, q, tenant, operator, "platform.company.capability_changed", map[string]any{"hub_id": hub, "capability": cap.Key, "from": effective, "to": want}); err != nil {
					return err
				}
			}
		}
		var err error
		out, err = s.get(c, q, hub, tenant)
		return err
	})
	return out, err
}
