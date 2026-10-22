package updatemgr

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/provisioning"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/ratelimit"
)

// HTTPHandler é a superfície /api/v1/platform/* (ADR-0054 §2.1 "Update Manager API"). Toda rota
// exige um platform operator ativo, checado DUAS vezes de forma independente (defesa em
// profundidade, mesmo padrão de internal/hub/adapters.AdminHandler):
//  1. aqui, na sessão de RLS própria do CHAMADOR (precisa de tenancyadapters.UserSessionMiddleware
//     montada antes desta — ver RegisterPlatformReleaseHandlers em server.go);
//  2. de novo dentro do Service, que roda numa sessão de SISTEMA e nunca confia que o HTTP já
//     checou (ver o comentário de Service.authorize).
//
// Qualquer outra identidade — tenant admin, delegado de Hub, aplicação da API Headless (que nem
// chega a autenticar como authn.Principal: ver internal/external/adapters.Middleware) — recebe o
// mesmo 404 uniforme, nunca 403: a API não confirma nem nega a existência de um recurso para
// quem não pode vê-lo.
type HTTPHandler struct {
	svc  *Service
	pool *pgxpool.Pool
}

func NewHTTPHandler(svc *Service, pool *pgxpool.Pool) *HTTPHandler {
	return &HTTPHandler{svc: svc, pool: pool}
}

func httpError(w http.ResponseWriter, msg string, code int) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg + "\n"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotFound):
		// Not found and not allowed are the SAME uniform 404 — no enumeration oracle.
		httpError(w, "not found", http.StatusNotFound)
	case errors.Is(err, ErrKeyMismatch):
		httpError(w, "this Idempotency-Key was already used for a different request", http.StatusUnprocessableEntity)
	case errors.Is(err, ErrSignatureUnverifiable):
		httpError(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, ErrInvalid):
		httpError(w, err.Error(), http.StatusUnprocessableEntity)
	default:
		log.Printf("updatemgr: %v", err)
		httpError(w, "internal server error", http.StatusInternalServerError)
	}
}

// scope extrai o operador autenticado, aplica o rate limit por usuário e confirma — na sessão de
// RLS do PRÓPRIO chamador (requer tenancyadapters.UserSessionMiddleware já montada) — que é um
// platform operator ativo. Nunca aceita tenant_id/hub_id da requisição: autoridade aqui é só
// IsPlatformOperator, decidida inteiramente no servidor a partir da sessão (ADR-0054 §2.2,
// CLAUDE.md "não aceitar tenant_id do payload como autorização"). Uma negação aqui é a MESMA
// resposta 404 uniforme que um ErrForbidden do Service — nenhuma diferença observável entre "não
// é operador" e "endpoint inexistente".
func (h *HTTPHandler) scope(w http.ResponseWriter, r *http.Request) (operator uuid.UUID, ok bool) {
	principal, err := authn.FromContext(r.Context())
	if err != nil || principal.UserID == uuid.Nil {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return uuid.Nil, false
	}
	if !ratelimit.EnforceUser(w, r, principal.UserID) {
		return uuid.Nil, false
	}
	if r.URL.Query().Has("tenant_id") || r.URL.Query().Has("hub_id") {
		httpError(w, "tenant/hub selection is not accepted; this API is platform-wide, not tenant-scoped", http.StatusBadRequest)
		return uuid.Nil, false
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	isOp, err := provisioning.IsPlatformOperator(r.Context(), q, principal.UserID)
	if err != nil {
		log.Printf("updatemgr: operator check: %v", err)
		httpError(w, "internal server error", http.StatusInternalServerError)
		return uuid.Nil, false
	}
	if !isOp {
		httpError(w, "not found", http.StatusNotFound)
		return uuid.Nil, false
	}
	return principal.UserID, true
}

func parseLimitBefore(r *http.Request) (limit int, before *time.Time, ok bool) {
	limit = 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return 0, nil, false
		}
		limit = n
	}
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return 0, nil, false
		}
		before = &t
	}
	return limit, before, true
}

// --- Releases ---

