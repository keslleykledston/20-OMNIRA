package adapters

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/omnira/omnira/internal/platform/db"
	presenceapplication "github.com/omnira/omnira/internal/presence/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

var errPermissionDenied = errors.New("presence: permission denied")

// Handler serves the self-scoped heartbeat and the supervisor snapshot.
type Handler struct {
	pool *pgxpool.Pool
	svc  *presenceapplication.Service
}

func NewHandler(pool *pgxpool.Pool, svc *presenceapplication.Service) *Handler {
	return &Handler{pool: pool, svc: svc}
}

type heartbeatRequest struct {
	SessionID string `json:"session_id"`
}

// resolveSelfAgentProfile derives the caller's own active AgentProfile from
// the authenticated tenant context — never from client input. Mirrors the
// eligibility check routing already performs (active Membership + active
// AgentProfile), see internal/routing/adapters/postgres.go.
func (h *Handler) resolveSelfAgentProfile(r *http.Request, tc *tenancydomain.TenantContext) (uuid.UUID, error) {
	var agentProfileID uuid.UUID
	err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT ap.id FROM agent_profiles ap
		JOIN memberships m ON m.tenant_id=ap.tenant_id AND m.id=ap.membership_id
		WHERE ap.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND ap.status='active'`,
		tc.TenantID, tc.ActorID).Scan(&agentProfileID)
	return agentProfileID, err
}

// Heartbeat is self-scoped (ADR-0010 §7, §3): tenant_id comes from the URL,
// already verified against real membership by AuthorizationMiddleware like
// every other tenant route in this API — the same convention the rest of the
// codebase uses (see internal/tenancy/adapters/http.go). Agent identity is
// resolved server-side from the authenticated actor. The client supplies
// only its own ephemeral tab/session identifier: never tenant_id,
// membership_id, agent_profile_id or user_id.
func (h *Handler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusUnauthorized)
		return
	}
	var in heartbeatRequest
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	sessionID, err := uuid.Parse(in.SessionID)
	if err != nil {
		http.Error(w, "session_id must be a valid uuid", http.StatusBadRequest)
		return
	}
	agentProfileID, err := h.resolveSelfAgentProfile(r, tc)
	if errors.Is(err, pgx.ErrNoRows) {
		// Inactive Membership or disabled/absent AgentProfile: heartbeat denied
		// (ADR-0010 §16 test matrix), not silently accepted as presence.
		http.Error(w, "no active agent profile", http.StatusForbidden)
		return
	}
	if err != nil {
		http.Error(w, "failed to resolve agent profile", http.StatusInternalServerError)
		return
	}
	if err := h.svc.Heartbeat(r.Context(), tc.TenantID, agentProfileID, sessionID.String()); err != nil {
		// Valkey unavailable: fail explicitly/observably (ADR-0010 §13). The
		// caller must not infer presence from a failed heartbeat.
		http.Error(w, "heartbeat unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// authorizeAgentRead mirrors internal/tenancy/adapters.TeamHandler.authorize:
// permission comes from the role→permission matrix, never a role-name
// comparison (ADR-0010 §15).
func (h *Handler) authorizeAgentRead(r *http.Request, tc *tenancydomain.TenantContext) error {
	var ok bool
	err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key='agent.read')`,
		tc.TenantID, tc.ActorID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return errPermissionDenied
	}
	return nil
}

// Snapshot is the supervisor's initial read: the current online set, derived
// from Valkey, never stale Postgres data (ADR-0010 §11).
func (h *Handler) Snapshot(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusUnauthorized)
		return
	}
	if err := h.authorizeAgentRead(r, tc); errors.Is(err, errPermissionDenied) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	} else if err != nil {
		http.Error(w, "failed to check authorization", http.StatusInternalServerError)
		return
	}
	ids, err := h.svc.Snapshot(r.Context(), tc.TenantID)
	if err != nil {
		http.Error(w, "presence snapshot unavailable", http.StatusServiceUnavailable)
		return
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"online_agent_profile_ids": out})
}
