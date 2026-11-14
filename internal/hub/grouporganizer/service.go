// Package grouporganizer is the Hub's metadata catalog and contextual links for WhatsApp groups
// (ADR-0059, Gate 2). It never reads wa_group_messages and never touches wa_groups.enabled: a catalog
// entry is self-declared metadata (provider group id, a display name) by someone already authorized to
// manage groups on the ORIGIN tenant, and a link only says "this group also concerns tenant X, in role
// Y" — it grants nothing by itself.
//
// Authorization reuses has_hub_manage_access(tenant, user, 'groups', hub) (ADR-0038/migration 000103),
// the same function the row-level policies of migration 000183 use. Unlike the control-plane services of
// this module (companies, distribution, access), this package does NOT open its session as a system
// admin: has_hub_manage_access requires p_user_id = current_user_id() unless the caller already is a
// system admin ("the system-session shortcut was rejected on purpose", migration 000103), so every call
// here runs in the REAL actor's own RLS session — exactly like the Gate 1 adversarial tests
// (internal/hub/adapters/grouporganizer_rls_integration_test.go). The explicit authorize() calls below
// are defence in depth on top of that session's RLS, not a replacement for it: a link's own INSERT is
// also re-checked by the table's WITH CHECK, but a composite foreign key does not consult the RLS of the
// table it points to, so the catalog entry's ORIGIN authorization is proven by a plain SELECT under RLS
// (which simply returns no rows if the actor has no access there) before ever attempting the INSERT.
package grouporganizer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/opsmetrics"
)

// Telemetry (ADR-0059 Gate 5): closed-set label values only (docs/architecture/OBSERVABILITY.md), same
// rule opsmetrics already enforces elsewhere in the Hub (internal/hub/adapters/events.go) — never a
// hub/tenant/group id, a JID, a display name or any other value that came from a request.
var (
	writesTotal = opsmetrics.NewCounter("omnira_hub_group_organizer_writes_total",
		"Smart Group Organizer writes by operation (ADR-0059)", "op",
		"catalog_upsert", "link_create", "link_revoke", "link_transfer", "relationship_create", "relationship_revoke")
	resolveTotal = opsmetrics.NewCounter("omnira_hub_group_organizer_resolve_context_total",
		"Smart Group Organizer context resolutions by outcome (ADR-0059 Gate 4)", "status",
		"resolved", "ambiguous", "no_access")
)

var (
	// ErrForbidden: the actor has no 'groups' management access on the tenant(s) this call touches, or is
	// not a member of the hub at all. The HTTP layer answers the same uniform 404 as ErrNotFound.
	ErrForbidden = errors.New("grouporganizer: forbidden")
	// ErrNotFound: the catalog entry, link or relationship named does not exist in this hub (or is simply
	// not visible to this actor — the two are indistinguishable on purpose, no enumeration oracle).
	ErrNotFound = errors.New("grouporganizer: not found")
	// ErrInvalid: well-formed but refused (422).
	ErrInvalid = errors.New("grouporganizer: invalid request")
	// ErrConflict: a concurrent write, a version mismatch, or a uniqueness rule already satisfied (409).
	ErrConflict = errors.New("grouporganizer: conflict")
	// ErrKeyMismatch: the Idempotency-Key was already used for a different request.
	ErrKeyMismatch = errors.New("grouporganizer: idempotency key reused with a different request")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

var providerGroupIDRe = regexp.MustCompile(`^[0-9]{5,20}(-[0-9]{1,20})?@g\.us$`)

const (
	RoleConsumer = "consumer"
	RoleSupplier = "supplier"
	RoleOperator = "operator"
	RolePartner  = "partner"
	RoleObserver = "observer"
)

var validRoles = map[string]bool{
	RoleConsumer: true, RoleSupplier: true, RoleOperator: true, RolePartner: true, RoleObserver: true,
}

// Service is safe for concurrent use.
type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func (s *Service) tx(ctx context.Context, actor uuid.UUID, fn func(c context.Context, q platformdb.Querier) error) error {
	if actor == uuid.Nil {
		return ErrForbidden
	}
	return platformdb.WithTenantSession(ctx, s.pool, actor, false, func(c context.Context) error {
		return fn(c, platformdb.QuerierFromContext(c, s.pool))
	})
}

// authorize asks has_hub_manage_access again, live, in this transaction — the same question the RLS
// policies of migration 000183 ask, not a shortcut around them.
func authorize(ctx context.Context, q platformdb.Querier, tenant, actor, hub uuid.UUID) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT has_hub_manage_access($1, $2, 'groups', $3)`, tenant, actor, hub).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

func audit(ctx context.Context, q platformdb.Querier, actor uuid.UUID, action, resourceType string, resourceID uuid.UUID, meta map[string]any) error {
	meta["via"] = "hub-group-organizer"
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata)
	                      VALUES ($1, NULL, $2, $3, $4, $5, 'success', $6, $7)`,
		uuid.New(), actor, action, resourceType, resourceID.String(), uuid.NewString(), raw)
	return err
}