func (h *HTTPHandler) ListReleases(w http.ResponseWriter, r *http.Request) {
	operator, ok := h.scope(w, r)
	if !ok {
		return
	}
	limit, before, ok := parseLimitBefore(r)
	if !ok {
		httpError(w, "invalid limit/before", http.StatusBadRequest)
		return
	}
	items, err := h.svc.ListReleases(r.Context(), operator, limit, before)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// IngestRelease recebe o manifesto CRU (já obtido de um release-build.yml real ou de um arquivo
// local — buscar o catálogo remoto assinado de verdade é BLOCKED_EXTERNAL nesta sessão, F0 §7)
// no corpo da requisição e o valida com o MESMO verificador estrito de F1
// (internal/platformrelease/domain.ParseManifest/Validate) antes de armazenar.
func (h *HTTPHandler) IngestRelease(w http.ResponseWriter, r *http.Request) {
	operator, ok := h.scope(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpError(w, "Idempotency-Key header is required", http.StatusBadRequest)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		httpError(w, "request body too large or unreadable", http.StatusBadRequest)
		return
	}
	rel, replayed, err := h.svc.IngestRelease(r.Context(), operator, key, raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		status = http.StatusOK
	}
	writeJSON(w, status, rel)
}

// --- Installation ---

func (h *HTTPHandler) GetInstallation(w http.ResponseWriter, r *http.Request) {
	operator, ok := h.scope(w, r)
	if !ok {
		return
	}
	inst, err := h.svc.GetInstallation(r.Context(), operator)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

type setInstallationVersionRequest struct {
	Version   string `json:"version"`
	ReleaseID string `json:"release_id"`
}

// SetInstallationVersion registra a versão/release atualmente instalada. Necessário porque
// nada nesta fase (F6) popula isso a partir de um deploy real — sem este endpoint, o preflight
// (fail-closed) bloquearia toda release para sempre (ver Service.checkCompatibility).
func (h *HTTPHandler) SetInstallationVersion(w http.ResponseWriter, r *http.Request) {
	operator, ok := h.scope(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpError(w, "Idempotency-Key header is required", http.StatusBadRequest)
		return
	}
	var body setInstallationVersionRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.More() {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	inst, err := h.svc.SetInstallationVersion(r.Context(), operator, key, body.Version, body.ReleaseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

// --- Backup evidence ---

type recordBackupRequest struct {
	Kind      string         `json:"kind"`
	TakenAt   time.Time      `json:"taken_at"`
	Verified  bool           `json:"verified"`
	SizeBytes *int64         `json:"size_bytes"`
	SHA256Hex string         `json:"sha256_hex"`
	Detail    map[string]any `json:"detail"`
}

func (h *HTTPHandler) RecordBackupEvidence(w http.ResponseWriter, r *http.Request) {
	operator, ok := h.scope(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpError(w, "Idempotency-Key header is required", http.StatusBadRequest)
		return
	}
	var body recordBackupRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.More() {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	rec, replayed, err := h.svc.RecordBackupEvidence(r.Context(), operator, key, body.Kind, body.TakenAt, body.Verified, body.SizeBytes, body.SHA256Hex, body.Detail)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		status = http.StatusOK
	}
	writeJSON(w, status, rec)
}

// --- Revocation (F9, release/policies/signing.yaml "revocation") ---

type revokeReleaseRequest struct {
	Reason string `json:"reason"`
}

func (h *HTTPHandler) RevokeRelease(w http.ResponseWriter, r *http.Request) {
	operator, ok := h.scope(w, r)
	if !ok {
		return
	}
	releaseID := r.PathValue("release_id")
	if releaseID == "" {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpError(w, "Idempotency-Key header is required", http.StatusBadRequest)
		return
	}
	var body revokeReleaseRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.More() {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	rec, replayed, err := h.svc.RevokeRelease(r.Context(), operator, key, releaseID, body.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		status = http.StatusOK
	}
	writeJSON(w, status, rec)
}

// --- Plans ---

type createPlanRequest struct {
	ReleaseID string `json:"release_id"`
}

func (h *HTTPHandler) CreatePlan(w http.ResponseWriter, r *http.Request) {
	operator, ok := h.scope(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpError(w, "Idempotency-Key header is required", http.StatusBadRequest)
		return
	}
	var body createPlanRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.More() {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	plan, replayed, err := h.svc.CreatePlan(r.Context(), operator, key, body.ReleaseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		status = http.StatusOK
	}
	writeJSON(w, status, plan)
}

func (h *HTTPHandler) GetPlan(w http.ResponseWriter, r *http.Request) {
	operator, ok := h.scope(w, r)
	if !ok {
		return
	}
	planID, err := uuid.Parse(r.PathValue("plan_id"))
	if err != nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	plan, err := h.svc.GetPlan(r.Context(), operator, planID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// --- Runs ---

type startRunRequest struct {
	PlanID uuid.UUID `json:"plan_id"`
}

func (h *HTTPHandler) StartRun(w http.ResponseWriter, r *http.Request) {
	operator, ok := h.scope(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpError(w, "Idempotency-Key header is required", http.StatusBadRequest)
		return
	}
	var body startRunRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.More() || body.PlanID == uuid.Nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	run, err := h.svc.StartRun(r.Context(), operator, body.PlanID, key)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (h *HTTPHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	operator, ok := h.scope(w, r)
	if !ok {
		return
	}
	limit, before, ok := parseLimitBefore(r)
	if !ok {
		httpError(w, "invalid limit/before", http.StatusBadRequest)
		return
	}
	items, err := h.svc.ListRuns(r.Context(), operator, limit, before)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *HTTPHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	operator, ok := h.scope(w, r)
	if !ok {
		return
	}
	run, err := h.svc.GetRun(r.Context(), operator, r.PathValue("run_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// ListEvents é a trilha de progresso (especificação: SSE ou polling) — polling nesta fase: um
// cursor `after` (seq) opaco só no sentido de que o cliente nunca precisa interpretá-lo, apenas
// devolvê-lo. SSE fica documentado como pendência (ver IMPLEMENTATION-STATUS.md F6).
func (h *HTTPHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	operator, ok := h.scope(w, r)
	if !ok {
		return
	}
	// -1, nunca 0: o PRIMEIRO evento de um run (StartRun grava "run.locked" como seq=0) é um
	// evento real, não um sentinela — achado real ao testar: "after" ausente tinha o
	// zero-value de int64 (0) e "seq > 0" excluía esse primeiro evento sempre, mesmo na
	// primeira chamada de um cliente que nunca viu nenhum evento ainda.
	after := int64(-1)
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			httpError(w, "invalid after", http.StatusBadRequest)
			return
		}
		after = n
	}
	items, err := h.svc.ListEvents(r.Context(), operator, r.PathValue("run_id"), after)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
