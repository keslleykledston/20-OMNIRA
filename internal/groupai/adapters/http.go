package adapters

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	"github.com/omnira/omnira/internal/groupai/application"
	"github.com/omnira/omnira/internal/groupai/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	servicectxdomain "github.com/omnira/omnira/internal/servicectx/domain"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

const (
	permAIRead  = "group.ai.read"
	permAIWrite = "group.ai.manage"
	permImport  = "group.import"
	permErase   = "group.memory.erase"
)

// Handler serves the group copilot APIs (ADR-0050). Authorization is the role->permission matrix of an ACTIVE membership (never a role name)
// on top of RLS, exactly like the group APIs of ADR-0015. Every response carries ids, enums and bounded text the caller is allowed to see.
type Handler struct {
	pool  *pgxpool.Pool
	svc   *application.Service
	audit auditports.AuditEventRepository
}

func NewHandler(pool *pgxpool.Pool, svc *application.Service, audit auditports.AuditEventRepository) *Handler {
	return &Handler{pool: pool, svc: svc, audit: audit}
}

// Routes registers every route of the module on r (a ServeMux or a recorder), wrapped by wrap (authentication + the tenant session). The
// contract test reads the same list, so a route and its documentation cannot drift apart.
func (h *Handler) Routes(r interface{ Handle(string, http.Handler) }, wrap func(http.HandlerFunc) http.Handler) {
	const base = "/api/v1/tenants/{tenant_id}/groups/{group_id}/ai"
	for _, rt := range []struct {
		pattern string
		fn      http.HandlerFunc
	}{
		{"PATCH " + base, h.SetAI},
		{"GET " + base + "/memory", h.ListMemory},
		{"GET " + base + "/memory/search", h.SearchMemory},
		{"POST " + base + "/memory", h.CreateItem},
		{"PATCH " + base + "/memory/{item_id}", h.PatchItem},
		{"DELETE " + base + "/memory/{item_id}", h.DeleteItem},
		{"DELETE " + base + "/memory", h.EraseMemory},
		{"POST " + base + "/analyze", h.Analyze},
		{"POST " + base + "/consolidate", h.Consolidate},
		{"GET " + base + "/imports", h.ListImports},
		{"POST " + base + "/imports", h.UploadImport},
		{"GET " + base + "/imports/{import_id}", h.GetImport},
		{"POST " + base + "/imports/{import_id}/discard", h.DiscardImport},
		{"POST " + base + "/imports/{import_id}/reprocess", h.ReprocessImport},
		{"GET " + base + "/proposals", h.ListProposals},
		{"POST " + base + "/proposals", h.DraftProposal},
		{"PATCH " + base + "/proposals/{proposal_id}", h.EditProposal},
		{"POST " + base + "/proposals/{proposal_id}/approve", h.ApproveProposal},
		{"POST " + base + "/proposals/{proposal_id}/reject", h.RejectProposal},
	} {
		r.Handle(rt.pattern, wrap(rt.fn))
	}
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request, permission string) (*tenancydomain.TenantContext, bool) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	var ok bool
	if err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`,
		tc.TenantID, tc.ActorID, permission).Scan(&ok); err != nil {
		http.Error(w, "failed to check permission", http.StatusInternalServerError)
		return nil, false
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return tc, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		http.Error(w, "invalid "+name, http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) record(r *http.Request, tc *tenancydomain.TenantContext, resource uuid.UUID, action auditdomain.AuditAction, meta map[string]any) {
	if h.audit == nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, action, auditdomain.ResourceGroup, resource, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	for k, v := range meta {
		ev.SetMetadata(k, v)
	}
	_ = h.audit.Store(r.Context(), ev)
}

// writeErr maps what the use cases return to an HTTP answer. The text is ours; nothing of the stored content is ever echoed in an error.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrNotFound), errors.Is(err, servicectxdomain.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, application.ErrDisabled):
		http.Error(w, "the group copilot is disabled", http.StatusNotFound)
	case errors.Is(err, application.ErrGroupAIOff):
		http.Error(w, "the AI is not enabled for this group", http.StatusConflict)
	case errors.Is(err, application.ErrConflict), errors.Is(err, application.ErrDuplicateImport), errors.Is(err, application.ErrDuplicateItem),
		errors.Is(err, application.ErrProposalDecided), errors.Is(err, application.ErrNothingToReprocess), errors.Is(err, domain.ErrCoverageIncomplete),
		errors.Is(err, domain.ErrAlreadyDiscarded):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, application.ErrForbiddenCreation), errors.Is(err, servicectxdomain.ErrForbidden):
		http.Error(w, "forbidden: you lack the permission to create this record", http.StatusForbidden)
	case errors.Is(err, domain.ErrImportTooLarge):
		http.Error(w, "the file is too large", http.StatusRequestEntityTooLarge)
	case errors.Is(err, domain.ErrImportMalformed), errors.Is(err, domain.ErrImportZip), errors.Is(err, application.ErrInfected):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrInvalidItem), errors.Is(err, domain.ErrInvalidProposal), errors.Is(err, servicectxdomain.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, application.ErrNotScanned):
		http.Error(w, "the file could not be scanned; try again later", http.StatusServiceUnavailable)
	case errors.Is(err, application.ErrModelUnavailable):
		http.Error(w, "the AI is unavailable", http.StatusServiceUnavailable)
	case errors.Is(err, application.ErrBudget):
		http.Error(w, "the daily limit of AI calls for this group was reached", http.StatusTooManyRequests)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return false
	}
	return true
}

// ---- shapes

type evidenceView struct {
	Quote string    `json:"quote,omitempty"`
	At    time.Time `json:"at"`
}

type itemJSON struct {
	ID               uuid.UUID      `json:"id"`
	Kind             string         `json:"kind"`
	Tier             string         `json:"tier"`
	Status           string         `json:"status"`
	TruthLevel       string         `json:"truth_level"`
	Summary          string         `json:"summary"`
	ActorLabel       string         `json:"actor_label"`
	Evidence         []evidenceView `json:"evidence"`
	Pinned           bool           `json:"pinned"`
	Relevance        float64        `json:"relevance"`
	LastReinforcedAt time.Time      `json:"last_reinforced_at"`
	Source           string         `json:"source"`
	Version          int            `json:"version"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

