package adapters

import (
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
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
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

const ticketColumns = `id, conversation_id, status, priority, subject, assigned_to, created_at, updated_at`

// parseTicketFilters validates status/priority against the canonical Ticket
// enums, shared by List and ExportCSV so both apply the exact same
// semantics — never a second, drifted definition.
func parseTicketFilters(r *http.Request) (status, priority string, err error) {
	status = r.URL.Query().Get("status")
	if status != "" && !validStatus[status] {
		return "", "", errors.New("invalid status")
	}
	priority = r.URL.Query().Get("priority")
	if priority != "" && !validPriority[priority] {
		return "", "", errors.New("invalid priority")
	}
	return status, priority, nil
}

// ticketFilterWhere builds the shared tenant+status+priority WHERE clause;
// callers append their own cursor/limit predicates and args afterwards.
func ticketFilterWhere(tenantID uuid.UUID, status, priority string) (string, []any) {
	where := `WHERE tenant_id = $1`
	args := []any{tenantID}
	if status != "" {
		args = append(args, status)
		where += ` AND status = $` + strconv.Itoa(len(args))
	}
	if priority != "" {
		args = append(args, priority)
		where += ` AND priority = $` + strconv.Itoa(len(args))
	}
	return where, args
}

func scanTicketItem(rows interface {
	Scan(dest ...any) error
}) (TicketItem, error) {
	var item TicketItem
	err := rows.Scan(&item.ID, &item.ConversationID, &item.Status, &item.Priority, &item.Subject, &item.AssignedTo, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

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
	if opts.Sort != "" && opts.Sort != "updated_at:desc" {
		http.Error(w, "unsupported sort", http.StatusBadRequest)
		return
	}
	cursor, err := pagination.DecodeCursor(opts.Cursor)
	if err != nil {
		http.Error(w, "invalid pagination", http.StatusBadRequest)
		return
	}

	status, priority, filterErr := parseTicketFilters(r)
	if filterErr != nil {
		http.Error(w, filterErr.Error(), http.StatusBadRequest)
		return
	}

	where, args := ticketFilterWhere(tc.TenantID, status, priority)
	if cursor != nil {
		cursorID, parseErr := uuid.Parse(cursor.ID)
		if parseErr != nil {
			http.Error(w, "invalid pagination", http.StatusBadRequest)
			return
		}
		args = append(args, cursor.Timestamp, cursorID)
		where += ` AND (updated_at, id) < ($` + strconv.Itoa(len(args)-1) + `, $` + strconv.Itoa(len(args)) + `)`
	}
	args = append(args, opts.Limit+1)
	query := `SELECT ` + ticketColumns + ` FROM tickets ` + where + ` ORDER BY updated_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args))

	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "failed to list tickets", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]TicketItem, 0, opts.Limit)
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

	hasMore := len(items) > opts.Limit
	if hasMore {
		items = items[:opts.Limit]
	}

	result := &pagination.PageResult{
		Items:   make([]interface{}, len(items)),
		HasMore: hasMore,
		Count:   len(items),
		Limit:   opts.Limit,
	}
	for i, item := range items {
		result.Items[i] = item
	}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		result.NextCursor = (&pagination.Cursor{ID: last.ID.String(), Timestamp: last.UpdatedAt}).Encode()
	}

	pagination.WritePaginationHeaders(w, result)
	writeJSON(w, result)
}

// exportMaxRows is the V1 safety ceiling (PRODUCT.5-A): no async export
// job, no object storage. A tenant whose filtered result exceeds this must
// narrow it — the export never silently truncates.
const exportMaxRows = 5000

var csvHeader = []string{"id", "conversation_id", "subject", "status", "priority", "assigned_to", "created_at", "updated_at"}

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

	status, priority, filterErr := parseTicketFilters(r)
	if filterErr != nil {
		http.Error(w, filterErr.Error(), http.StatusBadRequest)
		return
	}

	where, args := ticketFilterWhere(tc.TenantID, status, priority)
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
		})
	}
	cw.Flush()
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