// ------------------------------------------------------------------ catalog

type CatalogEntry struct {
	ID                        uuid.UUID `json:"id"`
	HubID                     uuid.UUID `json:"hub_id"`
	SourceTenantID            uuid.UUID `json:"source_tenant_id"`
	SourceChannelConnectionID uuid.UUID `json:"source_channel_connection_id"`
	ProviderGroupID           string    `json:"provider_group_id"`
	DisplayName               string    `json:"display_name"`
	SourceState               string    `json:"source_state"`
	Version                   int64     `json:"version"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
	LastSeenAt                time.Time `json:"last_seen_at"`
}

const catalogCols = `id, hub_id, source_tenant_id, source_channel_connection_id, provider_group_id, display_name, source_state, version, created_at, updated_at, last_seen_at`

func scanCatalogEntry(row pgx.Row) (CatalogEntry, error) {
	var e CatalogEntry
	err := row.Scan(&e.ID, &e.HubID, &e.SourceTenantID, &e.SourceChannelConnectionID, &e.ProviderGroupID,
		&e.DisplayName, &e.SourceState, &e.Version, &e.CreatedAt, &e.UpdatedAt, &e.LastSeenAt)
	return e, err
}

// ListCatalog returns the catalog entries this actor's 'groups' delegation makes visible — RLS alone
// decides the set (hub_group_catalog_rw, migration 000183); this call adds nothing to it.
func (s *Service) ListCatalog(ctx context.Context, actor, hub uuid.UUID, before *time.Time, limit int) ([]CatalogEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := []CatalogEntry{}
	err := s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		var rows pgx.Rows
		var err error
		if before != nil {
			rows, err = q.Query(c, `SELECT `+catalogCols+` FROM hub_group_catalog
			                        WHERE hub_id = $1 AND created_at < $2 ORDER BY created_at DESC LIMIT $3`, hub, *before, limit)
		} else {
			rows, err = q.Query(c, `SELECT `+catalogCols+` FROM hub_group_catalog
			                        WHERE hub_id = $1 ORDER BY created_at DESC LIMIT $2`, hub, limit)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanCatalogEntry(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

func cleanDisplayName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n > 200 {
		return "", invalid("display_name must have at most 200 characters")
	}
	return name, nil
}

// UpsertCatalogEntry registers (or refreshes) one group's metadata. Idempotent by construction: the
// natural key (hub, source tenant, connection, provider group id) is the resource's identity, so the
// SAME group registered twice updates the same row instead of creating a duplicate — no Idempotency-Key
// header is needed for THIS call specifically because retrying it twice is always safe and never doubles
// a side effect.
func (s *Service) UpsertCatalogEntry(ctx context.Context, actor, hub, sourceTenant, channelConnection uuid.UUID, providerGroupID, displayName string) (CatalogEntry, error) {
	if sourceTenant == uuid.Nil || channelConnection == uuid.Nil {
		return CatalogEntry{}, invalid("source_tenant_id and source_channel_connection_id are required")
	}
	if !providerGroupIDRe.MatchString(providerGroupID) {
		return CatalogEntry{}, invalid("provider_group_id is not a WhatsApp group id")
	}
	displayName, err := cleanDisplayName(displayName)
	if err != nil {
		return CatalogEntry{}, err
	}
	var out CatalogEntry
	err = s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, sourceTenant, actor, hub); err != nil {
			return err
		}
		row := q.QueryRow(c, `
			INSERT INTO hub_group_catalog (hub_id, source_tenant_id, source_channel_connection_id, provider_group_id, display_name)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (hub_id, source_tenant_id, source_channel_connection_id, provider_group_id)
			DO UPDATE SET display_name = EXCLUDED.display_name, source_state = 'available',
			              last_seen_at = now(), updated_at = now(), version = hub_group_catalog.version + 1
			RETURNING `+catalogCols, hub, sourceTenant, channelConnection, providerGroupID, displayName)
		var perr error
		out, perr = scanCatalogEntry(row)
		if perr != nil {
			return mapWriteErr(perr)
		}
		if err := audit(c, q, actor, "hub.group_organizer.catalog.upserted", "hub_group_catalog", out.ID,
			map[string]any{"hub_id": hub, "source_tenant_id": sourceTenant}); err != nil {
			return err
		}
		writesTotal.Inc("catalog_upsert")
		// ADR-0059 addendum 9: a catalog entry gets an active self-link to its OWN origin tenant
		// automatically, role 'operator' — the same gap hub_autodiscover_catalog_upsert (migration 000194)
		// closes for the background-discovery path. NOT EXISTS matches any row at all (active or revoked),
		// so a prior deliberate revoke of this exact self-link is never silently undone.
		tag, err := q.Exec(c, `
			INSERT INTO hub_group_tenant_links (hub_id, hub_group_id, tenant_id, role, assigned_by)
			SELECT $1, $2, $3, 'operator', $4
			WHERE NOT EXISTS (SELECT 1 FROM hub_group_tenant_links WHERE hub_group_id = $2 AND tenant_id = $3)`,
			hub, out.ID, sourceTenant, actor)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			if err := audit(c, q, actor, "hub.group_organizer.link.created", "hub_group_tenant_link", out.ID,
				map[string]any{"hub_id": hub, "hub_group_id": out.ID, "tenant_id": sourceTenant, "role": "operator", "via": "auto_self_link"}); err != nil {
				return err
			}
			writesTotal.Inc("link_create")
		}
		return nil
	})
	return out, err
}

// DiscoveredConnection is one WhatsApp channel connection already configured for the tenant — never its
// secret_ref, never anything from channel_credentials.
type DiscoveredConnection struct {
	ID               uuid.UUID `json:"id"`
	Channel          string    `json:"channel"`
	Provider         string    `json:"provider"`
	ExternalNumberID string    `json:"external_number_id"`
	Status           string    `json:"status"`
}

// DiscoverConnections lists the tenant's WhatsApp channel connections, so the "add to catalog" UI can
// offer a connection picker before the group picker instead of a blank UUID field the operator has no
// way to fill in (found broken live in production, same shape as DiscoverGroups below: the group picker
// only ever fires once a valid connection UUID is already typed in by hand).
func (s *Service) DiscoverConnections(ctx context.Context, actor, hub, sourceTenant uuid.UUID) ([]DiscoveredConnection, error) {
	if sourceTenant == uuid.Nil {
		return nil, invalid("source_tenant_id is required")
	}
	out := []DiscoveredConnection{}
	err := s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, sourceTenant, actor, hub); err != nil {
			return err
		}
		rows, err := q.Query(c, `SELECT id, channel, provider, external_number_id, status FROM hub_discover_tenant_connections($1, $2, $3)`,
			hub, sourceTenant, actor)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var conn DiscoveredConnection
			if err := rows.Scan(&conn.ID, &conn.Channel, &conn.Provider, &conn.ExternalNumberID, &conn.Status); err != nil {
				return err
			}
			out = append(out, conn)
		}
		return rows.Err()
	})
	return out, err
}

// DiscoveredGroup is one WhatsApp group already known to the tenant's own wa_groups table — never
// its enabled status, never a message, never a participant.
type DiscoveredGroup struct {
	ProviderGroupID string `json:"provider_group_id"`
	Name            string `json:"name"`
}

// DiscoverGroups lists the groups wa_groups already knows about for one tenant's connection, so the "add
// to catalog" UI can offer real groups instead of a blank text field the operator has nowhere to copy a
// JID into (found broken live in production: UpsertCatalogEntry always worked, but nothing in the product
// ever showed an operator a provider_group_id to type there). A group the tenant's own team has never
// opened once in their Groups page — so it was never written to wa_groups — will not appear here yet; a
// live WAHA listing would need 'channels'-scoped channel_credentials, a materially bigger grant than
// 'groups' was ever meant to carry (ADR-0059 §3), so that stays explicitly out of scope here.
func (s *Service) DiscoverGroups(ctx context.Context, actor, hub, sourceTenant, channelConnection uuid.UUID) ([]DiscoveredGroup, error) {
	if sourceTenant == uuid.Nil || channelConnection == uuid.Nil {
		return nil, invalid("source_tenant_id and source_channel_connection_id are required")
	}
	out := []DiscoveredGroup{}
	err := s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, sourceTenant, actor, hub); err != nil {
			return err
		}
		rows, err := q.Query(c, `SELECT provider_group_id, name FROM hub_discover_tenant_groups($1, $2, $3, $4)`,
			hub, sourceTenant, channelConnection, actor)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g DiscoveredGroup
			if err := rows.Scan(&g.ProviderGroupID, &g.Name); err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out, err
}

func mapWriteErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return fmt.Errorf("%w: %s", ErrConflict, "already exists")
		case "23503": // foreign_key_violation
			return fmt.Errorf("%w: %s", ErrNotFound, "referenced resource does not exist in this hub")
		case "23514": // check_violation
			return fmt.Errorf("%w: %s", ErrInvalid, "value rejected by a database constraint")
		}
	}
	return err
}

// ------------------------------------------------------------------ links

type Link struct {
	ID         uuid.UUID  `json:"id"`
	HubID      uuid.UUID  `json:"hub_id"`
	GroupID    uuid.UUID  `json:"hub_group_id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	Role       string     `json:"role"`
	Status     string     `json:"status"`
	AssignedBy *uuid.UUID `json:"assigned_by,omitempty"`
	AssignedAt time.Time  `json:"assigned_at"`
	RevokedBy  *uuid.UUID `json:"revoked_by,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	Version    int64      `json:"version"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

const linkCols = `id, hub_id, hub_group_id, tenant_id, role, status, assigned_by, assigned_at, revoked_by, revoked_at, version, created_at, updated_at`

func scanLink(row pgx.Row) (Link, error) {
	var l Link
	err := row.Scan(&l.ID, &l.HubID, &l.GroupID, &l.TenantID, &l.Role, &l.Status, &l.AssignedBy, &l.AssignedAt,
		&l.RevokedBy, &l.RevokedAt, &l.Version, &l.CreatedAt, &l.UpdatedAt)
	return l, err
}

// ListLinks returns every link (active or revoked) of one catalog group — again, RLS alone decides which
// rows this actor sees (hub_group_tenant_links_rw is per-row, keyed on the LINK's own tenant, not the
// catalog's origin): an actor authorized only for Alpha never sees the row that links the same group to
// Beta (RF-13/SEC-013), even if they can see the catalog entry.
func (s *Service) ListLinks(ctx context.Context, actor, hub, groupID uuid.UUID) ([]Link, error) {
	out := []Link{}
	err := s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		rows, err := q.Query(c, `SELECT `+linkCols+` FROM hub_group_tenant_links
		                        WHERE hub_id = $1 AND hub_group_id = $2 ORDER BY assigned_at`, hub, groupID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanLink(rows)
			if err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, err
}

// ListLinksForHub returns every link (active or revoked) of every catalog group of this hub, in ONE
// round trip — same RLS guarantee as ListLinks (hub_group_tenant_links_rw is per-row, keyed on the
// link's own tenant: an actor only ever sees rows for tenants they are authorized for).
//
// Exists because the Organizer page used to call ListLinks once PER catalog entry (an N+1 of one HTTP
// request per group): harmless while the catalog held a handful of manually-added groups, but autodiscovery
// (ADR-0059 addendum, migration 000187) can grow the catalog to dozens of groups on its own, and that many
// concurrent requests queued against this environment's small DB connection pool, found live in production
// as the Organizer page hanging on "carregando" indefinitely. This collapses it back to one query.
func (s *Service) ListLinksForHub(ctx context.Context, actor, hub uuid.UUID) ([]Link, error) {
	out := []Link{}
	err := s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		rows, err := q.Query(c, `SELECT `+linkCols+` FROM hub_group_tenant_links
		                        WHERE hub_id = $1 ORDER BY hub_group_id, assigned_at`, hub)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanLink(rows)
			if err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, err
}

// AccountRef is a customer_account named just for display — used for a group's additional (non-primary)
// clients (ADR-0059 addendum 8), mirroring internal/groups/adapters/http.go's own accountRef shape.
type AccountRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// RelationshipContext is the tenant-side classification of a catalog entry's real WhatsApp group
// (wa_groups.purpose) plus the CRM customer_account(s) it is linked to, if any — read only, never
// written here (that stays the tenant's own Groups page, see migration 000189/000190/000191/000193).
// Purpose is always present (wa_groups defaults it to 'other'); AccountID/AccountDisplayName are nil when
// the group has no account_id, which is the expected, unremarkable case for an
// 'internal_management'/'internal_team' group. WaGroupID is always present when a row exists at all (the
// query inner-joins wa_groups): it lets the Hub Organizer frontend address the real wa_groups row when it
// calls the tenant's OWN existing PUT/POST .../groups/{id}/account(s) endpoints directly — a real action,
// not routed through this package. AdditionalAccounts is every OTHER client the group also concerns
// (wa_group_additional_accounts, migration 000192 — e.g. a "swap"/peering group); never the primary
// above, always present, empty when there are none.
type RelationshipContext struct {
	HubGroupID         uuid.UUID    `json:"hub_group_id"`
	WaGroupID          uuid.UUID    `json:"wa_group_id"`
	Purpose            string       `json:"purpose"`
	AccountID          *uuid.UUID   `json:"account_id,omitempty"`
	AccountDisplayName *string      `json:"account_display_name,omitempty"`
	AdditionalAccounts []AccountRef `json:"additional_accounts"`
}

// RelationshipContextForHub reads every catalog entry's tenant-side classification in one call (same N+1
// lesson as ListLinksForHub above). A catalog entry whose underlying wa_groups row cannot be matched (or
// whose origin tenant this actor no longer has 'groups' access to) is simply absent from the result —
// the Hub Organizer page treats a missing entry as "no classification available", never an error.
func (s *Service) RelationshipContextForHub(ctx context.Context, actor, hub uuid.UUID) ([]RelationshipContext, error) {
	out := []RelationshipContext{}
	err := s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		rows, err := q.Query(c, `SELECT hub_group_id, wa_group_id, purpose, account_id, account_display_name,
		                               additional_account_ids, additional_account_names
		                        FROM hub_group_catalog_relationship_context($1, $2)`, hub, actor)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e RelationshipContext
			var extraIDs []uuid.UUID
			var extraNames []string
			if err := rows.Scan(&e.HubGroupID, &e.WaGroupID, &e.Purpose, &e.AccountID, &e.AccountDisplayName,
				&extraIDs, &extraNames); err != nil {
				return err
			}
			e.AdditionalAccounts = make([]AccountRef, len(extraIDs))
			for i, id := range extraIDs {
				e.AdditionalAccounts[i] = AccountRef{ID: id, Name: extraNames[i]}
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// originTenant reads the catalog group's source_tenant_id UNDER THE CALLER'S OWN RLS SESSION. If the
// actor has no 'groups' access on the origin tenant, the row is invisible and this returns ErrNotFound —
// which is also the correct authorization answer: nobody may create or move a link for a group whose
// origin they cannot manage, whatever tenant the link's OTHER side names.
func originTenant(ctx context.Context, q platformdb.Querier, hub, groupID uuid.UUID) (uuid.UUID, error) {
	var tenant uuid.UUID
	err := q.QueryRow(ctx, `SELECT source_tenant_id FROM hub_group_catalog WHERE id = $1 AND hub_id = $2`, groupID, hub).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	return tenant, err
}

func requestHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func checkIdempotencyKey(key string) error {
	if n := len(key); n < 8 || n > 128 {
		return invalid("Idempotency-Key must have 8-128 characters")
	}
	return nil
}

// receipt looks up a prior receipt for this (hub, actor, scope, key). ok=false with a nil error means
// "no prior receipt, proceed". A mismatched hash is ErrKeyMismatch.
func receipt(ctx context.Context, q platformdb.Querier, hub, actor uuid.UUID, scope, key, hash string) (linkID, relID *uuid.UUID, ok bool, err error) {
	var prevHash string
	err = q.QueryRow(ctx, `SELECT request_hash, hub_group_tenant_link_id, hub_tenant_relationship_id
	                       FROM hub_group_organizer_receipts WHERE hub_id = $1 AND actor_id = $2 AND scope = $3 AND idempotency_key = $4`,
		hub, actor, scope, key).Scan(&prevHash, &linkID, &relID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil, false, nil
	case err != nil:
		return nil, nil, false, err
	}
	if prevHash != hash {
		return nil, nil, false, ErrKeyMismatch
	}
	return linkID, relID, true, nil
}

func saveReceipt(ctx context.Context, q platformdb.Querier, hub, actor uuid.UUID, scope, key, hash string, linkID, relID *uuid.UUID) error {
	_, err := q.Exec(ctx, `INSERT INTO hub_group_organizer_receipts (hub_id, actor_id, scope, idempotency_key, request_hash, hub_group_tenant_link_id, hub_tenant_relationship_id)
	                       VALUES ($1, $2, $3, $4, $5, $6, $7)`, hub, actor, scope, key, hash, linkID, relID)
	return err
}

// CreateLink links an existing catalog group to a tenant (the "Vincular a Beta sem remover Alpha" case).
// Both sides are checked explicitly: the origin (via originTenant, under RLS) and the destination (via
// authorize) — the composite foreign key to hub_group_catalog proves the group exists in this hub, never
// that the caller may manage its origin.
func (s *Service) CreateLink(ctx context.Context, actor, hub, groupID, tenant uuid.UUID, role, idempotencyKey string) (l Link, replayed bool, err error) {
	if err := checkIdempotencyKey(idempotencyKey); err != nil {
		return Link{}, false, err
	}
	if !validRoles[role] {
		return Link{}, false, invalid("role must be one of consumer, supplier, operator, partner, observer")
	}
	if tenant == uuid.Nil {
		return Link{}, false, invalid("tenant_id is required")
	}
	hash := requestHash(hub.String(), groupID.String(), tenant.String(), role)
	err = s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		linkID, _, ok, rerr := receipt(c, q, hub, actor, "link.create", idempotencyKey, hash)
		if rerr != nil {
			return rerr
		}
		if ok {
			replayed = true
			row := q.QueryRow(c, `SELECT `+linkCols+` FROM hub_group_tenant_links WHERE id = $1`, *linkID)
			var perr error
			l, perr = scanLink(row)
			return perr
		}
		if _, err := originTenant(c, q, hub, groupID); err != nil {
			return err
		}
		if err := authorize(c, q, tenant, actor, hub); err != nil {
			return err
		}
		row := q.QueryRow(c, `
			INSERT INTO hub_group_tenant_links (hub_id, hub_group_id, tenant_id, role, assigned_by)
			VALUES ($1, $2, $3, $4, $5) RETURNING `+linkCols, hub, groupID, tenant, role, actor)
		var perr error
		l, perr = scanLink(row)
		if perr != nil {
			return mapWriteErr(perr)
		}
		if err := saveReceipt(c, q, hub, actor, "link.create", idempotencyKey, hash, &l.ID, nil); err != nil {
			return err
		}
		if err := audit(c, q, actor, "hub.group_organizer.link.created", "hub_group_tenant_link", l.ID,
			map[string]any{"hub_id": hub, "hub_group_id": groupID, "tenant_id": tenant, "role": role}); err != nil {
			return err
		}
		writesTotal.Inc("link_create")
		return nil
	})
	return l, replayed, err
}

// RevokeLink ends a tenant's link to a group. expectedVersion must match the link's current version
// (optimistic concurrency: "confirm before acting" — the caller must have read the row it is changing).
func (s *Service) RevokeLink(ctx context.Context, actor, hub, groupID, tenant uuid.UUID, expectedVersion int64) error {
	return s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, tenant, actor, hub); err != nil {
			return err
		}
		var id uuid.UUID
		err := q.QueryRow(c, `
			UPDATE hub_group_tenant_links SET status = 'revoked', revoked_by = $5, revoked_at = now(), version = version + 1, updated_at = now()
			WHERE hub_id = $1 AND hub_group_id = $2 AND tenant_id = $3 AND status = 'active' AND version = $4
			RETURNING id`, hub, groupID, tenant, expectedVersion, actor).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return currentOrNotFound(c, q, hub, groupID, tenant)
		}
		if err != nil {
			return err
		}
		if err := audit(c, q, actor, "hub.group_organizer.link.revoked", "hub_group_tenant_link", id,
			map[string]any{"hub_id": hub, "hub_group_id": groupID, "tenant_id": tenant}); err != nil {
			return err
		}
		writesTotal.Inc("link_revoke")
		return nil
	})
}

