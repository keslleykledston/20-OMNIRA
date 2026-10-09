package adapters

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/distribution"
)

// PoolsHandler is the HTTP surface of the work pools (ADR-0038 phase 4). Behind the same flag as the Access panel; only an active admin
// of the hub in the path gets past the service (proven again inside every writing transaction); everything else is one uniform 404.
type PoolsHandler struct{ svc *distribution.Service }

func NewPoolsHandler(pool *pgxpool.Pool) *PoolsHandler {
	return &PoolsHandler{svc: distribution.New(pool)}
}

func writePoolError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, distribution.ErrForbidden), errors.Is(err, distribution.ErrNotFound):
		// not an admin of this hub, or the pool/instance/person is not part of it: one answer, no oracle
		httpError(w, "not found", http.StatusNotFound)
	case errors.Is(err, distribution.ErrConflict):
		httpError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, distribution.ErrInvalid):
		httpError(w, err.Error(), http.StatusUnprocessableEntity)
	default:
		log.Printf("hub pools: %v", err)
		httpError(w, "internal server error", http.StatusInternalServerError)
	}
}

func (h *PoolsHandler) List(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	items, err := h.svc.List(r.Context(), actor, hub)
	if err != nil {
		writePoolError(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

type poolRequest struct {
	Name         string `json:"name"`
	Distribution string `json:"distribution"`
}

type poolPatch struct {
	Name         *string `json:"name"`
	Distribution *string `json:"distribution"`
}

func (h *PoolsHandler) Create(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	var body poolRequest
	if !decodeWrite(w, r, &body) {
		return
	}
	p, err := h.svc.Create(r.Context(), actor, hub, body.Name, body.Distribution)
	if err != nil {
		writePoolError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(p)
}

func (h *PoolsHandler) Update(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	pool, ok := pathUUID(w, r, "pool_id")
	if !ok {
		return
	}
	var body poolPatch
	if !decodeWrite(w, r, &body) {
		return
	}
	if err := h.svc.Update(r.Context(), actor, hub, pool, body.Name, body.Distribution); err != nil {
		writePoolError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PoolsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	pool, ok := pathUUID(w, r, "pool_id")
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), actor, hub, pool); err != nil {
		writePoolError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PoolsHandler) SetMembers(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	pool, ok := pathUUID(w, r, "pool_id")
	if !ok {
		return
	}
	var body struct {
		Members []distribution.MemberSpec `json:"members"`
	}
	if !decodeWrite(w, r, &body) {
		return
	}
	if err := h.svc.SetMembers(r.Context(), actor, hub, pool, body.Members); err != nil {
		writePoolError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PoolsHandler) SetInstances(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	pool, ok := pathUUID(w, r, "pool_id")
	if !ok {
		return
	}
	var body struct {
		Instances []distribution.InstanceSpec `json:"instances"`
	}
	if !decodeWrite(w, r, &body) {
		return
	}
	if err := h.svc.SetInstances(r.Context(), actor, hub, pool, body.Instances); err != nil {
		writePoolError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
