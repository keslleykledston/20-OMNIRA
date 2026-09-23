package adapters

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Snapshot is the real V1 Dashboard contract (PRODUCT.3-B): only durable
// Postgres aggregates with an unambiguous canonical definition. Realtime
// presence (agents_online) is deliberately NOT included here — the frontend
// composes it separately from the existing Valkey-backed presence snapshot
// (PRODUCT.1), never from Postgres.
type Snapshot struct {
	OpenConversations int `json:"open_conversations"`
	OpenTickets       int `json:"open_tickets"`
	TotalContacts     int `json:"total_contacts"`
}

type Handler struct {
	pool *pgxpool.Pool
}

func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

var errPermissionDenied = errors.New("dashboard: permission denied")

// authorizeDashboardRead mirrors internal/tenancy/adapters.TeamHandler.authorize
// (also mirrored locally in internal/presence/adapters/http.go and
// internal/tickets/adapters/http.go): permission comes from the
// role→permission matrix, never a role-name comparison.
func (h *Handler) authorizeDashboardRead(r *http.Request, tc *tenancydomain.TenantContext) error {
	var ok bool
	err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key='dashboard.read')`,
		tc.TenantID, tc.ActorID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return errPermissionDenied
	}
	return nil
}

// GetSnapshot returns the TenantContext tenant's durable operational counts,
// gated by dashboard.read. Each metric is an independent, tenant-scoped
// COUNT — no cross-domain join, no time-bucketing, no trend calculation.
func (h *Handler) GetSnapshot(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	if err := h.authorizeDashboardRead(r, tc); err != nil {
		if errors.Is(err, errPermissionDenied) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var snap Snapshot

	if err := q.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM conversations WHERE tenant_id=$1 AND status='open'`, tc.TenantID,
	).Scan(&snap.OpenConversations); err != nil {
		http.Error(w, "failed to count open conversations", http.StatusInternalServerError)
		return
	}
	if err := q.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM tickets WHERE tenant_id=$1 AND status='open'`, tc.TenantID,
	).Scan(&snap.OpenTickets); err != nil {
		http.Error(w, "failed to count open tickets", http.StatusInternalServerError)
		return
	}
	if err := q.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM contacts WHERE tenant_id=$1`, tc.TenantID,
	).Scan(&snap.TotalContacts); err != nil {
		http.Error(w, "failed to count contacts", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}