func toItem(it domain.Item, now time.Time) itemJSON {
	ev := make([]evidenceView, 0, len(it.Evidence))
	for _, e := range it.Evidence {
		ev = append(ev, evidenceView{Quote: e.Quote, At: e.At}) // the provider message id stays inside
	}
	rel := domain.DecayedRelevance(it.Relevance, it.LastReinforcedAt, now, it.Protected())
	return itemJSON{ID: it.ID, Kind: string(it.Kind), Tier: string(it.Tier), Status: string(it.Status), TruthLevel: string(it.Truth), Summary: it.Summary, ActorLabel: it.ActorLabel,
		Evidence: ev, Pinned: it.Pinned, Relevance: float64(int(rel*100)) / 100, LastReinforcedAt: it.LastReinforcedAt, Source: string(it.Source), Version: it.Version,
		CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt}
}

type stateJSON struct {
	LastProcessedAt        *time.Time `json:"last_processed_at"`
	LastRunAt              *time.Time `json:"last_run_at"`
	LastRunOK              *bool      `json:"last_run_ok"`
	LastConsolidatedAt     *time.Time `json:"last_consolidated_at"`
	AnalyzeRequestedAt     *time.Time `json:"analyze_requested_at"`
	ConsolidateRequestedAt *time.Time `json:"consolidate_requested_at"`
	PromptVersion          string     `json:"prompt_version"`
	Runs                   int        `json:"runs"`
	InputTokens            int64      `json:"input_tokens"`
	OutputTokens           int64      `json:"output_tokens"`
}

