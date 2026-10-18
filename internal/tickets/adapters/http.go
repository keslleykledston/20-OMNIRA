package adapters

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketdomain "github.com/omnira/omnira/internal/tickets/domain"
)

// TicketItem is the read model served to the browser: only fields already
// present on the canonical Ticket (internal/tickets/domain), no cross-domain
// enrichment. tenant_id is omitted — the session already establishes it.
type TicketItem struct {
	ID             uuid.UUID  `json:"id"`
	ConversationID uuid.UUID  `json:"conversation_id"`
	Subject        string     `json:"subject"`
	Status         string     `json:"status"`
	Priority       string     `json:"priority"`
	AssignedTo     *uuid.UUID `json:"assigned_to"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`

	// PRODUCT.6-D (ADR-0013): external ERP ticket projection metadata, added
	// additively. All nil/null for the legacy/local-only tickets that are
	// the only kind that exists today — never omitted from the JSON shape
	// (encoding/json renders a nil *string as null, not an absent key), so
	// existing clients see the new keys with null values rather than a
	// silently different response shape.
	Provider            *string    `json:"provider"`
	ExternalTicketID    *string    `json:"external_ticket_id"`
	ExternalStatus      *string    `json:"external_status"`
	ExternalStatusLabel *string    `json:"external_status_label"`
	SyncStatus          *string    `json:"sync_status"`
	LastSyncedAt        *time.Time `json:"last_synced_at"`
}

type Handler struct {
	pool *pgxpool.Pool
}

func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

var errPermissionDenied = errors.New("tickets: permission denied")

