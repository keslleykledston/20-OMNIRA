package adapters

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Permission keys (migration 000083). Editing and publishing are separate on purpose.
const (
	PermTemplateView    = "flow_template.view"
	PermTemplateInstall = "flow_template.install"
	PermView            = "flow.view"
	PermCreate          = "flow.create"
	PermEdit            = "flow.edit"
	PermTest            = "flow.test"
	PermPublish         = "flow.publish"
	PermArchive         = "flow.archive"
)

const maxBody = 2 << 20

// Handler is the REST surface of the control plane. Routes are mounted behind authn + the tenant session middleware
// (the tenant comes from the session, never from the body).
type Handler struct {
	pool *pgxpool.Pool
	cp   *application.ControlPlane
	ts   *application.TemplateService
}

// WithTemplates enables the template/pack catalog and installation endpoints.
func (h *Handler) WithTemplates(ts *application.TemplateService) *Handler {
	h.ts = ts
	return h
}

func NewHandler(pool *pgxpool.Pool, cp *application.ControlPlane) *Handler {
	return &Handler{pool: pool, cp: cp}
}

// session authenticates the TenantContext and authorizes by the role->permission matrix (never by role name).
func (h *Handler) session(w http.ResponseWriter, r *http.Request, permission string) (*tenancydomain.TenantContext, bool) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return nil, false
	}
	var ok bool
	if err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`,
		tc.TenantID, tc.ActorID, permission).Scan(&ok); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "failed to check permission")
		return nil, false
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "forbidden", "you do not have permission to do this")
		return nil, false
	}
	return tc, true
}

type apiError struct {
	Error   string         `json:"error"`
	Detail  string         `json:"detail,omitempty"`
	Issues  []domain.Issue `json:"issues,omitempty"`
	Missing []string       `json:"missing,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, apiError{Error: code, Detail: detail})
}

// fail maps domain errors to HTTP. Unknown errors are 500 with no internals leaked.
func fail(w http.ResponseWriter, err error) {
	var pe *application.PublishError
	var mm *application.MissingMappingsError
	switch {
	case errors.As(err, &mm):
		writeJSON(w, http.StatusBadRequest, apiError{Error: "missing_mappings", Detail: "map every placeholder to one of your own resources", Missing: mm.Keys})
	case errors.As(err, &pe):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "not_publishable", Detail: "the flow has blocking errors", Issues: pe.Issues})
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrNoSuchVersion):
		writeErr(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, domain.ErrSlugTaken):
		writeErr(w, http.StatusConflict, "slug_taken", "a flow with this slug already exists")
	case errors.Is(err, domain.ErrRevisionConflict):
		writeErr(w, http.StatusConflict, "revision_conflict", "the draft changed since you loaded it; reload and retry")
	case errors.Is(err, domain.ErrArchived):
		writeErr(w, http.StatusConflict, "archived", "the flow is archived")
	case errors.Is(err, domain.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal", "unexpected error")
	}
}

type flowDTO struct {
	ID                 uuid.UUID            `json:"id"`
	Slug               string               `json:"slug"`
	Name               string               `json:"name"`
	Description        string               `json:"description"`
	Type               domain.FlowType      `json:"type"`
	Status             domain.FlowStatus    `json:"status"`
	DraftRevision      int                  `json:"draft_revision"`
	ActiveVersion      *int                 `json:"active_version"`
	Priority           int                  `json:"priority"`
	IsDefault          bool                 `json:"is_default"`
	TriggerFilter      domain.TriggerFilter `json:"trigger_filter"`
	RestartPolicy      domain.RestartPolicy `json:"restart_policy"`
	SourceTemplateSlug *string              `json:"source_template_slug"`
	SourceTemplateVer  *int                 `json:"source_template_version"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
}

func toFlowDTO(f *domain.Flow, active *domain.FlowVersion) flowDTO {
	d := flowDTO{ID: f.ID, Slug: f.Slug, Name: f.Name, Description: f.Description, Type: f.Type, Status: f.Status,
		DraftRevision: f.DraftRevision, Priority: f.Priority, IsDefault: f.IsDefault, TriggerFilter: f.TriggerFilter,
		RestartPolicy: f.RestartPolicy, SourceTemplateSlug: f.SourceTemplateSlug, SourceTemplateVer: f.SourceTemplateVersion,
		CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt}
	if active != nil {
		d.ActiveVersion = &active.Version
	}
	return d
}

type versionDTO struct {
	ID          uuid.UUID            `json:"id"`
	Version     int                  `json:"version"`
	Hash        string               `json:"definition_hash"`
	Note        string               `json:"note"`
	SubflowPins map[string]uuid.UUID `json:"subflow_pins"`
	PublishedBy *uuid.UUID           `json:"published_by"`
	PublishedAt time.Time            `json:"published_at"`
	Definition  json.RawMessage      `json:"definition,omitempty"`
}

func toVersionDTO(v *domain.FlowVersion, withDefinition bool) versionDTO {
	d := versionDTO{ID: v.ID, Version: v.Version, Hash: v.DefinitionHash, Note: v.Note, SubflowPins: v.SubflowPins, PublishedBy: v.PublishedBy, PublishedAt: v.PublishedAt}
	if withDefinition {
		d.Definition = v.Definition
	}
	return d
}

func intQuery(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil {
		return v
	}
	return def
}

func pathID(r *http.Request, key string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(key))
	return id, err == nil && id != uuid.Nil
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "the request body is not valid JSON for this endpoint")
		return false
	}
	return true
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermView); !ok {
		return
	}
	flows, err := h.cp.List(r.Context(), ports.ListFilter{
		Status: domain.FlowStatus(r.URL.Query().Get("status")), Type: domain.FlowType(r.URL.Query().Get("type")),
		Limit: intQuery(r, "limit", 50), Offset: intQuery(r, "offset", 0)})
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]flowDTO, 0, len(flows))
	for _, f := range flows {
		out = append(out, toFlowDTO(f, nil))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermCreate); !ok {
		return
	}
	var in struct {
		Slug        string          `json:"slug"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Type        domain.FlowType `json:"type"`
	}
	if !decode(w, r, &in) {
		return
	}
	f, err := h.cp.Create(r.Context(), application.CreateInput{Slug: in.Slug, Name: in.Name, Description: in.Description, Type: in.Type})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toFlowDTO(f, nil))
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermView); !ok {
		return
	}
	id, ok := pathID(r, "flow_id")
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	f, err := h.cp.Get(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	active, err := h.cp.ActiveVersion(r.Context(), f)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		flowDTO
		Definition json.RawMessage `json:"definition"`
	}{toFlowDTO(f, active), f.DraftDefinition})
}