type importJSON struct {
	ID                uuid.UUID      `json:"id"`
	Status            string         `json:"status"`
	Format            string         `json:"format"`
	SourceFilename    string         `json:"source_filename"`
	SHA256            string         `json:"sha256"`
	SizeBytes         int64          `json:"size_bytes"`
	MessagesTotal     int            `json:"messages_total"`
	MessagesParsed    int            `json:"messages_parsed"`
	MessagesRejected  int            `json:"messages_rejected"`
	MessagesDuplicate int            `json:"messages_duplicate"`
	RejectedReasons   map[string]int `json:"rejected_reasons"`
	RangeFrom         *time.Time     `json:"range_from"`
	RangeTo           *time.Time     `json:"range_to"`
	BatchesTotal      int            `json:"batches_total"`
	BatchesOK         int            `json:"batches_ok"`
	BatchesFailed     int            `json:"batches_failed"`
	ItemsCreated      int            `json:"items_created"`
	CoverageComplete  bool           `json:"coverage_complete"`
	PromptVersion     string         `json:"prompt_version"`
	ExtractRuns       int            `json:"extract_runs"`
	CreatedAt         time.Time      `json:"created_at"`
	ExtractedAt       *time.Time     `json:"extracted_at"`
	ExpiresAt         time.Time      `json:"expires_at"`
	DiscardedAt       *time.Time     `json:"discarded_at"`
	DiscardReason     *string        `json:"discard_reason"`
	RawAvailable      bool           `json:"raw_available"`
	Version           int            `json:"version"`
}

func toImport(im domain.Import) importJSON {
	var reason *string
	if im.DiscardReason != nil {
		s := string(*im.DiscardReason)
		reason = &s
	}
	return importJSON{ID: im.ID, Status: string(im.Status), Format: im.Format, SourceFilename: im.SourceFilename, SHA256: im.SHA256, SizeBytes: im.SizeBytes, MessagesTotal: im.MsgsTotal,
		MessagesParsed: im.MsgsParsed, MessagesRejected: im.MsgsRejected, MessagesDuplicate: im.MsgsDuplicate, RejectedReasons: im.RejectedReason, RangeFrom: im.RangeFrom, RangeTo: im.RangeTo,
		BatchesTotal: im.BatchesTotal, BatchesOK: im.BatchesOK, BatchesFailed: im.BatchesFailed, ItemsCreated: im.ItemsCreated, CoverageComplete: im.CoverageComplete(),
		PromptVersion: im.PromptVersion, ExtractRuns: im.ExtractRuns, CreatedAt: im.CreatedAt, ExtractedAt: im.ExtractedAt, ExpiresAt: im.ExpiresAt, DiscardedAt: im.DiscardedAt,
		DiscardReason: reason, RawAvailable: im.Status != domain.ImportDiscarded, Version: im.Version}
}

type proposalJSON struct {
	ID            uuid.UUID  `json:"id"`
	MemoryItemID  *uuid.UUID `json:"memory_item_id"`
	Kind          string     `json:"kind"`
	Title         string     `json:"title"`
	Description   string     `json:"description"`
	Priority      string     `json:"priority"`
	DueAt         *time.Time `json:"due_at"`
	ProposedBy    string     `json:"proposed_by"`
	Status        string     `json:"status"`
	DecidedAt     *time.Time `json:"decided_at"`
	RejectReason  string     `json:"reject_reason,omitempty"`
	CreatedCaseID *uuid.UUID `json:"created_case_id"`
	CreatedTaskID *uuid.UUID `json:"created_task_id"`
	Version       int        `json:"version"`
	CreatedAt     time.Time  `json:"created_at"`
}

func toProposal(p domain.Proposal) proposalJSON {
	return proposalJSON{ID: p.ID, MemoryItemID: p.MemoryItemID, Kind: string(p.Kind), Title: p.Title, Description: p.Description, Priority: p.Priority, DueAt: p.DueAt, ProposedBy: p.ProposedBy,
		Status: string(p.Status), DecidedAt: p.DecidedAt, RejectReason: p.RejectReason, CreatedCaseID: p.CreatedCaseID, CreatedTaskID: p.CreatedTaskID, Version: p.Version, CreatedAt: p.CreatedAt}
}