// currentOrNotFound distinguishes "no such link at all" (ErrNotFound) from "it exists but is no longer
// active, or your version is stale" (ErrConflict), after an optimistic-concurrency UPDATE matched zero
// rows. It deliberately does NOT filter by status: a link already revoked must still be found here, so
// a second revoke attempt reports 409 (conflict: already revoked), never 404 (as if it never existed).
func currentOrNotFound(ctx context.Context, q platformdb.Querier, hub, groupID, tenant uuid.UUID) error {
	var status string
	err := q.QueryRow(ctx, `SELECT status FROM hub_group_tenant_links WHERE hub_id = $1 AND hub_group_id = $2 AND tenant_id = $3`,
		hub, groupID, tenant).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: the link changed since you read it (status=%s); reload and try again", ErrConflict, status)
}

// TransferLink is the "Transferir de Alpha para Beta" case: atomically revokes the FROM link and creates
// the TO link in the same transaction, so a crash or a retry never leaves the group linked to neither (or
// briefly to both in a way a concurrent reader could observe as a reassignment rather than two writes).
// Both tenants and the origin are authorized explicitly, same as CreateLink + RevokeLink individually.
func (s *Service) TransferLink(ctx context.Context, actor, hub, groupID, from, to uuid.UUID, role string, expectedVersion int64, idempotencyKey string) (l Link, replayed bool, err error) {
	if err := checkIdempotencyKey(idempotencyKey); err != nil {
		return Link{}, false, err
	}
	if !validRoles[role] {
		return Link{}, false, invalid("role must be one of consumer, supplier, operator, partner, observer")
	}
	if to == uuid.Nil || from == uuid.Nil {
		return Link{}, false, invalid("from and to tenant ids are required")
	}
	if from == to {
		return Link{}, false, invalid("from and to must be different tenants")
	}
	hash := requestHash(hub.String(), groupID.String(), from.String(), to.String(), role)
	err = s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		linkID, _, ok, rerr := receipt(c, q, hub, actor, "link.transfer", idempotencyKey, hash)
		if rerr != nil {
			return rerr
		}
		if ok {
			replayed = true
			row := q.QueryRow(c, `SELECT `+linkCols+` FROM hub_group_tenant_links WHERE id = $1`, *linkID)
			var perr error
			l, perr = scanLink(row)
			return perr
		}
		if _, err := originTenant(c, q, hub, groupID); err != nil {
			return err
		}
		if err := authorize(c, q, from, actor, hub); err != nil {
			return err
		}
		if err := authorize(c, q, to, actor, hub); err != nil {
			return err
		}
		tag, err := q.Exec(c, `
			UPDATE hub_group_tenant_links SET status = 'revoked', revoked_by = $5, revoked_at = now(), version = version + 1, updated_at = now()
			WHERE hub_id = $1 AND hub_group_id = $2 AND tenant_id = $3 AND status = 'active' AND version = $4`,
			hub, groupID, from, expectedVersion, actor)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return currentOrNotFound(c, q, hub, groupID, from)
		}
		row := q.QueryRow(c, `
			INSERT INTO hub_group_tenant_links (hub_id, hub_group_id, tenant_id, role, assigned_by)
			VALUES ($1, $2, $3, $4, $5) RETURNING `+linkCols, hub, groupID, to, role, actor)
		var perr error
		l, perr = scanLink(row)
		if perr != nil {
			return mapWriteErr(perr)
		}
		if err := saveReceipt(c, q, hub, actor, "link.transfer", idempotencyKey, hash, &l.ID, nil); err != nil {
			return err
		}
		if err := audit(c, q, actor, "hub.group_organizer.link.transferred", "hub_group_tenant_link", l.ID,
			map[string]any{"hub_id": hub, "hub_group_id": groupID, "from_tenant_id": from, "to_tenant_id": to, "role": role}); err != nil {
			return err
		}
		writesTotal.Inc("link_transfer")
		return nil
	})
	return l, replayed, err
}

