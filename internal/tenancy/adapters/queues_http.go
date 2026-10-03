package adapters

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// QueuesHandler manages the tenant's queues ("groups" in the UI): list with
// real counts, create, rename, change mode, choose the default and delete.
//
// The default queue is what routes new conversations (internal/inbox/adapters
// PostgresInboundStore.RouteNew): without one, a new conversation is not
// routed at all. That is why the API refuses to unset the default (only to
// switch it) and refuses to delete it. Permissions come from the role matrix
// (agent.read / agent.manage); RLS still scopes every row to the tenant.
type QueuesHandler struct {
	pool  *pgxpool.Pool
	audit auditports.AuditEventRepository
}

func NewQueuesHandler(p *pgxpool.Pool, a auditports.AuditEventRepository) *QueuesHandler {
	return &QueuesHandler{pool: p, audit: a}
}

const (
	queueModeManual     = "manual"
	queueModeRoundRobin = "round_robin"
	queueNameMaxRunes   = 60
)

// queueItem never carries tenant_id: the session already establishes it.
// member_count / available_count describe queue eligibility, not presence.
type queueItem struct {
	ID                    uuid.UUID `json:"id"`
	Name                  string    `json:"name"`
	Mode                  string    `json:"mode"`
	IsDefault             bool      `json:"is_default"`
	MemberCount           int       `json:"member_count"`
	AvailableCount        int       `json:"available_count"`
	OpenConversationCount int       `json:"open_conversation_count"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

const queueSelect = `SELECT q.id, q.name, q.mode, q.is_default, q.created_at, q.updated_at,
	(SELECT count(*) FROM queue_members m WHERE m.tenant_id = q.tenant_id AND m.queue_id = q.id),
	(SELECT count(*) FROM queue_members m WHERE m.tenant_id = q.tenant_id AND m.queue_id = q.id AND m.active AND m.available),
	(SELECT count(*) FROM conversations c WHERE c.tenant_id = q.tenant_id AND c.queue_id = q.id AND c.status = 'open')
	FROM queues q`

func scanQueue(row interface{ Scan(dest ...any) error }) (queueItem, error) {
	var it queueItem
	err := row.Scan(&it.ID, &it.Name, &it.Mode, &it.IsDefault, &it.CreatedAt, &it.UpdatedAt,
		&it.MemberCount, &it.AvailableCount, &it.OpenConversationCount)
	return it, err
}

func (h *QueuesHandler) authorize(w http.ResponseWriter, r *http.Request, permission string) (*tenancydomain.TenantContext, bool) {
	tc, err := (&TeamHandler{pool: h.pool}).authorize(r, permission)
	if err != nil {
		respondAuthzError(w, err)
		return nil, false
	}
	return tc, true
}

func queueIDParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("queue_id"))
	if err != nil {
		http.Error(w, "invalid queue_id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func validQueueName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(name)
	return name, n >= 1 && n <= queueNameMaxRunes
}

// nameTaken answers "is this name already used by another queue of the tenant?"
// with a plain SELECT, so the common duplicate path ends in a clean 409 instead
// of a unique-violation that aborts the session transaction. The constraint
// still guards the race between two concurrent requests.
func nameTaken(r *http.Request, q platformdb.Querier, tenantID uuid.UUID, name string, exceptID uuid.UUID) (bool, error) {
	var taken bool
	err := q.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM queues WHERE tenant_id = $1 AND name = $2 AND id <> $3)`, tenantID, name, exceptID).Scan(&taken)
	return taken, err
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func writeQueueJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *QueuesHandler) record(r *http.Request, id uuid.UUID, action auditdomain.AuditAction, meta map[string]any) {
	if h.audit == nil {
		return
	}
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, action, auditdomain.ResourceQueue, id, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	for k, v := range meta {
		ev.SetMetadata(k, v)
	}
	_ = h.audit.Store(r.Context(), ev)
}

