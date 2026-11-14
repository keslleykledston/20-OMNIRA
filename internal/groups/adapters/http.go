package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	accountsadapters "github.com/omnira/omnira/internal/accounts/adapters"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	mediaports "github.com/omnira/omnira/internal/media/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsports "github.com/omnira/omnira/internal/tickets/ports"
)

// Directory lists the groups of ONE of the tenant's WhatsApp connections, so an administrator can choose
// which ones to read. A tenant may run more than one WhatsApp instance (several numbers/instâncias) at
// once; their groups are never merged into a single pool. connectionID uuid.Nil asks the directory to pick
// the tenant's single active connection (ErrAmbiguousConnection if there is more than one); a non-nil id
// asks for exactly that connection (ErrNoConnection if it is not an active connection of THIS tenant).
type Directory interface {
	List(ctx context.Context, tenantID, connectionID uuid.UUID) (resolvedConnectionID uuid.UUID, groups []channeldomain.ProviderGroup, err error)
}

// Errors a Directory may return; the handler maps them to HTTP statuses.
var (
	ErrNoConnection        = errors.New("groups: no active WhatsApp connection")
	ErrAmbiguousConnection = errors.New("groups: more than one active WhatsApp connection")
)

var providerGroupPattern = regexp.MustCompile(`^[0-9]{5,20}(-[0-9]{1,20})?@g\.us$`)

// mentionPattern matches a WhatsApp @mention as it arrives in a group message's own body text ("@<digits>",
// the raw id the provider renders a mention with — a phone number or, just as often, the digits of an opaque
// @lid). Never a telephone lookup: only ever matched against known channel_participants of THIS group's
// connection (participantMentionNames below), never guessed or dialed.
var mentionPattern = regexp.MustCompile(`@([0-9]{5,20})`)

const (
	permRead   = "group.read"
	permManage = "group.manage"

	directoryTTL     = 60 * time.Second
	availableMax     = 200
	availableDefault = 50
	enabledListCap   = 500
)

// groupPurposes is the closed list an administrator picks from (migration 000178, 'partner'/'supplier'
// added by migration 000190). Never guessed from participants — that is conversation_kind's job;
// purpose is always an explicit choice.
var groupPurposes = map[string]bool{
	"client_management":   true,
	"client_support":      true,
	"internal_management": true,
	"internal_team":       true,
	"partner":             true,
	"supplier":            true,
	"other":               true,
}

// Handler serves the group APIs. Authorization is the role->permission matrix of an ACTIVE
// membership (never a role name) on top of RLS, which scopes every row to the tenant.
type Handler struct {
	pool  *pgxpool.Pool
	audit auditports.AuditEventRepository
	dir   Directory

	// accounts is the SAME accountsadapters.PostgresRepository contacts/adapters.ClassificationHandler already uses
	// (REUSE, not a second accounts client): find-or-create of the local account behind a CRM directory pick runs
	// through its EnsureFromDirectoryPick.
	accounts *accountsadapters.PostgresRepository
	// resolver is the tenant-scoped ticketsports.TicketingRuntimeResolver (ADR-0059 addendum 3): lets an operator of
	// a groups-only tenant (no contacts/tickets, so customer_accounts starts empty) search the tenant's own CRM
	// directly from LinkAccount instead of only picking among already-registered local accounts. nil until
	// WithCompanyDirectoryResolver is called (main.go wiring); a directory_company_id without it answers 503, same
	// message as classification_http.go, never a panic.
	resolver ticketsports.TicketingRuntimeResolver

	// mediaReader serves a group's cleared image/PDF (ADR-0015 addendum 2); nil until WithMediaReader is
	// called (main.go wiring, same as 1:1's internal/media/adapters.Reader), a group has media without it
	// answers 503, never a panic.
	mediaReader *GroupMediaReader

	mu    sync.Mutex
	cache map[directoryCacheKey]directoryEntry
	now   func() time.Time
}

// WithCompanyDirectoryResolver wires the SAME tenant-scoped runtime resolver ticketing/contact-classification use
// (no second K3G credential/client). Same name/pattern as
// internal/contacts/adapters.ClassificationHandler.SetCompanyDirectoryResolver.
func (h *Handler) WithCompanyDirectoryResolver(r ticketsports.TicketingRuntimeResolver) *Handler {
	h.resolver = r
	return h
}

// WithMediaReader wires the group image/PDF server (ADR-0015 addendum 2).
func (h *Handler) WithMediaReader(mr *GroupMediaReader) *Handler {
	h.mediaReader = mr
	return h
}

// directoryCacheKey separates the cache by WHICH connection was asked for: two WhatsApp instances of the
// same tenant have different group lists and must never share a cache entry.
type directoryCacheKey struct {
	tenant uuid.UUID
	hint   uuid.UUID
}

type directoryEntry struct {
	at     time.Time
	conn   uuid.UUID
	groups []channeldomain.ProviderGroup
}

func NewHandler(pool *pgxpool.Pool, audit auditports.AuditEventRepository, dir Directory) *Handler {
	return &Handler{pool: pool, audit: audit, dir: dir, accounts: accountsadapters.NewPostgresRepository(pool), cache: map[directoryCacheKey]directoryEntry{}, now: time.Now}
}

// ---- shapes

type lastMessage struct {
	AuthorName  string `json:"author_name"`
	FromMe      bool   `json:"from_me"`
	MessageType string `json:"message_type"`
	Preview     string `json:"preview"`
}

// accountRef names one customer_account for display — used both for the group's additional (non-primary)
// clients below and anywhere else a bare id+name pair of an account is enough.
type accountRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type groupItem struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	Enabled     bool       `json:"enabled"`
	Purpose     string     `json:"purpose"`
	AccountID   *uuid.UUID `json:"account_id"`
	AccountName *string    `json:"account_name"`
	// AdditionalAccounts: every OTHER client this group also concerns (ADR-0059 addendum 8, e.g. a
	// "swap"/peering group between two of the tenant's own clients) — never the primary above, always a
	// slice (empty, never null) so the frontend never special-cases absence.
	AdditionalAccounts []accountRef `json:"additional_accounts"`
	ConnectionID       uuid.UUID    `json:"connection_id"`
	ConnectionAccount  string       `json:"connection_account"`
	AIObserverEnabled  bool         `json:"ai_observer_enabled"`
	IsAlertChannel     bool         `json:"is_alert_channel"`
	LastMessageAt      *time.Time   `json:"last_message_at"`
	LastMessage        *lastMessage `json:"last_message"`
}

// suggestionItem is the AI observer's suggestion for one message (ADR-0055) — never anything acted on, just
// shown to whoever already has group.read so a human can review it.
type suggestionItem struct {
	Category        string  `json:"category"`
	Summary         string  `json:"summary"`
	SuggestedAction string  `json:"suggested_action"`
	Confidence      float64 `json:"confidence"`
}

type availableItem struct {
	ProviderGroupID  string     `json:"provider_group_id"`
	Name             string     `json:"name"`
	ParticipantCount int        `json:"participant_count"`
	Enabled          bool       `json:"enabled"`
	GroupID          *uuid.UUID `json:"group_id"`
}

type messageItem struct {
	ID          uuid.UUID       `json:"id"`
	AuthorName  string          `json:"author_name"`
	FromMe      bool            `json:"from_me"`
	MessageType string          `json:"message_type"`
	Body        string          `json:"body"`
	SentAt      time.Time       `json:"sent_at"`
	Suggestion  *suggestionItem `json:"suggestion"`
	// ParticipantID addresses channel_participants (ADR-0015 addendum) so the UI can open the alias/company
	// editor for this message's author — never the raw JID/phone (never served here).
	ParticipantID *uuid.UUID `json:"participant_id"`
	CompanyName   *string    `json:"company_name"`
	CompanyRole   *string    `json:"company_role"`
	// IsDownstreamClient (ADR-0015 addendum 3): the linked company is itself a client of the Client the group
	// already represents (ex.: Uplink, cliente da 4WNET), not the Client's own staff. Nil when there is no
	// company linked yet.
	IsDownstreamClient *bool `json:"is_downstream_client"`
	// ImageCaption (ADR-0015 addendum 2): the vision step's already-done description of this message's own
	// image/PDF, when there is one. Nil while pending, on a text message, or when analysis never completed.
	ImageCaption *string `json:"image_caption"`
}