// ------------------------------------------------------------------ context resolver (Gate 4)

// ResolveCandidate is one tenant this group is actively linked to AND that the caller may manage —
// RLS (hub_group_tenant_links_rw, migration 000183) is the only filter applied: a tenant the actor
// cannot manage is invisible to the query below, never merely hidden by an extra check here.
type ResolveCandidate struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Role     string    `json:"role"`
}

// ResolveResult mirrors the package's own contract: "resolved" when exactly one tenant qualifies,
// "ambiguous" when more than one does (the caller must pick explicitly — this never merges or sums
// two tenants' access into one operation), "no_access" when none does. "no_access" is also what a
// nonexistent or cross-hub group id produces: the RLS-filtered query simply returns zero rows either
// way, so this endpoint is not an oracle for "does group X exist in this hub" (same no-enumeration
// rule as the rest of this module).
type ResolveResult struct {
	Status     string             `json:"status"` // "resolved" | "ambiguous" | "no_access"
	TenantID   *uuid.UUID         `json:"tenant_id,omitempty"`
	Candidates []ResolveCandidate `json:"candidates,omitempty"`
}

// ResolveContext answers "which ONE tenant should THIS operation act as" for a given catalog group,
// from the actor's own point of view. It is read-only and has no side effects: it does not create a
// ticket, does not touch wa_groups, and is not wired into any ticket/SLA/queue engine — EffectiveAccessResolver
// (internal/hub/application/effective_access.go) still resolves exactly one tenant per request; this
// function only helps a caller pick WHICH tenant_id to hand it when a group's own links leave that
// ambiguous. It never grants access by itself: the tenant_id it returns still has to pass the normal
// Hub authorization chain (contract -> grant -> scope) on whatever request uses it next.
func (s *Service) ResolveContext(ctx context.Context, actor, hub, groupID uuid.UUID) (ResolveResult, error) {
	var out ResolveResult
	err := s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		rows, err := q.Query(c, `SELECT tenant_id, role FROM hub_group_tenant_links
		                         WHERE hub_id = $1 AND hub_group_id = $2 AND status = 'active'
		                         ORDER BY tenant_id`, hub, groupID)
		if err != nil {
			return err
		}
		defer rows.Close()
		var candidates []ResolveCandidate
		for rows.Next() {
			var cand ResolveCandidate
			if err := rows.Scan(&cand.TenantID, &cand.Role); err != nil {
				return err
			}
			candidates = append(candidates, cand)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		switch len(candidates) {
		case 0:
			out = ResolveResult{Status: "no_access"}
		case 1:
			tenant := candidates[0].TenantID
			out = ResolveResult{Status: "resolved", TenantID: &tenant, Candidates: candidates}
		default:
			out = ResolveResult{Status: "ambiguous", Candidates: candidates}
		}
		return nil
	})
	if err == nil {
		resolveTotal.Inc(out.Status)
	}
	return out, err
}