// authorizeTicketRead mirrors internal/tenancy/adapters.TeamHandler.authorize
// (also mirrored locally in internal/presence/adapters/http.go): permission
// comes from the role→permission matrix, never a role-name comparison.
func (h *Handler) authorizeTicketRead(r *http.Request, tc *tenancydomain.TenantContext) error {
	var ok bool
	err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key='ticket.read')`,
		tc.TenantID, tc.ActorID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return errPermissionDenied
	}
	return nil
}

var validStatus = map[string]bool{"open": true, "in_progress": true, "waiting": true, "resolved": true, "closed": true}
var validPriority = map[string]bool{"critical": true, "high": true, "medium": true, "low": true}

const ticketColumns = `id, conversation_id, status, priority, subject, assigned_to, created_at, updated_at,
	provider, external_ticket_id, external_status, external_status_label, sync_status, last_synced_at`

// parseTicketFilters validates status/priority against the canonical Ticket
// enums, shared by List and ExportCSV so both apply the exact same
// semantics — never a second, drifted definition. external_ticket_id
// (PRODUCT.6-O2D) is an opaque provider identifier, like target_status in
// the status-mutation contract — no enum, no normalization, exact match
// only against the local projection (never a provider lookup).
func parseTicketFilters(r *http.Request) (status, priority, externalTicketID string, err error) {
	status = r.URL.Query().Get("status")
	if status != "" && !validStatus[status] {
		return "", "", "", errors.New("invalid status")
	}
	priority = r.URL.Query().Get("priority")
	if priority != "" && !validPriority[priority] {
		return "", "", "", errors.New("invalid priority")
	}
	externalTicketID = r.URL.Query().Get("external_ticket_id")
	return status, priority, externalTicketID, nil
}

// ticketFilterWhere builds the shared tenant+status+priority+external_ticket_id
// WHERE clause; callers append their own cursor/limit predicates and args
// afterwards. An external_ticket_id predicate is an ordinary SQL equality —
// it never matches a NULL external_ticket_id row (legacy/local-only
// tickets), by plain SQL NULL-comparison semantics, with no extra code.
func ticketFilterWhere(tenantID uuid.UUID, status, priority, externalTicketID string) (string, []any) {
	// Placeholder tickets (see domain.RealTicketSQL) are not shown to operators.
	where := `WHERE tenant_id = $1 AND ` + ticketdomain.RealTicketSQL("")
	args := []any{tenantID}
	if status != "" {
		args = append(args, status)
		where += ` AND status = $` + strconv.Itoa(len(args))
	}
	if priority != "" {
		args = append(args, priority)
		where += ` AND priority = $` + strconv.Itoa(len(args))
	}
	if externalTicketID != "" {
		args = append(args, externalTicketID)
		where += ` AND external_ticket_id = $` + strconv.Itoa(len(args))
	}
	return where, args
}

// scopeToSectors narrows a ticket query to the conversations the actor may see when the company's sector scope is on (ADR-0043 S3). It adds the predicate with its own
// bind arguments, so it can be called right after the base where-clause is built.
func scopeToSectors(ctx context.Context, q platformdb.Querier, tc *tenancydomain.TenantContext, where string, args []any) (string, []any, error) {
	restricted, err := tenancyadapters.SectorScopeRestricts(ctx, q, tc)
	if err != nil || !restricted {
		return where, args, err
	}
	args = append(args, tc.ActorID)
	return where + ` AND conversation_in_sector_scope(tenant_id, $` + strconv.Itoa(len(args)) + `, conversation_id)`, args, nil
}

func scanTicketItem(rows interface {
	Scan(dest ...any) error
}) (TicketItem, error) {
	var item TicketItem
	err := rows.Scan(&item.ID, &item.ConversationID, &item.Status, &item.Priority, &item.Subject, &item.AssignedTo, &item.CreatedAt, &item.UpdatedAt,
		&item.Provider, &item.ExternalTicketID, &item.ExternalStatus, &item.ExternalStatusLabel, &item.SyncStatus, &item.LastSyncedAt)
	return item, err
}

// ticketSorts are the orderings the Tickets screen offers besides the default (newest activity first). The SQL is ours, never the caller's.
var ticketSorts = func() map[string]struct {
	col  pagination.SortColumn
	desc bool
} {
	type spec = struct {
		col  pagination.SortColumn
		desc bool
	}
	priorityRank := pagination.SortColumn{Expr: "(CASE priority WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'medium' THEN 2 ELSE 1 END)", Kind: pagination.SortInt}
	updated := pagination.SortColumn{Expr: "updated_at", Kind: pagination.SortTime}
	subject := pagination.SortColumn{Expr: "lower(subject)", Kind: pagination.SortText}
	return map[string]spec{
		"updated_at:asc": {updated, false},
		"priority:desc":  {priorityRank, true}, // most urgent first
		"priority:asc":   {priorityRank, false},
		"subject:asc":    {subject, false},
		"subject:desc":   {subject, true},
	}
}()

// withExtra lets scanTicketItem read the usual columns and then one more (the value the list is ordered by).
type withExtra struct {
	rows  interface{ Scan(dest ...any) error }
	extra *any
}

func (w withExtra) Scan(dest ...any) error { return w.rows.Scan(append(dest, w.extra)...) }

// List returns one page of the TenantContext tenant's tickets, newest
// activity first, gated by ticket.read. Ordering matches the leading
// (tenant_id, ...) columns tickets already has via
// idx_tickets_tenant_status_updated; an unfiltered global scan across every
// status is accepted for this first slice (small tenant ticket volumes,
// same trade-off DESIGN.3 made for contacts) rather than adding a new index.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	if err := h.authorizeTicketRead(r, tc); err != nil {
		if errors.Is(err, errPermissionDenied) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	opts := pagination.ParsePageOptionsFromQuery(r)
	// The default (newest activity first) keeps its own cursor; every other ordering the screen offers carries the sorted value in a key cursor.
	sortSpec, sorted := ticketSorts[opts.Sort]
	if opts.Sort != "" && opts.Sort != "updated_at:desc" && !sorted {
		http.Error(w, "unsupported sort", http.StatusBadRequest)
		return
	}
	var cursor *pagination.Cursor
	var keyCursor *pagination.KeyCursor
	if sorted {
		keyCursor, err = pagination.DecodeKeyCursor(opts.Cursor, opts.Sort)
	} else {
		cursor, err = pagination.DecodeCursor(opts.Cursor)
	}
	if err != nil {
		http.Error(w, "invalid pagination", http.StatusBadRequest)
		return
	}

	status, priority, externalTicketID, filterErr := parseTicketFilters(r)
	if filterErr != nil {
		http.Error(w, filterErr.Error(), http.StatusBadRequest)
		return
	}

	where, args := ticketFilterWhere(tc.TenantID, status, priority, externalTicketID)
	where, args, err = scopeToSectors(r.Context(), platformdb.QuerierFromContext(r.Context(), h.pool), tc, where, args)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	// how many tickets match the filters in all pages (the result counter); counted before the cursor narrows the rows
	countWhere, countArgs := where, append([]any(nil), args...)
	if cursor != nil {
		cursorID, parseErr := uuid.Parse(cursor.ID)
		if parseErr != nil {
			http.Error(w, "invalid pagination", http.StatusBadRequest)
			return
		}
		args = append(args, cursor.Timestamp, cursorID)
		where += ` AND (updated_at, id) < ($` + strconv.Itoa(len(args)-1) + `, $` + strconv.Itoa(len(args)) + `)`
	}
	orderBy := "updated_at DESC, id DESC"
	selectKey := ""
	if sorted {
		dir := "ASC"
		if sortSpec.desc {
			dir = "DESC"
		}
		orderBy = sortSpec.col.Expr + " " + dir + ", id " + dir
		selectKey = ", " + sortSpec.col.Expr
		if keyCursor != nil {
			pred, perr := pagination.KeysetWhere(sortSpec.col, "id", sortSpec.desc, keyCursor, func(v any) string {
				args = append(args, v)
				return "$" + strconv.Itoa(len(args))
			})
			if perr != nil {
				http.Error(w, "invalid pagination", http.StatusBadRequest)
				return
			}
			where += " AND " + pred
		}
	}
	args = append(args, opts.Limit+1)
	query := `SELECT ` + ticketColumns + selectKey + ` FROM tickets ` + where + ` ORDER BY ` + orderBy + ` LIMIT $` + strconv.Itoa(len(args))

	dbq := platformdb.QuerierFromContext(r.Context(), h.pool)
	// The result counter is counted once, with the first page: asking for the next pages does not recount (the screen keeps the first page's number).
	var total *int
	if cursor == nil && keyCursor == nil {
		var n int
		if err := dbq.QueryRow(r.Context(), `SELECT count(*) FROM tickets `+countWhere, countArgs...).Scan(&n); err != nil {
			http.Error(w, "failed to list tickets", http.StatusInternalServerError)
			return
		}
		total = &n
	}
	rows, err := dbq.Query(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "failed to list tickets", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]TicketItem, 0, opts.Limit)
	var keys []any // the sorted value of each row, for the next cursor
	for rows.Next() {
		var item TicketItem
		var scanErr error
		if sorted {
			var sortValue any
			item, scanErr = scanTicketItem(withExtra{rows, &sortValue})
			keys = append(keys, sortValue)
		} else {
			item, scanErr = scanTicketItem(rows)
		}
		if scanErr != nil {
			http.Error(w, "failed to read tickets", http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read tickets", http.StatusInternalServerError)
		return
	}

	hasMore := len(items) > opts.Limit
	if hasMore {
		items = items[:opts.Limit]
	}

	result := &pagination.PageResult{
		Items:   make([]interface{}, len(items)),
		HasMore: hasMore,
		Count:   len(items),
		Limit:   opts.Limit,
		Total:   total,
	}
	for i, item := range items {
		result.Items[i] = item
	}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		if sorted {
			key, kerr := sortSpec.col.KeyText(keys[len(items)-1])
			if kerr != nil {
				http.Error(w, "failed to read tickets", http.StatusInternalServerError)
				return
			}
			result.NextCursor = (&pagination.KeyCursor{Sort: opts.Sort, Key: key, ID: last.ID.String()}).Encode()
		} else {
			result.NextCursor = (&pagination.Cursor{ID: last.ID.String(), Timestamp: last.UpdatedAt}).Encode()
		}
	}

	pagination.WritePaginationHeaders(w, result)
	writeJSON(w, result)
}

// exportMaxRows is the V1 safety ceiling (PRODUCT.5-A): no async export
// job, no object storage. A tenant whose filtered result exceeds this must
// narrow it — the export never silently truncates.
const exportMaxRows = 5000

// PRODUCT.6-D (ADR-0013): projection columns appended after the existing
// canonical set — never inserted in the middle — so a spreadsheet an
// operator already has saved against the old header still aligns on the
// columns it knows.
var csvHeader = []string{
	"id", "conversation_id", "subject", "status", "priority", "assigned_to", "created_at", "updated_at",
	"provider", "external_ticket_id", "external_status", "external_status_label", "sync_status", "last_synced_at",
}

// sanitizeCSVField neutralizes spreadsheet formula injection (OWASP CSV
// injection guidance) for user/provider-controlled text. The check looks at
// the first MEANINGFUL character — skipping leading whitespace/control
// characters a spreadsheet application would itself skip before evaluating
// a cell as a formula (space, tab, CR, LF) — not merely byte 0, so
// " =1+1", "\t=1+1" and "\r=1+1" are caught the same as "=1+1". When that
// character is one of = + - @, a leading apostrophe is prepended to the
// ORIGINAL (untrimmed) value so spreadsheet applications render the whole
// cell as text. Stored data is never mutated; this is serialization-only.
func sanitizeCSVField(s string) string {
	trimmed := strings.TrimLeft(s, " \t\r\n")
	if trimmed == "" {
		return s
	}
	switch trimmed[0] {
	case '=', '+', '-', '@':
		return "'" + s
	default:
		return s
	}
}

// ExportCSV streams the TenantContext tenant's full matching ticket set
// (up to exportMaxRows) as CSV, gated by ticket.read — the same permission
// as List, since export serves the exact same authorized data. Ordering
// and filter semantics are identical to List (shared helpers); export
// scope is the full filtered result, never a single pagination page.
func (h *Handler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	if err := h.authorizeTicketRead(r, tc); err != nil {
		if errors.Is(err, errPermissionDenied) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	status, priority, externalTicketID, filterErr := parseTicketFilters(r)
	if filterErr != nil {
		http.Error(w, filterErr.Error(), http.StatusBadRequest)
		return
	}

	where, args := ticketFilterWhere(tc.TenantID, status, priority, externalTicketID)
	where, args, err = scopeToSectors(r.Context(), platformdb.QuerierFromContext(r.Context(), h.pool), tc, where, args)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	args = append(args, exportMaxRows+1)
	query := `SELECT ` + ticketColumns + ` FROM tickets ` + where + ` ORDER BY updated_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args))

	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "failed to export tickets", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]TicketItem, 0, exportMaxRows)
	for rows.Next() {
		item, scanErr := scanTicketItem(rows)
		if scanErr != nil {
			http.Error(w, "failed to read tickets", http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read tickets", http.StatusInternalServerError)
		return
	}

	if len(items) > exportMaxRows {
		http.Error(w, "export exceeds 5000 tickets. Narrow the result using filters.", http.StatusRequestEntityTooLarge)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="tickets.csv"`)
	w.Header().Set("Cache-Control", "no-store")

	cw := csv.NewWriter(w)
	_ = cw.Write(csvHeader)
	for _, item := range items {
		assignedTo := ""
		if item.AssignedTo != nil {
			assignedTo = item.AssignedTo.String()
		}
		_ = cw.Write([]string{
			item.ID.String(),
			item.ConversationID.String(),
			sanitizeCSVField(item.Subject),
			item.Status,
			item.Priority,
			assignedTo,
			item.CreatedAt.UTC().Format(time.RFC3339),
			item.UpdatedAt.UTC().Format(time.RFC3339),
			// Legacy/local-only tickets (the only kind that exists today)
			// export these six as empty cells — never a fabricated value.
			// Provider-supplied text is sanitized the same as subject: it
			// comes from an external system, same trust level as operator
			// input.
			sanitizeCSVField(strPtrOr(item.Provider, "")),
			sanitizeCSVField(strPtrOr(item.ExternalTicketID, "")),
			sanitizeCSVField(strPtrOr(item.ExternalStatus, "")),
			sanitizeCSVField(strPtrOr(item.ExternalStatusLabel, "")),
			sanitizeCSVField(strPtrOr(item.SyncStatus, "")),
			timePtrOrEmpty(item.LastSyncedAt),
		})
	}
	cw.Flush()
}

func strPtrOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

func timePtrOrEmpty(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