// List returns every queue of the tenant, default first, with real counts.
func (h *QueuesHandler) List(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r, "agent.read")
	if !ok {
		return
	}
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(),
		queueSelect+` WHERE q.tenant_id = $1 ORDER BY q.is_default DESC, q.name ASC, q.id ASC`, tc.TenantID)
	if err != nil {
		http.Error(w, "failed to list queues", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []queueItem{}
	for rows.Next() {
		it, scanErr := scanQueue(rows)
		if scanErr != nil {
			http.Error(w, "failed to read queues", http.StatusInternalServerError)
			return
		}
		items = append(items, it)
	}
	if rows.Err() != nil {
		http.Error(w, "failed to read queues", http.StatusInternalServerError)
		return
	}
	writeQueueJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

type createQueueRequest struct {
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	IsDefault bool   `json:"is_default"`
}

// Create adds a queue. mode defaults to manual; is_default makes it the queue
// that routes new conversations (switching it away from the previous one).
func (h *QueuesHandler) Create(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	var req createQueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	name, valid := validQueueName(req.Name)
	if !valid {
		http.Error(w, "name must have 1 to 60 characters", http.StatusBadRequest)
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = queueModeManual
	}
	if mode != queueModeManual && mode != queueModeRoundRobin {
		http.Error(w, "mode must be manual or round_robin", http.StatusBadRequest)
		return
	}

	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	if taken, err := nameTaken(r, q, tc.TenantID, name, uuid.Nil); err != nil {
		http.Error(w, "failed to check the queue name", http.StatusInternalServerError)
		return
	} else if taken {
		http.Error(w, "a queue with this name already exists", http.StatusConflict)
		return
	}
	if req.IsDefault {
		if _, err := q.Exec(r.Context(), `UPDATE queues SET is_default = false, updated_at = now() WHERE tenant_id = $1 AND is_default`, tc.TenantID); err != nil {
			http.Error(w, "failed to switch the default queue", http.StatusInternalServerError)
			return
		}
	}
	id := uuid.New()
	if _, err := q.Exec(r.Context(),
		`INSERT INTO queues (id, tenant_id, name, mode, is_default) VALUES ($1,$2,$3,$4,$5)`,
		id, tc.TenantID, name, mode, req.IsDefault); err != nil {
		if isUniqueViolation(err, "queues_tenant_id_name_key") {
			http.Error(w, "a queue with this name already exists", http.StatusConflict)
			return
		}
		http.Error(w, "failed to create queue", http.StatusInternalServerError)
		return
	}
	it, err := scanQueue(q.QueryRow(r.Context(), queueSelect+` WHERE q.tenant_id = $1 AND q.id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "failed to read queue", http.StatusInternalServerError)
		return
	}
	h.record(r, id, auditdomain.ActionQueueCreated, map[string]any{"mode": mode, "is_default": req.IsDefault})
	writeQueueJSON(w, http.StatusCreated, it)
}

type updateQueueRequest struct {
	Name      *string `json:"name"`
	Mode      *string `json:"mode"`
	IsDefault *bool   `json:"is_default"`
}

// Update renames a queue, changes its mode and/or makes it the default. The
// default cannot be unset, only switched to another queue: with no default the
// tenant stops routing new conversations.
func (h *QueuesHandler) Update(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	id, ok := queueIDParam(w, r)
	if !ok {
		return
	}
	var req updateQueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.Name == nil && req.Mode == nil && req.IsDefault == nil {
		http.Error(w, "name, mode or is_default required", http.StatusBadRequest)
		return
	}
	var name string
	if req.Name != nil {
		n, valid := validQueueName(*req.Name)
		if !valid {
			http.Error(w, "name must have 1 to 60 characters", http.StatusBadRequest)
			return
		}
		name = n
	}
	if req.Mode != nil && *req.Mode != queueModeManual && *req.Mode != queueModeRoundRobin {
		http.Error(w, "mode must be manual or round_robin", http.StatusBadRequest)
		return
	}

	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var currentDefault bool
	if err := q.QueryRow(r.Context(), `SELECT is_default FROM queues WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id).Scan(&currentDefault); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "queue not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to read queue", http.StatusInternalServerError)
		return
	}
	if req.IsDefault != nil && !*req.IsDefault && currentDefault {
		http.Error(w, "choose another queue as the default instead of unsetting it", http.StatusConflict)
		return
	}

	if req.Name != nil {
		if taken, err := nameTaken(r, q, tc.TenantID, name, id); err != nil {
			http.Error(w, "failed to check the queue name", http.StatusInternalServerError)
			return
		} else if taken {
			http.Error(w, "a queue with this name already exists", http.StatusConflict)
			return
		}
	}

	switchedDefault := req.IsDefault != nil && *req.IsDefault && !currentDefault
	if switchedDefault {
		if _, err := q.Exec(r.Context(), `UPDATE queues SET is_default = false, updated_at = now() WHERE tenant_id = $1 AND is_default`, tc.TenantID); err != nil {
			http.Error(w, "failed to switch the default queue", http.StatusInternalServerError)
			return
		}
	}
	if _, err := q.Exec(r.Context(), `UPDATE queues SET
		name = COALESCE($3, name), mode = COALESCE($4, mode), is_default = is_default OR $5, updated_at = now()
		WHERE tenant_id = $1 AND id = $2`,
		tc.TenantID, id, nilIfEmpty(req.Name, name), req.Mode, switchedDefault); err != nil {
		if isUniqueViolation(err, "queues_tenant_id_name_key") {
			http.Error(w, "a queue with this name already exists", http.StatusConflict)
			return
		}
		http.Error(w, "failed to update queue", http.StatusInternalServerError)
		return
	}
	it, err := scanQueue(q.QueryRow(r.Context(), queueSelect+` WHERE q.tenant_id = $1 AND q.id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "failed to read queue", http.StatusInternalServerError)
		return
	}
	if switchedDefault {
		h.record(r, id, auditdomain.ActionQueueDefaultChanged, nil)
	}
	if req.Name != nil || req.Mode != nil {
		h.record(r, id, auditdomain.ActionQueueUpdated, map[string]any{"renamed": req.Name != nil, "mode_changed": req.Mode != nil})
	}
	writeQueueJSON(w, http.StatusOK, it)
}

func nilIfEmpty(p *string, v string) *string {
	if p == nil {
		return nil
	}
	return &v
}

// Delete removes a queue and its member assignments. It refuses the default
// queue (it routes new conversations) and any queue that still holds
// conversations (the database restricts it; the check gives a clear answer).
func (h *QueuesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	id, ok := queueIDParam(w, r)
	if !ok {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var isDefault bool
	var conversations, members int
	if err := q.QueryRow(r.Context(), `SELECT is_default,
		(SELECT count(*) FROM conversations c WHERE c.tenant_id = q.tenant_id AND c.queue_id = q.id),
		(SELECT count(*) FROM queue_members m WHERE m.tenant_id = q.tenant_id AND m.queue_id = q.id)
		FROM queues q WHERE q.tenant_id = $1 AND q.id = $2`, tc.TenantID, id).Scan(&isDefault, &conversations, &members); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "queue not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to read queue", http.StatusInternalServerError)
		return
	}
	if isDefault {
		http.Error(w, "the default queue cannot be deleted; choose another default first", http.StatusConflict)
		return
	}
	if conversations > 0 {
		http.Error(w, "the queue still has conversations and cannot be deleted", http.StatusConflict)
		return
	}
	if _, err := q.Exec(r.Context(), `DELETE FROM queues WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id); err != nil {
		http.Error(w, "failed to delete queue", http.StatusInternalServerError)
		return
	}
	h.record(r, id, auditdomain.ActionQueueDeleted, map[string]any{"members_removed": members})
	w.WriteHeader(http.StatusNoContent)
}