// ---- the AI opt-in

// SetAI turns the AI on or off for a group.
func (h *Handler) SetAI(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIWrite)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	var req struct {
		Enabled        *bool `json:"enabled"`
		DiscardImports bool  `json:"discard_imports"`
	}
	if !decode(w, r, &req) || req.Enabled == nil {
		if req.Enabled == nil {
			http.Error(w, "enabled must be true or false", http.StatusBadRequest)
		}
		return
	}
	g, discarded, err := h.svc.SetAI(r.Context(), tc.TenantID, gid, tc.ActorID, *req.Enabled, req.DiscardImports)
	if err != nil {
		writeErr(w, err)
		return
	}
	action := auditdomain.ActionGroupAIDisabled
	if *req.Enabled {
		action = auditdomain.ActionGroupAIEnabled
	}
	h.record(r, tc, gid, action, map[string]any{"imports_discarded": discarded})
	writeJSON(w, http.StatusOK, map[string]any{"id": g.ID, "ai_enabled": g.AIEnabled, "imports_discarded": discarded})
}

// ---- memory

func (h *Handler) ListMemory(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIRead)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	q := r.URL.Query()
	f := application.ItemFilter{OrderByRank: true, Query: strings.TrimSpace(q.Get("q")), IncludeSuper: q.Get("include_superseded") == "true"}
	if len(f.Query) > 200 {
		http.Error(w, "invalid search", http.StatusBadRequest)
		return
	}
	if v := q.Get("status"); v != "" {
		if !domain.Status(v).Valid() {
			http.Error(w, "invalid status", http.StatusBadRequest)
			return
		}
		f.Status = []domain.Status{domain.Status(v)}
	}
	if v := q.Get("kind"); v != "" {
		if !domain.Kind(v).Valid() {
			http.Error(w, "invalid kind", http.StatusBadRequest)
			return
		}
		f.Kinds = []domain.Kind{domain.Kind(v)}
	}
	if v := q.Get("tier"); v != "" {
		if v != string(domain.TierEpisodic) && v != string(domain.TierConsolidated) {
			http.Error(w, "invalid tier", http.StatusBadRequest)
			return
		}
		f.Tier = domain.Tier(v)
	}
	g, items, st, err := h.svc.ListItems(r.Context(), tc.TenantID, gid, f)
	if err != nil {
		writeErr(w, err)
		return
	}
	now := time.Now()
	out := make([]itemJSON, 0, len(items))
	for _, it := range items {
		out = append(out, toItem(it, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"group": map[string]any{"id": g.ID, "name": g.Name, "enabled": g.Enabled, "ai_enabled": g.AIEnabled},
		"items": out, "count": len(out),
		"state": stateJSON{LastProcessedAt: st.LastProcessedAt, LastRunAt: st.LastRunAt, LastRunOK: st.LastRunOK, LastConsolidatedAt: st.LastConsolidatedAt, AnalyzeRequestedAt: st.AnalyzeRequestedAt,
			ConsolidateRequestedAt: st.ConsolidateRequestedAt, PromptVersion: st.PromptVersion, Runs: st.Runs, InputTokens: st.InputTokens, OutputTokens: st.OutputTokens},
	})
}

// SearchMemory answers "what do we know about X in this group": a word search over the memory, ranked by what is relevant now. What is found
// is reinforced.
func (h *Handler) SearchMemory(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIRead)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	items, err := h.svc.Search(r.Context(), tc.TenantID, gid, r.URL.Query().Get("q"), 20)
	if err != nil {
		writeErr(w, err)
		return
	}
	now := time.Now()
	out := make([]itemJSON, 0, len(items))
	for _, it := range items {
		out = append(out, toItem(it, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "count": len(out)})
}