type draftBody struct {
	Revision    int             `json:"revision"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Definition  json.RawMessage `json:"definition"`
}

func (h *Handler) SaveDraft(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermEdit); !ok {
		return
	}
	id, ok := pathID(r, "flow_id")
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	var in draftBody
	if !decode(w, r, &in) {
		return
	}
	res, err := h.cp.SaveDraft(r.Context(), id, in.Revision, in.Name, in.Description, in.Definition)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"flow": toFlowDTO(res.Flow, nil), "issues": res.Issues})
}

// ValidateStored validates the saved draft; ValidateDraft validates an unsaved definition (live validation).
func (h *Handler) ValidateStored(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermView); !ok {
		return
	}
	id, ok := pathID(r, "flow_id")
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	res, err := h.cp.ValidateStored(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": !domain.HasErrors(res.Issues), "issues": res.Issues})
}

func (h *Handler) ValidateDraft(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermEdit); !ok {
		return
	}
	var in struct {
		Definition json.RawMessage `json:"definition"`
	}
	if !decode(w, r, &in) {
		return
	}
	issues, err := h.cp.ValidateRaw(r.Context(), in.Definition)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": !domain.HasErrors(issues), "issues": issues})
}

func (h *Handler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermEdit); !ok {
		return
	}
	id, ok := pathID(r, "flow_id")
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	var in struct {
		Priority      int                  `json:"priority"`
		IsDefault     bool                 `json:"is_default"`
		TriggerFilter domain.TriggerFilter `json:"trigger_filter"`
		RestartPolicy domain.RestartPolicy `json:"restart_policy"`
	}
	if !decode(w, r, &in) {
		return
	}
	f, err := h.cp.UpdateSettings(r.Context(), id, ports.Settings{Priority: in.Priority, IsDefault: in.IsDefault, TriggerFilter: in.TriggerFilter, RestartPolicy: in.RestartPolicy})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toFlowDTO(f, nil))
}

func (h *Handler) Publish(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermPublish); !ok {
		return
	}
	id, ok := pathID(r, "flow_id")
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	var in struct {
		Revision int    `json:"revision"`
		Note     string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	res, err := h.cp.Publish(r.Context(), id, in.Revision, in.Note)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"version": toVersionDTO(res.Version, false), "warnings": res.Warnings})
}

func (h *Handler) Versions(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermView); !ok {
		return
	}
	id, ok := pathID(r, "flow_id")
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	vs, err := h.cp.Versions(r.Context(), id, intQuery(r, "limit", 50), intQuery(r, "offset", 0))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]versionDTO, 0, len(vs))
	for _, v := range vs {
		out = append(out, toVersionDTO(v, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) Version(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermView); !ok {
		return
	}
	id, ok := pathID(r, "flow_id")
	n, err := strconv.Atoi(r.PathValue("version"))
	if !ok || err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	v, err := h.cp.Version(r.Context(), id, n)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toVersionDTO(v, true))
}

func (h *Handler) Activate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermPublish); !ok {
		return
	}
	id, ok := pathID(r, "flow_id")
	n, err := strconv.Atoi(r.PathValue("version"))
	if !ok || err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	f, v, err := h.cp.Activate(r.Context(), id, n)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toFlowDTO(f, v))
}

func (h *Handler) Archive(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermArchive); !ok {
		return
	}
	id, ok := pathID(r, "flow_id")
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if err := h.cp.Archive(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Simulate runs a scenario against the draft (or an unsaved definition) with the real engine and no side effects.
func (h *Handler) Simulate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermTest); !ok {
		return
	}
	id, ok := pathID(r, "flow_id")
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	var in struct {
		Definition json.RawMessage      `json:"definition"`
		Scenario   application.Scenario `json:"scenario"`
	}
	if !decode(w, r, &in) {
		return
	}
	res, err := h.cp.SimulateFlow(r.Context(), id, in.Definition, in.Scenario)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) templatesReady(w http.ResponseWriter) bool {
	if h.ts == nil {
		writeErr(w, http.StatusNotImplemented, "templates_unavailable", "the template library is not enabled")
		return false
	}
	return true
}

func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermTemplateView); !ok || !h.templatesReady(w) {
		return
	}
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, map[string]any{"items": h.ts.Templates(application.TemplateFilter{Category: q.Get("category"), Query: q.Get("q"), RecommendedFor: q.Get("recommended_for")})})
}

func (h *Handler) GetTemplate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermTemplateView); !ok || !h.templatesReady(w) {
		return
	}
	info, err := h.ts.Template(r.PathValue("slug"), intQuery(r, "version", 0))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (h *Handler) ListPacks(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermTemplateView); !ok || !h.templatesReady(w) {
		return
	}
	packs, err := h.ts.Packs(r.URL.Query().Get("recommended_for"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": packs})
}

func (h *Handler) GetPack(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermTemplateView); !ok || !h.templatesReady(w) {
		return
	}
	var selected []string
	if v := strings.TrimSpace(r.URL.Query().Get("templates")); v != "" {
		selected = strings.Split(v, ",")
	}
	info, err := h.ts.Pack(r.PathValue("slug"), intQuery(r, "version", 0), selected)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

type installBody struct {
	Version   int               `json:"version"`
	Templates []string          `json:"templates"`
	Mappings  map[string]string `json:"mappings"`
}

func (h *Handler) InstallTemplate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermTemplateInstall); !ok || !h.templatesReady(w) {
		return
	}
	var in installBody
	if !decode(w, r, &in) {
		return
	}
	res, err := h.ts.Install(r.Context(), application.InstallRequest{Templates: []string{r.PathValue("slug")}, Version: in.Version, Mappings: in.Mappings})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (h *Handler) InstallPack(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermTemplateInstall); !ok || !h.templatesReady(w) {
		return
	}
	var in installBody
	if !decode(w, r, &in) {
		return
	}
	res, err := h.ts.Install(r.Context(), application.InstallRequest{Pack: r.PathValue("slug"), Version: in.Version, Templates: in.Templates, Mappings: in.Mappings})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// NodeTypes serves the node library of the builder.
func (h *Handler) NodeTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r, PermView); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": application.NodeCatalog()})
}

// Registrar is what Routes needs from a router (*http.ServeMux satisfies it; tests use a recorder).
type Registrar interface {
	Handle(pattern string, handler http.Handler)
}

// Routes mounts the control-plane API. wrap adds authn + the tenant session (see httpserver.RegisterFlowHandlers).
func (h *Handler) Routes(mux Registrar, wrap func(http.HandlerFunc) http.Handler) {
	const base = "/api/v1/tenants/{tenant_id}/flows"
	mux.Handle("GET "+base, wrap(h.List))
	mux.Handle("POST "+base, wrap(h.Create))
	mux.Handle("POST "+base+"/validate", wrap(h.ValidateDraft))
	mux.Handle("GET "+base+"/{flow_id}", wrap(h.Get))
	mux.Handle("PUT "+base+"/{flow_id}/draft", wrap(h.SaveDraft))
	mux.Handle("POST "+base+"/{flow_id}/validate", wrap(h.ValidateStored))
	mux.Handle("PATCH "+base+"/{flow_id}/settings", wrap(h.UpdateSettings))
	mux.Handle("POST "+base+"/{flow_id}/simulate", wrap(h.Simulate))
	mux.Handle("POST "+base+"/{flow_id}/publish", wrap(h.Publish))
	mux.Handle("GET "+base+"/{flow_id}/versions", wrap(h.Versions))
	mux.Handle("GET "+base+"/{flow_id}/versions/{version}", wrap(h.Version))
	mux.Handle("POST "+base+"/{flow_id}/versions/{version}/activate", wrap(h.Activate))
	mux.Handle("POST "+base+"/{flow_id}/archive", wrap(h.Archive))
	mux.Handle("GET /api/v1/tenants/{tenant_id}/flow-node-types", wrap(h.NodeTypes))
	const lib = "/api/v1/tenants/{tenant_id}"
	mux.Handle("GET "+lib+"/flow-templates", wrap(h.ListTemplates))
	mux.Handle("GET "+lib+"/flow-templates/{slug}", wrap(h.GetTemplate))
	mux.Handle("POST "+lib+"/flow-templates/{slug}/install", wrap(h.InstallTemplate))
	mux.Handle("GET "+lib+"/flow-packs", wrap(h.ListPacks))
	mux.Handle("GET "+lib+"/flow-packs/{slug}", wrap(h.GetPack))
	mux.Handle("POST "+lib+"/flow-packs/{slug}/install", wrap(h.InstallPack))
}