// ------------------------------------------------------------------ relationships

type Relationship struct {
	ID               uuid.UUID  `json:"id"`
	HubID            uuid.UUID  `json:"hub_id"`
	ConsumerTenantID uuid.UUID  `json:"consumer_tenant_id"`
	SupplierTenantID uuid.UUID  `json:"supplier_tenant_id"`
	ServiceKind      string     `json:"service_kind"`
	ServiceReference string     `json:"service_reference"`
	Status           string     `json:"status"`
	ValidFrom        time.Time  `json:"valid_from"`
	ValidUntil       *time.Time `json:"valid_until,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

const relCols = `id, hub_id, consumer_tenant_id, supplier_tenant_id, service_kind, service_reference, status, valid_from, valid_until, created_at, updated_at`

func scanRelationship(row pgx.Row) (Relationship, error) {
	var r Relationship
	err := row.Scan(&r.ID, &r.HubID, &r.ConsumerTenantID, &r.SupplierTenantID, &r.ServiceKind, &r.ServiceReference,
		&r.Status, &r.ValidFrom, &r.ValidUntil, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// ListRelationships: RLS alone decides visibility (hub_tenant_relationships_rw requires BOTH sides,
// SEC-002) — an actor with only one side never learns the relationship exists.
func (s *Service) ListRelationships(ctx context.Context, actor, hub uuid.UUID) ([]Relationship, error) {
	out := []Relationship{}
	err := s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		rows, err := q.Query(c, `SELECT `+relCols+` FROM hub_tenant_relationships WHERE hub_id = $1 ORDER BY created_at DESC`, hub)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRelationship(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func cleanServiceKind(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n < 1 || n > 100 {
		return "", invalid("service_kind must have 1-100 characters")
	}
	return s, nil
}

func cleanServiceReference(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n > 200 {
		return "", invalid("service_reference must have at most 200 characters")
	}
	return s, nil
}

// CreateRelationship records that consumer buys service_kind from supplier (both under the SAME hub).
// This never grants, widens or sums access by itself — it is read only by EffectiveAccessResolver-
// adjacent UI to derive "Alpha ↔ Beta" shared folders (D-07); the resolver still picks exactly one tenant
// per operation. Both sides are authorized explicitly, matching the relationship's own RLS WITH CHECK.
func (s *Service) CreateRelationship(ctx context.Context, actor, hub, consumer, supplier uuid.UUID, serviceKind, serviceReference, idempotencyKey string) (r Relationship, replayed bool, err error) {
	if err := checkIdempotencyKey(idempotencyKey); err != nil {
		return Relationship{}, false, err
	}
	if consumer == uuid.Nil || supplier == uuid.Nil {
		return Relationship{}, false, invalid("consumer_tenant_id and supplier_tenant_id are required")
	}
	if consumer == supplier {
		return Relationship{}, false, invalid("consumer and supplier must be different tenants")
	}
	serviceKind, err = cleanServiceKind(serviceKind)
	if err != nil {
		return Relationship{}, false, err
	}
	serviceReference, err = cleanServiceReference(serviceReference)
	if err != nil {
		return Relationship{}, false, err
	}
	hash := requestHash(hub.String(), consumer.String(), supplier.String(), serviceKind, serviceReference)
	err = s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		_, relID, ok, rerr := receipt(c, q, hub, actor, "relationship.create", idempotencyKey, hash)
		if rerr != nil {
			return rerr
		}
		if ok {
			replayed = true
			row := q.QueryRow(c, `SELECT `+relCols+` FROM hub_tenant_relationships WHERE id = $1`, *relID)
			var perr error
			r, perr = scanRelationship(row)
			return perr
		}
		if err := authorize(c, q, consumer, actor, hub); err != nil {
			return err
		}
		if err := authorize(c, q, supplier, actor, hub); err != nil {
			return err
		}
		row := q.QueryRow(c, `
			INSERT INTO hub_tenant_relationships (hub_id, consumer_tenant_id, supplier_tenant_id, service_kind, service_reference, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+relCols, hub, consumer, supplier, serviceKind, serviceReference, actor)
		var perr error
		r, perr = scanRelationship(row)
		if perr != nil {
			return mapWriteErr(perr)
		}
		if err := saveReceipt(c, q, hub, actor, "relationship.create", idempotencyKey, hash, nil, &r.ID); err != nil {
			return err
		}
		if err := audit(c, q, actor, "hub.group_organizer.relationship.created", "hub_tenant_relationship", r.ID,
			map[string]any{"hub_id": hub, "consumer_tenant_id": consumer, "supplier_tenant_id": supplier, "service_kind": serviceKind}); err != nil {
			return err
		}
		writesTotal.Inc("relationship_create")
		return nil
	})
	return r, replayed, err
}