// ---- helpers

func (h *Handler) session(w http.ResponseWriter, r *http.Request, permission string) (*tenancydomain.TenantContext, bool) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	var ok bool
	if err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`,
		tc.TenantID, tc.ActorID, permission).Scan(&ok); err != nil {
		http.Error(w, "failed to check permission", http.StatusInternalServerError)
		return nil, false
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return tc, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func pgCode(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

// attempt runs one statement that may violate a constraint (ADR-0058's alert-channel CHECK/unique index) inside
// a savepoint: on failure only the savepoint is rolled back, so the request's own transaction stays usable and
// the handler can still answer with a normal HTTP error instead of leaving an aborted transaction for the
// caller to commit (same pattern as internal/sectors/adapters.attempt).
func attempt(ctx context.Context, q platformdb.Querier, run func() error) error {
	if _, err := q.Exec(ctx, `SAVEPOINT group_attempt`); err != nil {
		return err
	}
	if err := run(); err != nil {
		_, _ = q.Exec(ctx, `ROLLBACK TO SAVEPOINT group_attempt`)
		return err
	}
	_, err := q.Exec(ctx, `RELEASE SAVEPOINT group_attempt`)
	return err
}

func groupID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("group_id"))
	if err != nil {
		http.Error(w, "invalid group_id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) record(r *http.Request, tc *tenancydomain.TenantContext, id uuid.UUID, action auditdomain.AuditAction, meta map[string]any) {
	if h.audit == nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, action, auditdomain.ResourceGroup, id, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	for k, v := range meta {
		ev.SetMetadata(k, v)
	}
	_ = h.audit.Store(r.Context(), ev)
}

// directory returns one connection's WhatsApp groups, cached for a minute per (tenant, hint): the
// provider's answer lists every participant of every group and is heavy. hint is uuid.Nil to let the
// directory pick the tenant's single active connection, or a specific connection id to disambiguate when
// the tenant runs more than one WhatsApp instance at once.
func (h *Handler) directory(ctx context.Context, tenantID, hint uuid.UUID) (uuid.UUID, []channeldomain.ProviderGroup, error) {
	key := directoryCacheKey{tenant: tenantID, hint: hint}
	h.mu.Lock()
	if e, ok := h.cache[key]; ok && h.now().Sub(e.at) < directoryTTL {
		h.mu.Unlock()
		return e.conn, e.groups, nil
	}
	h.mu.Unlock()
	if h.dir == nil {
		return uuid.Nil, nil, ErrNoConnection
	}
	conn, groups, err := h.dir.List(ctx, tenantID, hint)
	if err != nil {
		return uuid.Nil, nil, err
	}
	h.mu.Lock()
	h.cache[key] = directoryEntry{at: h.now(), conn: conn, groups: groups}
	h.mu.Unlock()
	return conn, groups, nil
}

// connectionHint reads an optional connection_id query/body value, used to pick which WhatsApp instance's
// groups to work with when a tenant runs more than one at once. Empty means "let the directory decide"
// (uuid.Nil); an invalid id is reported to the caller rather than silently ignored.
func connectionHint(raw string) (uuid.UUID, bool) {
	if raw == "" {
		return uuid.Nil, true
	}
	id, err := uuid.Parse(raw)
	return id, err == nil
}

func directoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoConnection):
		http.Error(w, "no active WhatsApp connection", http.StatusConflict)
	case errors.Is(err, ErrAmbiguousConnection):
		http.Error(w, "more than one active WhatsApp connection", http.StatusConflict)
	default:
		http.Error(w, "WhatsApp is unavailable", http.StatusBadGateway)
	}
}

const groupSelect = `
	SELECT g.id, g.name, g.enabled, g.purpose, g.account_id, ca.name, g.channel_connection_id, cc.external_account_id,
	       g.ai_observer_enabled, g.is_alert_channel, g.last_message_at, lm.author_name, lm.from_me, lm.message_type, left(lm.body, 160)
	FROM wa_groups g
	LEFT JOIN customer_accounts ca ON ca.tenant_id = g.tenant_id AND ca.id = g.account_id
	LEFT JOIN channel_connections cc ON cc.tenant_id = g.tenant_id AND cc.id = g.channel_connection_id
	LEFT JOIN LATERAL (
	  -- the author shows under the team's alias when the participant is a known contact
	  SELECT COALESCE(NULLIF(ct.alias, ''), m.author_name) AS author_name, m.from_me, m.message_type, m.body FROM wa_group_messages m
	  LEFT JOIN channel_participants cp ON cp.tenant_id = m.tenant_id AND cp.id = m.sender_channel_participant_id
	  LEFT JOIN contacts ct ON ct.tenant_id = cp.tenant_id AND ct.id = cp.contact_id
	  WHERE m.tenant_id = g.tenant_id AND m.group_id = g.id
	  ORDER BY m.sent_at DESC, m.id DESC LIMIT 1) lm ON true`

func scanGroup(row pgx.Row) (groupItem, error) {
	var it groupItem
	var author, mtype, preview *string
	var fromMe *bool
	if err := row.Scan(&it.ID, &it.Name, &it.Enabled, &it.Purpose, &it.AccountID, &it.AccountName, &it.ConnectionID, &it.ConnectionAccount,
		&it.AIObserverEnabled, &it.IsAlertChannel, &it.LastMessageAt, &author, &fromMe, &mtype, &preview); err != nil {
		return it, err
	}
	it.AdditionalAccounts = []accountRef{}
	if author != nil && fromMe != nil && mtype != nil && preview != nil {
		it.LastMessage = &lastMessage{AuthorName: *author, FromMe: *fromMe, MessageType: *mtype, Preview: *preview}
	}
	return it, nil
}

// additionalAccountsFor loads one group's additional (non-primary) clients (ADR-0059 addendum 8).
func additionalAccountsFor(ctx context.Context, q platformdb.Querier, tenantID, groupID uuid.UUID) ([]accountRef, error) {
	rows, err := q.Query(ctx, `
		SELECT a.account_id, ca.name FROM wa_group_additional_accounts a
		JOIN customer_accounts ca ON ca.tenant_id = a.tenant_id AND ca.id = a.account_id
		WHERE a.tenant_id = $1 AND a.group_id = $2 ORDER BY ca.name`, tenantID, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []accountRef{}
	for rows.Next() {
		var ref accountRef
		if err := rows.Scan(&ref.ID, &ref.Name); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// additionalAccountsForMany batches the same lookup across a whole page of groups (List) — one query, not
// one per row, same N+1 lesson learned elsewhere this session (ListLinksForHub/RelationshipContextForHub).
func additionalAccountsForMany(ctx context.Context, q platformdb.Querier, tenantID uuid.UUID, groupIDs []uuid.UUID) (map[uuid.UUID][]accountRef, error) {
	out := map[uuid.UUID][]accountRef{}
	if len(groupIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT a.group_id, a.account_id, ca.name FROM wa_group_additional_accounts a
		JOIN customer_accounts ca ON ca.tenant_id = a.tenant_id AND ca.id = a.account_id
		WHERE a.tenant_id = $1 AND a.group_id = ANY($2) ORDER BY ca.name`, tenantID, groupIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var gid uuid.UUID
		var ref accountRef
		if err := rows.Scan(&gid, &ref.ID, &ref.Name); err != nil {
			return nil, err
		}
		out[gid] = append(out[gid], ref)
	}
	return out, rows.Err()
}

// ---- handlers

