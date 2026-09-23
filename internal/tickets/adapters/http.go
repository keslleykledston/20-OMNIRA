package adapters

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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

	status := r.URL.Query().Get("status")
	if status != "" && !validStatus[status] {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}
	priority := r.URL.Query().Get("priority")
	if priority != "" && !validPriority[priority] {
		http.Error(w, "invalid priority", http.StatusBadRequest)
		return
	}

	where := `WHERE tenant_id = $1`
	args := []any{tc.TenantID}
	if status != "" {
		args = append(args, status)
		where += ` AND status = $` + strconv.Itoa(len(args))
	}
	if priority != "" {
		args = append(args, priority)
		where += ` AND priority = $` + strconv.Itoa(len(args))
	}
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
		var item TicketItem
		if scanErr := rows.Scan(&item.ID, &item.ConversationID, &item.Status, &item.Priority, &item.Subject, &item.AssignedTo, &item.CreatedAt, &item.UpdatedAt); scanErr != nil {
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

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