// RevokeRelationship ends a commercial relationship. It does not touch any group link: links made while
// the relationship stood keep existing (D-07 only governs how the UI derives SHARED FOLDERS, not whether
// a link survives), consistent with "not summing or silently withdrawing access by side effect".
func (s *Service) RevokeRelationship(ctx context.Context, actor, hub, relationshipID uuid.UUID) error {
	return s.tx(ctx, actor, func(c context.Context, q platformdb.Querier) error {
		var consumer, supplier uuid.UUID
		err := q.QueryRow(c, `SELECT consumer_tenant_id, supplier_tenant_id FROM hub_tenant_relationships WHERE id = $1 AND hub_id = $2`,
			relationshipID, hub).Scan(&consumer, &supplier)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := authorize(c, q, consumer, actor, hub); err != nil {
			return err
		}
		if err := authorize(c, q, supplier, actor, hub); err != nil {
			return err
		}
		tag, err := q.Exec(c, `UPDATE hub_tenant_relationships SET status = 'revoked', updated_at = now() WHERE id = $1 AND hub_id = $2 AND status = 'active'`,
			relationshipID, hub)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: already revoked or suspended", ErrConflict)
		}
		if err := audit(c, q, actor, "hub.group_organizer.relationship.revoked", "hub_tenant_relationship", relationshipID,
			map[string]any{"hub_id": hub}); err != nil {
			return err
		}
		writesTotal.Inc("relationship_revoke")
		return nil
	})
}