func (h *Handler) CreateItem(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIWrite)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	var req struct {
		Kind    string `json:"kind"`
		Summary string `json:"summary"`
		Actor   string `json:"actor_label"`
		Status  string `json:"status"`
	}
	if !decode(w, r, &req) {
		return
	}
	it, err := h.svc.CreateItem(r.Context(), tc.TenantID, gid, tc.ActorID, application.NewItem{Kind: domain.Kind(req.Kind), Summary: req.Summary, Actor: req.Actor, Status: domain.Status(req.Status)})
	if err != nil {
		writeErr(w, err)
		return
	}
	h.record(r, tc, gid, auditdomain.ActionGroupMemoryItemChanged, map[string]any{"change": "created", "item_id": it.ID.String()})
	writeJSON(w, http.StatusCreated, toItem(it, time.Now()))
}

func (h *Handler) PatchItem(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIWrite)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	iid, ok := pathID(w, r, "item_id")
	if !ok {
		return
	}
	var req struct {
		Summary *string `json:"summary"`
		Status  *string `json:"status"`
		Pinned  *bool   `json:"pinned"`
		Confirm *bool   `json:"confirm"`
		Version int     `json:"version"`
	}
	if !decode(w, r, &req) {
		return
	}
	p := application.ItemPatch{Summary: req.Summary, Pinned: req.Pinned, Confirm: req.Confirm, Version: req.Version}
	if req.Status != nil {
		st := domain.Status(*req.Status)
		p.Status = &st
	}
	it, err := h.svc.PatchItem(r.Context(), tc.TenantID, gid, iid, tc.ActorID, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.record(r, tc, gid, auditdomain.ActionGroupMemoryItemChanged, map[string]any{"change": "patched", "item_id": it.ID.String()})
	writeJSON(w, http.StatusOK, toItem(it, time.Now()))
}

func (h *Handler) DeleteItem(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIWrite)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	iid, ok := pathID(w, r, "item_id")
	if !ok {
		return
	}
	if err := h.svc.DeleteItem(r.Context(), tc.TenantID, gid, iid, tc.ActorID); err != nil {
		writeErr(w, err)
		return
	}
	h.record(r, tc, gid, auditdomain.ActionGroupMemoryItemChanged, map[string]any{"change": "deleted", "item_id": iid.String()})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Analyze(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIWrite)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	if err := h.svc.RequestAnalyze(r.Context(), tc.TenantID, gid, tc.ActorID); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"requested": true})
}

func (h *Handler) Consolidate(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIWrite)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	if err := h.svc.RequestConsolidate(r.Context(), tc.TenantID, gid); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"requested": true})
}

// EraseMemory deletes everything the AI remembers about a group.
func (h *Handler) EraseMemory(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permErase)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	items, imports, err := h.svc.EraseMemory(r.Context(), tc.TenantID, gid, tc.ActorID)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.record(r, tc, gid, auditdomain.ActionGroupMemoryErased, map[string]any{"items": items, "imports_discarded": imports})
	writeJSON(w, http.StatusOK, map[string]any{"items_deleted": items, "imports_discarded": imports})
}

// ---- imports

func (h *Handler) ListImports(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIRead)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	list, err := h.svc.ListImports(r.Context(), tc.TenantID, gid)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]importJSON, 0, len(list))
	for _, im := range list {
		out = append(out, toImport(im))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "count": len(out)})
}

func (h *Handler) GetImport(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIRead)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	iid, ok := pathID(w, r, "import_id")
	if !ok {
		return
	}
	im, err := h.svc.GetImport(r.Context(), tc.TenantID, gid, iid)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toImport(im))
}

