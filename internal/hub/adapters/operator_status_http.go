package adapters

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/hub/provisioning"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/ratelimit"
)

// OperatorStatusHandler answers only "is the signed-in person a platform operator" (ADR-0038),
// independent of any OTHER platform-wide feature. The admin console's nav guard
// (web/src/hooks/usePlatformOperator.ts) and every screen that needs this fact use THIS endpoint —
// never a feature-specific one (e.g. Update Manager's /platform/installation), which silently stops
// answering for EVERY operator the day that other feature happens to be disabled (found
// 2026-11-15: a real, already-granted operator saw "Acesso restrito" on this production host
// because OMNIRA_PLATFORM_RELEASE_API_ENABLED was never turned on, so /platform/installation's
// route was never mounted at all — a raw Go 404 indistinguishable, client-side, from "not an
// operator"). Platform operator authority does not depend on Update Manager, Hub, or any other
// feature's own flag.
type OperatorStatusHandler struct {
	pool *pgxpool.Pool
}

func NewOperatorStatusHandler(pool *pgxpool.Pool) *OperatorStatusHandler {
	return &OperatorStatusHandler{pool: pool}
}

type operatorStatusResponse struct {
	IsOperator bool `json:"is_operator"`
}

// Get — same uniform-404 convention as every other platform-wide handler (updatemgr/http.go
// `scope`, groupsadapters.SLAAlertFeatureFlagHandler, ContextIntelligenceV2FeatureFlagHandler): a
// denial is indistinguishable from "endpoint doesn't exist", no enumeration oracle.
func (h *OperatorStatusHandler) Get(w http.ResponseWriter, r *http.Request) {
	principal, err := authn.FromContext(r.Context())
	if err != nil || principal.UserID == uuid.Nil {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !ratelimit.EnforceUser(w, r, principal.UserID) {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	isOp, err := provisioning.IsPlatformOperator(r.Context(), q, principal.UserID)
	if err != nil {
		httpError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !isOp {
		httpError(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, operatorStatusResponse{IsOperator: true})
}