// List returns the groups an administrator enabled, most recently active first. account_id=<uuid> narrows
// to one client's groups; account_id=none finds the ones nobody has linked to a client yet. connection_id
// narrows to one WhatsApp instance, for a tenant running more than one at once — their groups are never
// merged into a single pool.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permRead)
	if !ok {
		return
	}
	args := []any{tc.TenantID}
	where := "g.tenant_id = $1 AND g.enabled"
	if v := r.URL.Query().Get("account_id"); v != "" {
		if v == "none" {
			where += " AND g.account_id IS NULL"
		} else if id, err := uuid.Parse(v); err == nil {
			args = append(args, id)
			where += fmt.Sprintf(" AND g.account_id = $%d", len(args))
		} else {
			http.Error(w, "invalid account_id", http.StatusBadRequest)
			return
		}
	}
	if v := r.URL.Query().Get("connection_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			http.Error(w, "invalid connection_id", http.StatusBadRequest)
			return
		}
		args = append(args, id)
		where += fmt.Sprintf(" AND g.channel_connection_id = $%d", len(args))
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	rows, err := q.Query(r.Context(),
		groupSelect+` WHERE `+where+`
		ORDER BY g.last_message_at DESC NULLS LAST, lower(g.name), g.id LIMIT `+strconv.Itoa(enabledListCap), args...)
	if err != nil {
		http.Error(w, "failed to list groups", http.StatusInternalServerError)
		return
	}
	items := []groupItem{}
	ids := []uuid.UUID{}
	for rows.Next() {
		it, err := scanGroup(rows)
		if err != nil {
			rows.Close()
			http.Error(w, "failed to read groups", http.StatusInternalServerError)
			return
		}
		items = append(items, it)
		ids = append(ids, it.ID)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		http.Error(w, "failed to read groups", http.StatusInternalServerError)
		return
	}
	additional, err := additionalAccountsForMany(r.Context(), q, tc.TenantID, ids)
	if err != nil {
		http.Error(w, "failed to read groups", http.StatusInternalServerError)
		return
	}
	for i := range items {
		if a, ok := additional[items[i].ID]; ok {
			items[i].AdditionalAccounts = a
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// accountSummaryItem is one of the tenant's active CRM clients with how many of the tenant's ENABLED
// WhatsApp groups concern it — counting both the primary link (wa_groups.account_id) and the additional
// links (wa_group_additional_accounts, ADR-0059 addendum 8), the same two places List/scanGroup already
// look. Powers the tenant's own "Organização" browser (Grupos > Organização): a left panel of clients,
// each drilling down into its groups via the existing List/account_id filter — no ticket/Atendimento
// count here, that stays out of scope until that feature exists.
type accountSummaryItem struct {
	AccountID   uuid.UUID `json:"account_id"`
	AccountName string    `json:"account_name"`
	GroupCount  int       `json:"group_count"`
}

// AccountsSummary lists every active CRM client of the tenant with its linked-group count, in one call —
// same N+1 lesson as additionalAccountsForMany: one aggregate query, never one COUNT per client.
func (h *Handler) AccountsSummary(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permRead)
	if !ok {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	rows, err := q.Query(r.Context(), `
		SELECT ca.id, ca.name, COUNT(DISTINCT grp.group_id)
		FROM customer_accounts ca
		LEFT JOIN (
			SELECT account_id, id AS group_id FROM wa_groups WHERE tenant_id = $1 AND enabled AND account_id IS NOT NULL
			UNION
			SELECT ea.account_id, ea.group_id FROM wa_group_additional_accounts ea
			JOIN wa_groups g ON g.tenant_id = ea.tenant_id AND g.id = ea.group_id AND g.enabled
			WHERE ea.tenant_id = $1
		) grp ON grp.account_id = ca.id
		WHERE ca.tenant_id = $1 AND ca.status = 'active'
		GROUP BY ca.id, ca.name
		ORDER BY ca.name`, tc.TenantID)
	if err != nil {
		http.Error(w, "failed to list accounts", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []accountSummaryItem{}
	for rows.Next() {
		var it accountSummaryItem
		if err := rows.Scan(&it.AccountID, &it.AccountName, &it.GroupCount); err != nil {
			http.Error(w, "failed to read accounts", http.StatusInternalServerError)
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read accounts", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// Available lists ONE WhatsApp instance's groups (name search, capped) with whether each is enabled.
// connection_id picks which instance when the tenant has more than one active; omitted, it works as before
// (the tenant's single active connection, or 409 if there is more than one and no connection_id was given).
func (h *Handler) Available(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	limit := availableDefault
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = min(n, availableMax)
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if len(q) > 100 {
		http.Error(w, "invalid search", http.StatusBadRequest)
		return
	}
	hint, ok := connectionHint(r.URL.Query().Get("connection_id"))
	if !ok {
		http.Error(w, "invalid connection_id", http.StatusBadRequest)
		return
	}
	conn, groups, err := h.directory(r.Context(), tc.TenantID, hint)
	if err != nil {
		directoryError(w, err)
		return
	}
	stored := map[string]struct {
		id      uuid.UUID
		enabled bool
	}{}
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(),
		`SELECT provider_group_id, id, enabled FROM wa_groups WHERE tenant_id = $1 AND channel_connection_id = $2`, tc.TenantID, conn)
	if err != nil {
		http.Error(w, "failed to list groups", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var pid string
		var s struct {
			id      uuid.UUID
			enabled bool
		}
		if err := rows.Scan(&pid, &s.id, &s.enabled); err != nil {
			http.Error(w, "failed to read groups", http.StatusInternalServerError)
			return
		}
		stored[pid] = s
	}
	matches := make([]availableItem, 0, len(groups))
	for _, g := range groups {
		if !providerGroupPattern.MatchString(g.JID) || (q != "" && !strings.Contains(strings.ToLower(g.Name), q)) {
			continue
		}
		it := availableItem{ProviderGroupID: g.JID, Name: g.Name, ParticipantCount: g.ParticipantCount}
		if s, ok := stored[g.JID]; ok {
			id := s.id
			it.Enabled, it.GroupID = s.enabled, &id
		}
		matches = append(matches, it)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Enabled != matches[j].Enabled {
			return matches[i].Enabled
		}
		return strings.ToLower(matches[i].Name) < strings.ToLower(matches[j].Name)
	})
	total := len(matches)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": matches, "count": len(matches), "total": total, "connection_id": conn})
}

// Enable starts reading a group. The group must exist in the tenant's own WhatsApp account, which
// also gives its name; enabling an already known group keeps its history. connection_id picks which
// instance when the tenant has more than one active WhatsApp connection (same rule as Available).
func (h *Handler) Enable(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	var req struct {
		ProviderGroupID *string `json:"provider_group_id"`
		ConnectionID    *string `json:"connection_id"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.ProviderGroupID == nil || !providerGroupPattern.MatchString(*req.ProviderGroupID) {
		http.Error(w, "provider_group_id must be a WhatsApp group id", http.StatusBadRequest)
		return
	}
	hint := uuid.Nil
	if req.ConnectionID != nil {
		id, err := uuid.Parse(*req.ConnectionID)
		if err != nil {
			http.Error(w, "connection_id must be a valid id", http.StatusBadRequest)
			return
		}
		hint = id
	}
	conn, groups, err := h.directory(r.Context(), tc.TenantID, hint)
	if err != nil {
		directoryError(w, err)
		return
	}
	var name string
	found := false
	for _, g := range groups {
		if g.JID == *req.ProviderGroupID {
			name, found = g.Name, true
			break
		}
	}
	if !found {
		http.Error(w, "group not found in this WhatsApp account", http.StatusNotFound)
		return
	}
	if r := []rune(name); len(r) > 200 {
		name = string(r[:200])
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var id uuid.UUID
	var created bool
	err = q.QueryRow(r.Context(), `
		INSERT INTO wa_groups (tenant_id, channel_connection_id, provider_group_id, name, enabled, enabled_by, enabled_at)
		VALUES ($1,$2,$3,$4,true,$5,now())
		ON CONFLICT (tenant_id, channel_connection_id, provider_group_id)
		DO UPDATE SET enabled = true, name = EXCLUDED.name, enabled_by = EXCLUDED.enabled_by, enabled_at = now(), updated_at = now()
		RETURNING id, (xmax = 0)`, tc.TenantID, conn, *req.ProviderGroupID, name, tc.ActorID).Scan(&id, &created)
	if err != nil {
		http.Error(w, "failed to enable group", http.StatusInternalServerError)
		return
	}
	h.record(r, tc, id, auditdomain.ActionGroupEnabled, map[string]any{"created": created})
	it, err := scanGroup(q.QueryRow(r.Context(), groupSelect+` WHERE g.tenant_id = $1 AND g.id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if it.AdditionalAccounts, err = additionalAccountsFor(r.Context(), q, tc.TenantID, id); err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, it)
}

// SetEnabled turns reading on or off for a known group, changes its manual purpose tag, and/or turns the AI
// observer (ADR-0055) on or off — any subset of the three fields at once. Turning reading off stops storing
// new messages; the history stays (deleting it is a separate, explicit action). purpose is never guessed: an
// administrator picks it from the closed list. The AI observer can never be turned on for a group whose
// reading ends up off in the same request — there would be nothing left to observe.
func (h *Handler) SetEnabled(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled           *bool   `json:"enabled"`
		Purpose           *string `json:"purpose"`
		AIObserverEnabled *bool   `json:"ai_observer_enabled"`
		AlertChannel      *bool   `json:"is_alert_channel"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || (req.Enabled == nil && req.Purpose == nil && req.AIObserverEnabled == nil && req.AlertChannel == nil) {
		http.Error(w, "enabled, purpose, ai_observer_enabled and/or is_alert_channel must be set", http.StatusBadRequest)
		return
	}
	if req.Purpose != nil && !groupPurposes[*req.Purpose] {
		http.Error(w, "invalid purpose", http.StatusBadRequest)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var prevEnabled, prevAIObserver, prevAlertChannel bool
	var prevPurpose string
	var accountID *uuid.UUID
	err := q.QueryRow(r.Context(), `SELECT enabled, purpose, ai_observer_enabled, is_alert_channel, account_id FROM wa_groups WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tc.TenantID, id).
		Scan(&prevEnabled, &prevPurpose, &prevAIObserver, &prevAlertChannel, &accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "group not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	newEnabled, newPurpose, newAIObserver, newAlertChannel := prevEnabled, prevPurpose, prevAIObserver, prevAlertChannel
	if req.Enabled != nil {
		newEnabled = *req.Enabled
	}
	if req.Purpose != nil {
		newPurpose = *req.Purpose
	}
	if req.AIObserverEnabled != nil {
		newAIObserver = *req.AIObserverEnabled
	}
	if req.AlertChannel != nil {
		newAlertChannel = *req.AlertChannel
	}
	// Classifying a group (purpose) IS itself an administrator's explicit choice about this specific group
	// (ADR-0015's own requirement) — autodiscovery (migration 000189) leaves enabled at its column default
	// of false exactly because nobody has decided anything about the group yet; the moment the Hub
	// Organizer's Kanban (or any other caller) sets its purpose, that silence ends, so reading starts too,
	// without a separate "Ativar" click the operator would otherwise have to remember. Never overrides an
	// enabled value the request sent explicitly in the SAME call (a deliberate "classify but keep off" still
	// works), and never fires on a purpose value that doesn't actually change anything (purposeChanged below).
	if req.Purpose != nil && req.Enabled == nil && !prevEnabled && *req.Purpose != prevPurpose {
		newEnabled = true
	}
	if newAIObserver && !newEnabled {
		http.Error(w, "ai_observer_enabled requires the group to be reading (enabled)", http.StatusBadRequest)
		return
	}
	// ADR-0058: the only group-send exception in the whole product, so it is kept as narrow as the database
	// itself can make it — never a group tied to a customer account, and (same "no dependent without the
	// thing it depends on" rule as the AI observer) never a group that is not even being read.
	if newAlertChannel && !newEnabled {
		http.Error(w, "is_alert_channel requires the group to be reading (enabled)", http.StatusBadRequest)
		return
	}
	if newAlertChannel && accountID != nil {
		http.Error(w, "is_alert_channel cannot be set on a group linked to a customer account", http.StatusConflict)
		return
	}
	// ADR-0058 addendum: the alert channel is internal-only — client/partner/supplier groups can never
	// be repurposed as it, even unlinked ones. Checked against newPurpose (not prevPurpose) so a single
	// request may reclassify a group to internal_management/internal_team AND turn it into the alert
	// channel at once.
	if newAlertChannel && newPurpose != "internal_management" && newPurpose != "internal_team" {
		http.Error(w, "is_alert_channel requires the group to be classified as Interno · Gestão or Interno · Equipe", http.StatusConflict)
		return
	}
	enabledChanged := newEnabled != prevEnabled
	purposeChanged := newPurpose != prevPurpose
	aiObserverChanged := newAIObserver != prevAIObserver
	alertChannelChanged := newAlertChannel != prevAlertChannel
	if enabledChanged || purposeChanged || aiObserverChanged || alertChannelChanged {
		updateErr := attempt(r.Context(), q, func() error {
			_, err := q.Exec(r.Context(), `
				UPDATE wa_groups SET enabled = $3, purpose = $4, ai_observer_enabled = $8, is_alert_channel = $10, updated_at = now(),
				  enabled_by = CASE WHEN $3 AND NOT $5 THEN $6::uuid ELSE enabled_by END,
				  enabled_at = CASE WHEN $3 AND NOT $5 THEN now() ELSE enabled_at END,
				  purpose_set_by = CASE WHEN $7 THEN $6::uuid ELSE purpose_set_by END,
				  purpose_set_at = CASE WHEN $7 THEN now() ELSE purpose_set_at END,
				  ai_observer_set_by = CASE WHEN $9 THEN $6::uuid ELSE ai_observer_set_by END,
				  ai_observer_set_at = CASE WHEN $9 THEN now() ELSE ai_observer_set_at END,
				  alert_channel_set_by = CASE WHEN $11 THEN $6::uuid ELSE alert_channel_set_by END,
				  alert_channel_set_at = CASE WHEN $11 THEN now() ELSE alert_channel_set_at END
				WHERE tenant_id = $1 AND id = $2`,
				tc.TenantID, id, newEnabled, newPurpose, prevEnabled, tc.ActorID, purposeChanged, newAIObserver, aiObserverChanged,
				newAlertChannel, alertChannelChanged)
			return err
		})
		if updateErr != nil {
			if pgCode(updateErr) == "23505" {
				http.Error(w, "another group in this tenant is already the alert channel", http.StatusConflict)
				return
			}
			http.Error(w, "failed to save group", http.StatusInternalServerError)
			return
		}
		if enabledChanged {
			action := auditdomain.ActionGroupDisabled
			if newEnabled {
				action = auditdomain.ActionGroupEnabled
			}
			h.record(r, tc, id, action, map[string]any{"created": false})
		}
		if purposeChanged {
			h.record(r, tc, id, auditdomain.ActionGroupPurposeSet, map[string]any{"from": prevPurpose, "to": newPurpose})
		}
		if aiObserverChanged {
			action := auditdomain.ActionGroupAIObserverDisabled
			if newAIObserver {
				action = auditdomain.ActionGroupAIObserverEnabled
			}
			h.record(r, tc, id, action, map[string]any{})
		}
		if alertChannelChanged {
			action := auditdomain.ActionGroupAlertChannelDisabled
			if newAlertChannel {
				action = auditdomain.ActionGroupAlertChannelEnabled
			}
			h.record(r, tc, id, action, map[string]any{})
		}
	}
	it, err := scanGroup(q.QueryRow(r.Context(), groupSelect+` WHERE g.tenant_id = $1 AND g.id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if it.AdditionalAccounts, err = additionalAccountsFor(r.Context(), q, tc.TenantID, id); err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// participantMentionNames builds the digits-of-the-raw-id -> resolved-name lookup resolveMentions needs, for
// every participant ever seen on the group's own connection (never cross-connection, never cross-tenant).
// Same "team alias wins" preference as the rest of this file (cp.alias, then a linked contact's alias, then
// the provider's own display_name) — never the raw id itself, which is never served anywhere in this area.
func participantMentionNames(ctx context.Context, q platformdb.Querier, tenantID, connectionID uuid.UUID) (map[string]string, error) {
	rows, err := q.Query(ctx, `
		SELECT cp.external_participant_id, COALESCE(NULLIF(cp.alias, ''), NULLIF(ct.alias, ''), cp.display_name)
		FROM channel_participants cp
		LEFT JOIN contacts ct ON ct.tenant_id = cp.tenant_id AND ct.id = cp.contact_id
		WHERE cp.tenant_id = $1 AND cp.channel_connection_id = $2`, tenantID, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var externalID, name string
		if err := rows.Scan(&externalID, &name); err != nil {
			return nil, err
		}
		if digits := leadingDigits(externalID); digits != "" && name != "" {
			out[digits] = name
		}
	}
	return out, rows.Err()
}

func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// resolveMentions replaces "@<digits>" with "@<Nome>" for every digits sequence that matches a known
// participant of this group's own connection; anything else (an unknown id, a price, an order number) is
// left exactly as written — this only ever ADDS a readable name next to what the sender typed, never removes
// or guesses.
func resolveMentions(body string, names map[string]string) string {
	if len(names) == 0 || !strings.Contains(body, "@") {
		return body
	}
	return mentionPattern.ReplaceAllStringFunc(body, func(m string) string {
		if name, ok := names[mentionPattern.FindStringSubmatch(m)[1]]; ok {
			return "@" + name
		}
		return m
	})
}

// ListMessages pages a group's stored messages, newest first (the client shows the newest last).
// A disabled group's history is still readable until someone deletes it.
func (h *Handler) ListMessages(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permRead)
	if !ok {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	opts := pagination.ParsePageOptionsFromQuery(r)
	if opts.Sort != "" && opts.Sort != "sent_at:desc" {
		http.Error(w, "unsupported sort", http.StatusBadRequest)
		return
	}
	cursor, err := pagination.DecodeCursor(opts.Cursor)
	if err != nil {
		http.Error(w, "invalid pagination", http.StatusBadRequest)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var connectionID uuid.UUID
	if err := q.QueryRow(r.Context(), `SELECT channel_connection_id FROM wa_groups WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id).Scan(&connectionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "group not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	mentions, err := participantMentionNames(r.Context(), q, tc.TenantID, connectionID)
	if err != nil {
		http.Error(w, "failed to read participants", http.StatusInternalServerError)
		return
	}
	args := []any{tc.TenantID, id}
	where := "m.tenant_id = $1 AND m.group_id = $2"
	if cursor != nil {
		cursorID, perr := uuid.Parse(cursor.ID)
		if perr != nil {
			http.Error(w, "invalid pagination", http.StatusBadRequest)
			return
		}
		args = append(args, cursor.Timestamp, cursorID)
		where += " AND (m.sent_at, m.id) < ($3, $4)"
	}
	args = append(args, opts.Limit+1)
	rows, err := q.Query(r.Context(), `
		SELECT m.id, COALESCE(NULLIF(cp.alias, ''), NULLIF(ct.alias, ''), m.author_name), m.from_me, m.message_type, m.body, m.sent_at,
		       s.category, s.summary, s.suggested_action, s.confidence,
		       cp.id, ca.name, cp.company_role, cp.is_downstream_client, van.body
		FROM wa_group_messages m
		LEFT JOIN channel_participants cp ON cp.tenant_id = m.tenant_id AND cp.id = m.sender_channel_participant_id
		LEFT JOIN contacts ct ON ct.tenant_id = cp.tenant_id AND ct.id = cp.contact_id
		LEFT JOIN customer_accounts ca ON ca.tenant_id = cp.tenant_id AND ca.id = cp.company_account_id
		LEFT JOIN wa_group_message_suggestions s ON s.tenant_id = m.tenant_id AND s.group_message_id = m.id
		LEFT JOIN wa_group_message_analysis van ON van.tenant_id = m.tenant_id AND van.group_message_id = m.id AND van.status = 'done'
		WHERE `+where+` ORDER BY m.sent_at DESC, m.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		http.Error(w, "failed to list messages", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := make([]interface{}, 0, opts.Limit)
	var last messageItem
	// Check the limit BEFORE rows.Next(): the extra row (LIMIT n+1) must stay for the hasMore probe.
	for len(items) < opts.Limit && rows.Next() {
		var category, summary, suggestedAction *string
		var confidence *float64
		last.Suggestion = nil
		if err := rows.Scan(&last.ID, &last.AuthorName, &last.FromMe, &last.MessageType, &last.Body, &last.SentAt, &category, &summary, &suggestedAction, &confidence,
			&last.ParticipantID, &last.CompanyName, &last.CompanyRole, &last.IsDownstreamClient, &last.ImageCaption); err != nil {
			http.Error(w, "failed to read messages", http.StatusInternalServerError)
			return
		}
		last.Body = resolveMentions(last.Body, mentions)
		if category != nil {
			action := ""
			if suggestedAction != nil {
				action = *suggestedAction
			}
			last.Suggestion = &suggestionItem{Category: *category, Summary: *summary, SuggestedAction: action, Confidence: *confidence}
		}
		items = append(items, last)
	}
	hasMore := rows.Next()
	result := pagination.NewPageResult(items, opts.Limit, "", hasMore)
	if hasMore {
		result.NextCursor = (&pagination.Cursor{ID: last.ID.String(), Timestamp: last.SentAt}).Encode()
	}
	pagination.WritePaginationHeaders(w, result)
	writeJSON(w, http.StatusOK, result)
}

// DeleteHistory removes every stored message of a group (an explicit, audited action, separate from
// disabling) and requests the removal of its archived files. The group keeps its enabled state. Backups
// already taken still contain the messages until they expire.
func (h *Handler) DeleteHistory(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var exists bool
	if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM wa_groups WHERE tenant_id = $1 AND id = $2)`, tc.TenantID, id).Scan(&exists); err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "group not found", http.StatusNotFound)
		return
	}
	tag, err := q.Exec(r.Context(), `DELETE FROM wa_group_messages WHERE tenant_id = $1 AND group_id = $2`, tc.TenantID, id)
	if err != nil {
		http.Error(w, "failed to delete history", http.StatusInternalServerError)
		return
	}
	// Also ask the archive job (it alone can reach the external disk) to remove this group's cold files.
	if _, err := q.Exec(r.Context(), `UPDATE wa_groups SET last_message_at = NULL, archive_purge_requested_at = now(), updated_at = now() WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id); err != nil {
		http.Error(w, "failed to delete history", http.StatusInternalServerError)
		return
	}
	h.record(r, tc, id, auditdomain.ActionGroupHistoryDeleted, map[string]any{"deleted": tag.RowsAffected()})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": tag.RowsAffected()})
}

// resolveDirectoryAccount finds-or-creates the local account for a company picked straight from the tenant's own
// CRM directory (ADR-0059 addendum 3): a groups-only tenant (customer_accounts starts empty — no contacts/tickets
// ever created one) can link a group to a REAL CRM company without first registering a local account by hand. The
// directory id is revalidated against the tenant's own CompanyDirectory here — a name, CNPJ or status sent by the
// browser is never trusted — exactly like contact classification
// (internal/contacts/adapters.ClassificationHandler.resolveRef), and the same statuses/messages are used for
// consistency. A status of 0 means success.
func (h *Handler) resolveDirectoryAccount(ctx context.Context, tenantID uuid.UUID, dirID string) (accountID uuid.UUID, status int, message string) {
	if h.resolver == nil {
		return uuid.Nil, http.StatusServiceUnavailable, "company directory is not configured"
	}
	rt, err := h.resolver.Resolve(ctx, tenantID)
	if err != nil {
		var resErr *ticketsports.ResolutionError
		if errors.As(err, &resErr) {
			return uuid.Nil, http.StatusServiceUnavailable, "company directory is not available"
		}
		log.Printf("groups: resolve company directory: %v", err)
		return uuid.Nil, http.StatusInternalServerError, "internal error"
	}
	companies, err := rt.CompanyDirectory.ListCompanies(ctx)
	if err != nil {
		log.Printf("groups: list companies: %v", err)
		return uuid.Nil, http.StatusBadGateway, "company directory failed"
	}
	var picked *ticketsports.Company
	for i := range companies {
		if companies[i].ExternalID == dirID {
			picked = &companies[i]
			break
		}
	}
	if picked == nil || !picked.Active {
		return uuid.Nil, http.StatusUnprocessableEntity, "company not found or inactive in the directory"
	}
	id, err := h.accounts.EnsureFromDirectoryPick(ctx, tenantID, rt.ConnectionID, picked.ExternalID, picked.Name)
	if err != nil {
		log.Printf("groups: ensure account from directory pick: %v", err)
		return uuid.Nil, http.StatusInternalServerError, "failed to save account"
	}
	return id, 0, ""
}

// LinkAccount sets which "cliente final" (customer_accounts, ADR-0018) a group belongs to. The request names the
// account EITHER by a local `account_id` (must exist in the SAME tenant) OR by a `directory_company_id` straight
// from the tenant's own CRM directory (ADR-0059 addendum 3, resolveDirectoryAccount above) — mutually exclusive,
// same shape as contacts/adapters.accountRefDTO. A group may belong to at most one account at a time (linking
// again replaces the previous one). Idempotent: linking the account already set changes nothing and is not
// re-audited.
func (h *Handler) LinkAccount(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	var req struct {
		AccountID          *string `json:"account_id"`
		DirectoryCompanyID *string `json:"directory_company_id"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	dirID := ""
	if req.DirectoryCompanyID != nil {
		dirID = strings.TrimSpace(*req.DirectoryCompanyID)
	}
	if (req.AccountID != nil) == (dirID != "") {
		// neither or both: exactly one of account_id/directory_company_id is required.
		http.Error(w, "account_id must be a valid id", http.StatusBadRequest)
		return
	}
	var localAccountID uuid.UUID
	if req.AccountID != nil {
		var err error
		localAccountID, err = uuid.Parse(*req.AccountID)
		if err != nil {
			http.Error(w, "account_id must be a valid id", http.StatusBadRequest)
			return
		}
	}

	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var prev *uuid.UUID
	var prevEnabled bool
	if err := q.QueryRow(r.Context(), `SELECT account_id, enabled FROM wa_groups WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tc.TenantID, id).Scan(&prev, &prevEnabled); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "group not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}

	accountID := localAccountID
	viaDirectory := dirID != ""
	if viaDirectory {
		resolved, status, msg := h.resolveDirectoryAccount(r.Context(), tc.TenantID, dirID)
		if msg != "" {
			http.Error(w, msg, status)
			return
		}
		accountID = resolved
	} else {
		var accountExists bool
		if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM customer_accounts WHERE tenant_id = $1 AND id = $2)`, tc.TenantID, accountID).Scan(&accountExists); err != nil {
			http.Error(w, "failed to read account", http.StatusInternalServerError)
			return
		}
		if !accountExists {
			http.Error(w, "account not found", http.StatusNotFound)
			return
		}
	}

	accountChanged := prev == nil || *prev != accountID
	// Linking a client IS itself an administrator's explicit choice about this specific group (same
	// reasoning as SetEnabled's own purpose-classify case above): autodiscovery (migration 000189) can
	// leave a group cataloged and addressable (wa_group_id known) but never enabled, and the Hub
	// Organizer's "Vincular cliente"/Kanban act on it directly, bypassing the enabled-only List filter —
	// found live in production: a group fully classified and linked to a client that was still never
	// actually being read. Never overrides a deliberate disable: only fires when the group isn't already
	// enabled.
	enableNeeded := !prevEnabled
	if accountChanged || enableNeeded {
		linkErr := attempt(r.Context(), q, func() error {
			_, err := q.Exec(r.Context(), `
				UPDATE wa_groups SET account_id = $3,
				  account_set_by = CASE WHEN $5 THEN $4::uuid ELSE account_set_by END,
				  account_set_at = CASE WHEN $5 THEN now() ELSE account_set_at END,
				  enabled = CASE WHEN $6 THEN true ELSE enabled END,
				  enabled_by = CASE WHEN $6 THEN $4::uuid ELSE enabled_by END,
				  enabled_at = CASE WHEN $6 THEN now() ELSE enabled_at END,
				  updated_at = now()
				WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id, accountID, tc.ActorID, accountChanged, enableNeeded)
			return err
		})
		if linkErr != nil {
			// ADR-0058: a CHECK on wa_groups refuses to combine is_alert_channel with an account link, in
			// either order — this is the order "link an account to the current alert channel".
			if pgCode(linkErr) == "23514" {
				http.Error(w, "this group is the tenant's SLA alert channel and cannot be linked to a customer account", http.StatusConflict)
				return
			}
			http.Error(w, "failed to save group", http.StatusInternalServerError)
			return
		}
		if accountChanged {
			meta := map[string]any{"account_id": accountID}
			if viaDirectory {
				meta["via"] = "crm_directory"
			}
			h.record(r, tc, id, auditdomain.ActionGroupAccountLinked, meta)
		}
		if enableNeeded {
			h.record(r, tc, id, auditdomain.ActionGroupEnabled, map[string]any{"created": false, "via": "account_link"})
		}
	}
	it, err := scanGroup(q.QueryRow(r.Context(), groupSelect+` WHERE g.tenant_id = $1 AND g.id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if it.AdditionalAccounts, err = additionalAccountsFor(r.Context(), q, tc.TenantID, id); err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// UnlinkAccount removes a group's "cliente final" link, leaving it unlinked (found by List's
// account_id=none filter) until someone links it again. Idempotent; a no-op is not audited.
func (h *Handler) UnlinkAccount(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var prev *uuid.UUID
	if err := q.QueryRow(r.Context(), `SELECT account_id FROM wa_groups WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tc.TenantID, id).Scan(&prev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "group not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if prev != nil {
		if _, err := q.Exec(r.Context(), `
			UPDATE wa_groups SET account_id = NULL, updated_at = now() WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id); err != nil {
			http.Error(w, "failed to save group", http.StatusInternalServerError)
			return
		}
		h.record(r, tc, id, auditdomain.ActionGroupAccountUnlinked, map[string]any{"account_id": *prev})
	}
	it, err := scanGroup(q.QueryRow(r.Context(), groupSelect+` WHERE g.tenant_id = $1 AND g.id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if it.AdditionalAccounts, err = additionalAccountsFor(r.Context(), q, tc.TenantID, id); err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// AddAdditionalAccount links a SECOND (or further) client to a group that concerns more than one at once
// (ADR-0059 addendum 8 — e.g. a "swap"/peering group between two of the tenant's own clients, where the
// tenant operates on behalf of both). Unlike LinkAccount (the group's ONE primary client, wa_groups.account_id,
// never touched here), a group may have any number of additional accounts. Same account_id/directory_company_id
// XOR shape as LinkAccount; idempotent (adding one already there changes nothing and is not re-audited); 422
// if the account named is already the group's primary (that would be redundant and ambiguous about which
// column is authoritative).
func (h *Handler) AddAdditionalAccount(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	var req struct {
		AccountID          *string `json:"account_id"`
		DirectoryCompanyID *string `json:"directory_company_id"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	dirID := ""
	if req.DirectoryCompanyID != nil {
		dirID = strings.TrimSpace(*req.DirectoryCompanyID)
	}
	if (req.AccountID != nil) == (dirID != "") {
		http.Error(w, "account_id must be a valid id", http.StatusBadRequest)
		return
	}

	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var primary *uuid.UUID
	var prevEnabled bool
	if err := q.QueryRow(r.Context(), `SELECT account_id, enabled FROM wa_groups WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tc.TenantID, id).Scan(&primary, &prevEnabled); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "group not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}

	var accountID uuid.UUID
	viaDirectory := dirID != ""
	if viaDirectory {
		resolved, status, msg := h.resolveDirectoryAccount(r.Context(), tc.TenantID, dirID)
		if msg != "" {
			http.Error(w, msg, status)
			return
		}
		accountID = resolved
	} else {
		var err error
		accountID, err = uuid.Parse(*req.AccountID)
		if err != nil {
			http.Error(w, "account_id must be a valid id", http.StatusBadRequest)
			return
		}
		var accountExists bool
		if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM customer_accounts WHERE tenant_id = $1 AND id = $2)`, tc.TenantID, accountID).Scan(&accountExists); err != nil {
			http.Error(w, "failed to read account", http.StatusInternalServerError)
			return
		}
		if !accountExists {
			http.Error(w, "account not found", http.StatusNotFound)
			return
		}
	}
	if primary != nil && *primary == accountID {
		http.Error(w, "this account is already the group's primary client", http.StatusUnprocessableEntity)
		return
	}

	var inserted bool
	execErr := attempt(r.Context(), q, func() error {
		tag, err := q.Exec(r.Context(), `
			INSERT INTO wa_group_additional_accounts (tenant_id, group_id, account_id, created_by)
			VALUES ($1, $2, $3, $4) ON CONFLICT (group_id, account_id) DO NOTHING`, tc.TenantID, id, accountID, tc.ActorID)
		if err == nil {
			inserted = tag.RowsAffected() > 0
		}
		return err
	})
	if execErr != nil {
		http.Error(w, "failed to save group", http.StatusInternalServerError)
		return
	}
	if inserted {
		meta := map[string]any{"account_id": accountID}
		if viaDirectory {
			meta["via"] = "crm_directory"
		}
		h.record(r, tc, id, auditdomain.ActionGroupAdditionalAccountLinked, meta)
	}
	// Same reasoning as LinkAccount: adding a client (even a second one) IS an explicit administrator choice
	// about this specific group, so a group autodiscovery left disabled starts being read here too.
	if !prevEnabled {
		if err := attempt(r.Context(), q, func() error {
			_, err := q.Exec(r.Context(), `
				UPDATE wa_groups SET enabled = true, enabled_by = $3, enabled_at = now(), updated_at = now()
				WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id, tc.ActorID)
			return err
		}); err != nil {
			http.Error(w, "failed to save group", http.StatusInternalServerError)
			return
		}
		h.record(r, tc, id, auditdomain.ActionGroupEnabled, map[string]any{"created": false, "via": "account_link"})
	}
	it, err := scanGroup(q.QueryRow(r.Context(), groupSelect+` WHERE g.tenant_id = $1 AND g.id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if it.AdditionalAccounts, err = additionalAccountsFor(r.Context(), q, tc.TenantID, id); err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// RemoveAdditionalAccount removes one additional (non-primary) client from a group. Idempotent; removing one
// not currently there changes nothing and is not re-audited. Never touches wa_groups.account_id.
func (h *Handler) RemoveAdditionalAccount(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(r.PathValue("account_id"))
	if err != nil {
		http.Error(w, "invalid account_id", http.StatusBadRequest)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var exists bool
	if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM wa_groups WHERE tenant_id = $1 AND id = $2)`, tc.TenantID, id).Scan(&exists); err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "group not found", http.StatusNotFound)
		return
	}
	tag, err := q.Exec(r.Context(), `DELETE FROM wa_group_additional_accounts WHERE tenant_id = $1 AND group_id = $2 AND account_id = $3`, tc.TenantID, id, accountID)
	if err != nil {
		http.Error(w, "failed to save group", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() > 0 {
		h.record(r, tc, id, auditdomain.ActionGroupAdditionalAccountUnlinked, map[string]any{"account_id": accountID})
	}
	it, err := scanGroup(q.QueryRow(r.Context(), groupSelect+` WHERE g.tenant_id = $1 AND g.id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if it.AdditionalAccounts, err = additionalAccountsFor(r.Context(), q, tc.TenantID, id); err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// ---- Participant profiles (ADR-0015 addendum): who writes in a group, identified WITHOUT a phone —
// channel_participants, never contacts. See the ADR for why (author_jid is often an opaque @lid).

var validCompanyRole = map[string]bool{
	"employee": true, "owner": true, "technical_contact": true, "billing_contact": true,
	"administrative_contact": true, "representative": true, "contractor": true, "other": true,
}

type groupParticipantItem struct {
	ID               uuid.UUID  `json:"id"`
	DisplayName      string     `json:"display_name"`
	Alias            *string    `json:"alias"`
	CompanyAccountID *uuid.UUID `json:"company_account_id"`
	CompanyName      *string    `json:"company_name"`
	CompanyRole      *string    `json:"company_role"`
	// IsDownstreamClient (ADR-0015 addendum 3): the linked company is itself a client of the Client the
	// tenant already serves (ex.: Uplink, cliente da 4WNET), not the Client's own staff/representative.
	// Always false when there is no company linked (channel_participants_downstream_client_requires_account).
	IsDownstreamClient bool      `json:"is_downstream_client"`
	MessageCount       int       `json:"message_count"`
	LastSeenAt         time.Time `json:"last_seen_at"`
}

func participantID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("participant_id"))
	if err != nil {
		http.Error(w, "invalid participant_id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) recordParticipant(r *http.Request, tc *tenancydomain.TenantContext, id uuid.UUID, action auditdomain.AuditAction, meta map[string]any) {
	if h.audit == nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, action, auditdomain.ResourceChannelParticipant, id, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	for k, v := range meta {
		ev.SetMetadata(k, v)
	}
	_ = h.audit.Store(r.Context(), ev)
}

// Participants lists who has ever written in this group (distinct channel_participants seen as the author of
// one of its messages), newest-seen first. The external_participant_id (the provider's JID, often a phone)
// is NEVER served here — same rule the rest of this ADR follows for group participants.
func (h *Handler) Participants(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permRead)
	if !ok {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var exists bool
	if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM wa_groups WHERE tenant_id = $1 AND id = $2)`, tc.TenantID, id).Scan(&exists); err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "group not found", http.StatusNotFound)
		return
	}
	rows, err := q.Query(r.Context(), `
		SELECT cp.id, cp.display_name, cp.alias, cp.company_account_id, ca.name, cp.company_role, cp.is_downstream_client,
		       count(*), max(gm.sent_at)
		FROM wa_group_messages gm
		JOIN channel_participants cp ON cp.tenant_id = gm.tenant_id AND cp.id = gm.sender_channel_participant_id
		LEFT JOIN customer_accounts ca ON ca.tenant_id = cp.tenant_id AND ca.id = cp.company_account_id
		WHERE gm.tenant_id = $1 AND gm.group_id = $2
		GROUP BY cp.id, cp.display_name, cp.alias, cp.company_account_id, ca.name, cp.company_role, cp.is_downstream_client
		ORDER BY max(gm.sent_at) DESC`, tc.TenantID, id)
	if err != nil {
		http.Error(w, "failed to list participants", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []groupParticipantItem{}
	for rows.Next() {
		var it groupParticipantItem
		if err := rows.Scan(&it.ID, &it.DisplayName, &it.Alias, &it.CompanyAccountID, &it.CompanyName, &it.CompanyRole, &it.IsDownstreamClient, &it.MessageCount, &it.LastSeenAt); err != nil {
			http.Error(w, "failed to read participants", http.StatusInternalServerError)
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read participants", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) scanParticipant(ctx context.Context, q platformdb.Querier, tenantID, id uuid.UUID) (groupParticipantItem, error) {
	var it groupParticipantItem
	err := q.QueryRow(ctx, `
		SELECT cp.id, cp.display_name, cp.alias, cp.company_account_id, ca.name, cp.company_role, cp.is_downstream_client,
		       COALESCE((SELECT count(*) FROM wa_group_messages m WHERE m.tenant_id = cp.tenant_id AND m.sender_channel_participant_id = cp.id), 0),
		       COALESCE(cp.last_seen_at, cp.created_at)
		FROM channel_participants cp
		LEFT JOIN customer_accounts ca ON ca.tenant_id = cp.tenant_id AND ca.id = cp.company_account_id
		WHERE cp.tenant_id = $1 AND cp.id = $2`, tenantID, id).
		Scan(&it.ID, &it.DisplayName, &it.Alias, &it.CompanyAccountID, &it.CompanyName, &it.CompanyRole, &it.IsDownstreamClient, &it.MessageCount, &it.LastSeenAt)
	return it, err
}

// SetParticipantAlias: PUT /tenants/{tenant_id}/groups/participants/{participant_id}/details — the "nome
// aparente" override. An empty (or blank) alias CLEARS it and the provider's own display_name shows again,
// same convention as contacts.alias (internal/contacts/adapters/details.go).
func (h *Handler) SetParticipantAlias(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	id, ok := participantID(w, r)
	if !ok {
		return
	}
	var req struct {
		Alias *string `json:"alias"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Alias == nil {
		http.Error(w, "alias must be set", http.StatusBadRequest)
		return
	}
	alias := strings.TrimSpace(*req.Alias)
	if len(alias) > 200 {
		http.Error(w, "alias is too long", http.StatusUnprocessableEntity)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	tag, err := q.Exec(r.Context(), `
		UPDATE channel_participants SET alias = NULLIF($3, ''), profile_set_by = $4, profile_set_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id, alias, tc.ActorID)
	if err != nil {
		http.Error(w, "failed to save participant", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "participant not found", http.StatusNotFound)
		return
	}
	h.recordParticipant(r, tc, id, auditdomain.ActionGroupParticipantAliasSet, map[string]any{"cleared": alias == ""})
	it, err := h.scanParticipant(r.Context(), q, tc.TenantID, id)
	if err != nil {
		http.Error(w, "failed to read participant", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// SetParticipantCompany: PUT /tenants/{tenant_id}/groups/participants/{participant_id}/company — which
// company (customer_accounts) this participant represents, and their function there. Same account_id/
// directory_company_id XOR shape as LinkAccount above (resolveDirectoryAccount), and the same closed
// vocabulary as contact_account_links.relationship_type for "role" (consistency over a free-text label).
func (h *Handler) SetParticipantCompany(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	id, ok := participantID(w, r)
	if !ok {
		return
	}
	var req struct {
		AccountID          *string `json:"account_id"`
		DirectoryCompanyID *string `json:"directory_company_id"`
		Role               *string `json:"role"`
		// IsDownstreamClient (ADR-0015 addendum 3): the linked company is itself a client of the Client
		// (ex.: Uplink, cliente da 4WNET), not the Client's own staff. Defaults to false, like Role.
		IsDownstreamClient bool `json:"is_downstream_client"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	dirID := ""
	if req.DirectoryCompanyID != nil {
		dirID = strings.TrimSpace(*req.DirectoryCompanyID)
	}
	if (req.AccountID != nil) == (dirID != "") {
		http.Error(w, "account_id must be a valid id", http.StatusBadRequest)
		return
	}
	var role *string
	if req.Role != nil {
		rr := strings.TrimSpace(*req.Role)
		if !validCompanyRole[rr] {
			http.Error(w, "invalid role", http.StatusBadRequest)
			return
		}
		role = &rr
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var exists bool
	if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM channel_participants WHERE tenant_id = $1 AND id = $2)`, tc.TenantID, id).Scan(&exists); err != nil {
		http.Error(w, "failed to read participant", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "participant not found", http.StatusNotFound)
		return
	}
	var accountID uuid.UUID
	if req.AccountID != nil {
		var err error
		accountID, err = uuid.Parse(*req.AccountID)
		if err != nil {
			http.Error(w, "account_id must be a valid id", http.StatusBadRequest)
			return
		}
		var accountExists bool
		if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM customer_accounts WHERE tenant_id = $1 AND id = $2)`, tc.TenantID, accountID).Scan(&accountExists); err != nil {
			http.Error(w, "failed to read account", http.StatusInternalServerError)
			return
		}
		if !accountExists {
			http.Error(w, "account not found", http.StatusNotFound)
			return
		}
	} else {
		resolved, status, msg := h.resolveDirectoryAccount(r.Context(), tc.TenantID, dirID)
		if msg != "" {
			http.Error(w, msg, status)
			return
		}
		accountID = resolved
	}
	if _, err := q.Exec(r.Context(), `
		UPDATE channel_participants SET company_account_id = $3, company_role = $4, is_downstream_client = $5, profile_set_by = $6, profile_set_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id, accountID, role, req.IsDownstreamClient, tc.ActorID); err != nil {
		http.Error(w, "failed to save participant", http.StatusInternalServerError)
		return
	}
	h.recordParticipant(r, tc, id, auditdomain.ActionGroupParticipantCompanyLinked, map[string]any{"account_id": accountID, "is_downstream_client": req.IsDownstreamClient})
	it, err := h.scanParticipant(r.Context(), q, tc.TenantID, id)
	if err != nil {
		http.Error(w, "failed to read participant", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// UnlinkParticipantCompany: DELETE /tenants/{tenant_id}/groups/participants/{participant_id}/company.
func (h *Handler) UnlinkParticipantCompany(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	id, ok := participantID(w, r)
	if !ok {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	tag, err := q.Exec(r.Context(), `
		UPDATE channel_participants SET company_account_id = NULL, company_role = NULL, is_downstream_client = FALSE, profile_set_by = $3, profile_set_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id, tc.ActorID)
	if err != nil {
		http.Error(w, "failed to save participant", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "participant not found", http.StatusNotFound)
		return
	}
	h.recordParticipant(r, tc, id, auditdomain.ActionGroupParticipantCompanyUnlinked, map[string]any{})
	it, err := h.scanParticipant(r.Context(), q, tc.TenantID, id)
	if err != nil {
		http.Error(w, "failed to read participant", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// groupInlineSafeMime: what the browser may render itself for a group attachment — raster images and PDF
// (the only vision kinds this ADR analyzes). Nothing else is ever stored by wa_group_message_media's
// EnqueueVision gate, but the server never trusts a stored mime blindly either.
func groupInlineSafeMime(mime string) bool {
	switch strings.ToLower(strings.Split(mime, ";")[0]) {
	case "image/jpeg", "image/png", "image/webp", "image/gif", "application/pdf":
		return true
	}
	return false
}

// Media serves a group message's cleared image/PDF (ADR-0015 addendum 2) — same security posture as the 1:1
// media server: sandboxed CSP, never anything but the clean area, status-aware so the UI can say what is
// going on instead of showing a broken image.
func (h *Handler) Media(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permRead)
	if !ok {
		return
	}
	if _, ok := groupID(w, r); !ok {
		return
	}
	messageID, err := uuid.Parse(r.PathValue("message_id"))
	if err != nil {
		http.Error(w, "invalid message_id", http.StatusBadRequest)
		return
	}
	if h.mediaReader == nil {
		http.Error(w, "media retrieval not configured", http.StatusInternalServerError)
		return
	}
	media, err := h.mediaReader.Open(r.Context(), tc.TenantID, messageID)
	if errors.Is(err, mediaports.ErrMediaNotFound) {
		http.Error(w, "media not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to retrieve media", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Media-Status", media.Status)
	if media.File == nil {
		switch media.Status {
		case "pending", "quarantined":
			http.Error(w, "media is being checked", http.StatusConflict)
		case "infected", "rejected", "failed":
			http.Error(w, "media blocked for security reasons", http.StatusForbidden)
		default: // source_gone, purged
			http.Error(w, "media no longer available", http.StatusGone)
		}
		return
	}
	defer media.File.Close()
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if groupInlineSafeMime(media.Mime) {
		w.Header().Set("Content-Type", media.Mime)
		w.Header().Set("Content-Disposition", "inline")
		if media.SHA256 != "" {
			w.Header().Set("Cache-Control", "private, max-age=86400")
			w.Header().Set("ETag", `"`+media.SHA256+`"`)
		}
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment")
	}
	http.ServeContent(w, r, "", time.Time{}, io.ReadSeeker(media.File))
}