// UploadImport takes the export file as the request body (a .txt or a .zip), the name in ?filename=. The reading is asynchronous: the answer is 202
// with the coverage of the parse; the extraction runs in the worker.
func (h *Handler) UploadImport(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permImport)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	if err := h.svc.CanImport(r.Context(), tc.TenantID, gid); err != nil {
		writeErr(w, err)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, domain.MaxImportBytes+1))
	if err != nil {
		http.Error(w, "the file is too large", http.StatusRequestEntityTooLarge)
		return
	}
	im, err := h.svc.UploadImport(r.Context(), tc.TenantID, gid, tc.ActorID, r.URL.Query().Get("filename"), body)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.record(r, tc, gid, auditdomain.ActionGroupImportUploaded, map[string]any{"import_id": im.ID.String(), "sha256": im.SHA256, "size_bytes": im.SizeBytes, "messages": im.MsgsParsed})
	writeJSON(w, http.StatusAccepted, toImport(im))
}

func (h *Handler) DiscardImport(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permImport)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	iid, ok := pathID(w, r, "import_id")
	if !ok {
		return
	}
	im, err := h.svc.DiscardImport(r.Context(), tc.TenantID, gid, iid, tc.ActorID)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.record(r, tc, gid, auditdomain.ActionGroupImportDiscarded, map[string]any{"import_id": iid.String(), "sha256": im.SHA256, "reason": "confirmed"})
	writeJSON(w, http.StatusOK, toImport(im))
}

func (h *Handler) ReprocessImport(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permImport)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	iid, ok := pathID(w, r, "import_id")
	if !ok {
		return
	}
	im, err := h.svc.ReprocessImport(r.Context(), tc.TenantID, gid, iid)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.record(r, tc, gid, auditdomain.ActionGroupImportReprocessed, map[string]any{"import_id": iid.String()})
	writeJSON(w, http.StatusAccepted, toImport(im))
}

// ---- proposals

func (h *Handler) ListProposals(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIRead)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "pending_approval", "rejected", "created", "failed":
	default:
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}
	list, err := h.svc.ListProposals(r.Context(), tc.TenantID, gid, status)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]proposalJSON, 0, len(list))
	for _, p := range list {
		out = append(out, toProposal(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "count": len(out)})
}

func (h *Handler) DraftProposal(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIWrite)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	var req struct {
		MemoryItemID uuid.UUID `json:"memory_item_id"`
		Kind         string    `json:"kind"`
	}
	if !decode(w, r, &req) {
		return
	}
	p, created, err := h.svc.DraftProposal(r.Context(), tc.TenantID, gid, req.MemoryItemID, tc.ActorID, domain.ProposalKind(req.Kind))
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, toProposal(p))
}

func (h *Handler) EditProposal(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIWrite)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	pid, ok := pathID(w, r, "proposal_id")
	if !ok {
		return
	}
	var req struct {
		Title       string     `json:"title"`
		Description string     `json:"description"`
		Priority    string     `json:"priority"`
		DueAt       *time.Time `json:"due_at"`
		Version     int        `json:"version"`
	}
	if !decode(w, r, &req) {
		return
	}
	p, err := h.svc.EditProposal(r.Context(), tc.TenantID, gid, pid, application.ProposalEdit{Title: req.Title, Description: req.Description, Priority: req.Priority, DueAt: req.DueAt, Version: req.Version})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProposal(p))
}

func (h *Handler) ApproveProposal(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIWrite)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	pid, ok := pathID(w, r, "proposal_id")
	if !ok {
		return
	}
	p, err := h.svc.ApproveProposal(r.Context(), tc.TenantID, gid, pid, tc.ActorID)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.record(r, tc, gid, auditdomain.ActionGroupProposalApproved, map[string]any{"proposal_id": pid.String(), "kind": string(p.Kind)})
	writeJSON(w, http.StatusOK, toProposal(p))
}

func (h *Handler) RejectProposal(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permAIWrite)
	if !ok {
		return
	}
	gid, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	pid, ok := pathID(w, r, "proposal_id")
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	p, err := h.svc.RejectProposal(r.Context(), tc.TenantID, gid, pid, tc.ActorID, req.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.record(r, tc, gid, auditdomain.ActionGroupProposalRejected, map[string]any{"proposal_id": pid.String()})
	writeJSON(w, http.StatusOK, toProposal(p))
}
