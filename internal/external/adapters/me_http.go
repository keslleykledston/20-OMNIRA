package adapters

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/external/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Features says which parts of the external API this build serves, so a client can see what is wired instead of discovering 404s.
type Features struct {
	Read   bool `json:"read_api"`
	Events bool `json:"events_api"`
	Write  bool `json:"write_api"`
}

// MeHandler answers GET /api/v1/external/me: who the credential is, what it may do, and its limits. It reads nothing about conversations.
type MeHandler struct {
	pool     *pgxpool.Pool
	features Features
	now      func() time.Time
}

func NewMeHandler(pool *pgxpool.Pool, f Features) *MeHandler {
	return &MeHandler{pool: pool, features: f, now: time.Now}
}

type meResponse struct {
	APIVersion   string `json:"api_version"`
	Installation struct {
		ID     uuid.UUID `json:"id"`
		Name   string    `json:"name"`
		Status string    `json:"status"`
	} `json:"installation"`
	Tenant struct {
		ID uuid.UUID `json:"id"` // informative only: the credential decides the tenant, a request can never choose it
	} `json:"tenant"`
	Scopes []string `json:"scopes"`
	Limits struct {
		RatePerMin        int  `json:"rate_limit_per_min"`
		LeaseSeconds      *int `json:"lease_seconds"`
		AllowOutsideHours bool `json:"allow_outside_hours"`
	} `json:"limits"`
	Allowlist struct {
		QueueIDs   []uuid.UUID `json:"queue_ids"`
		ChannelIDs []uuid.UUID `json:"channel_ids"`
	} `json:"allowlist"`
	Features   Features  `json:"features"`
	ServerTime time.Time `json:"server_time"`
}

func (h *MeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required", "")
		return
	}
	var out meResponse
	err := platformdb.WithSystemTenantSession(r.Context(), h.pool, p.TenantID, func(ctx2 context.Context) error {
		al, err := LoadAllowlists(ctx2, platformdb.QuerierFromContext(ctx2, h.pool), p)
		out.Allowlist.QueueIDs, out.Allowlist.ChannelIDs = al.QueueIDs, al.ChannelIDs
		return err
	})
	if err != nil {
		externalMetric.Inc("error")
		WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Temporarily unavailable", "Try again shortly.")
		return
	}
	out.APIVersion = "v1"
	out.Installation.ID, out.Installation.Name, out.Installation.Status = p.InstallationID, p.Name, string(domain.StatusActive)
	out.Tenant.ID = p.TenantID
	out.Scopes = domain.ScopeStrings(p.Scopes)
	out.Limits.RatePerMin, out.Limits.LeaseSeconds, out.Limits.AllowOutsideHours = p.RatePerMin, p.LeaseSeconds, p.AllowOutsideHours
	out.Features = h.features
	out.ServerTime = h.now().UTC()
	externalMetric.Inc("served")
	WriteJSON(w, r, http.StatusOK, out)
}
