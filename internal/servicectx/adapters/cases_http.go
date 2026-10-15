package adapters

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

// CasesHandler is the HTTP surface of cases and tasks. The same conventions as the sessions: tenant from the TenantContext only, unknown body fields refused,
// If-Match on every change of an existing record, Idempotency-Key on creation.
type CasesHandler struct{ svc *application.Service }

func (h *CasesHandler) Routes(mux Registrar, wrap func(http.HandlerFunc) http.Handler, delegable func(key string, next http.Handler) http.Handler) {
	const base = "/api/v1/tenants/{tenant_id}"
	route := func(key string, fn http.HandlerFunc) http.Handler {
		if delegable == nil || key == "" {
			return wrap(fn)
		}
		return delegable(key, wrap(fn))
	}
	mux.Handle("POST "+base+"/service-cases", route(application.PermCreate, h.openCase))
	mux.Handle("GET "+base+"/service-cases", route(application.PermRead, h.listCases))
	mux.Handle("GET "+base+"/service-cases/{case_id}", route(application.PermRead, h.getCase))
	mux.Handle("PATCH "+base+"/service-cases/{case_id}", route(application.PermUpdate, h.patchCase))
	mux.Handle("POST "+base+"/service-cases/{case_id}/status", route(application.PermClose, h.caseStatus))
	mux.Handle("POST "+base+"/service-cases/{case_id}/assign", route(application.PermAssign, h.assignCase))
	mux.Handle("POST "+base+"/service-cases/{case_id}/sessions", route(application.PermUpdate, h.linkSession))
	mux.Handle("GET "+base+"/service-cases/{case_id}/sessions", route(application.PermRead, h.caseSessions))
	mux.Handle("GET "+base+"/service-cases/{case_id}/timeline", route(application.PermRead, h.caseTimeline))
	mux.Handle("POST "+base+"/service-tasks", route(application.PermCreate, h.createTask))
	mux.Handle("GET "+base+"/service-tasks", route(application.PermRead, h.listTasks))
	mux.Handle("GET "+base+"/service-tasks/{task_id}", route(application.PermRead, h.getTask))
	mux.Handle("PATCH "+base+"/service-tasks/{task_id}", route(application.PermUpdate, h.patchTask))
	mux.Handle("POST "+base+"/service-tasks/{task_id}/assign", route(application.PermAssign, h.assignTask))
	mux.Handle("POST "+base+"/service-tasks/{task_id}/start", route(application.PermUpdate, h.actTask(application.TaskStart)))
	mux.Handle("POST "+base+"/service-tasks/{task_id}/complete", route(application.PermClose, h.actTask(application.TaskComplete)))
	mux.Handle("POST "+base+"/service-tasks/{task_id}/cancel", route(application.PermClose, h.actTask(application.TaskCancel)))
	mux.Handle("POST "+base+"/service-tasks/{task_id}/reopen", route(application.PermClose, h.actTask(application.TaskReopen)))
}

type caseDTO struct {
	ID              uuid.UUID  `json:"id"`
	CaseNumber      string     `json:"case_number"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	CustomerID      *uuid.UUID `json:"customer_id"`
	CategoryID      *uuid.UUID `json:"category_id"`
	Priority        string     `json:"priority"`
	Status          string     `json:"status"`
	AssignedTeamID  *uuid.UUID `json:"assigned_team_id"`
	AssignedAgentID *uuid.UUID `json:"assigned_agent_id"`
	OriginSessionID *uuid.UUID `json:"origin_session_id"`
	Resolution      *string    `json:"resolution"`
	CancelReason    *string    `json:"cancel_reason"`
	OpenedAt        time.Time  `json:"opened_at"`
	ResolvedAt      *time.Time `json:"resolved_at"`
	ClosedAt        *time.Time `json:"closed_at"`
	CancelledAt     *time.Time `json:"cancelled_at"`
	ReopenCount     int        `json:"reopen_count"`
	Version         int        `json:"version"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func toCaseDTO(c *domain.Case) caseDTO {
	return caseDTO{ID: c.ID, CaseNumber: c.CaseNumber, Title: c.Title, Description: c.Description, CustomerID: c.CustomerID, CategoryID: c.CategoryID,
		Priority: string(c.Priority), Status: string(c.Status), AssignedTeamID: c.AssignedTeamID, AssignedAgentID: c.AssignedAgentID, OriginSessionID: c.OriginSessionID,
		Resolution: c.Resolution, CancelReason: c.CancelReason, OpenedAt: c.OpenedAt, ResolvedAt: c.ResolvedAt, ClosedAt: c.ClosedAt, CancelledAt: c.CancelledAt,
		ReopenCount: c.ReopenCount, Version: c.Version, UpdatedAt: c.UpdatedAt}
}

func writeCase(w http.ResponseWriter, status int, c *domain.Case) {
	w.Header().Set("ETag", etag(c.Version))
	writeJSON(w, status, toCaseDTO(c))
}

type taskDTO struct {
	ID             uuid.UUID  `json:"id"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	Status         string     `json:"status"`
	Priority       string     `json:"priority"`
	AssignedUserID *uuid.UUID `json:"assigned_user_id"`
	AssignedTeamID *uuid.UUID `json:"assigned_team_id"`
	DueAt          *time.Time `json:"due_at"`
	Overdue        bool       `json:"overdue"`
	CompletedAt    *time.Time `json:"completed_at"`
	CompletedBy    *uuid.UUID `json:"completed_by_user_id"`
	CancelledAt    *time.Time `json:"cancelled_at"`
	CancelReason   *string    `json:"cancel_reason"`
	OriginType     string     `json:"origin_type"`
	SessionID      *uuid.UUID `json:"session_id"`
	CaseID         *uuid.UUID `json:"case_id"`
	ContactID      *uuid.UUID `json:"contact_id"`
	ReopenCount    int        `json:"reopen_count"`
	Version        int        `json:"version"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func toTaskDTO(t *domain.Task, now time.Time) taskDTO {
	return taskDTO{ID: t.ID, Title: t.Title, Description: t.Description, Status: string(t.Status), Priority: string(t.Priority), AssignedUserID: t.AssignedUserID,
		AssignedTeamID: t.AssignedTeamID, DueAt: t.DueAt, Overdue: t.Overdue(now), CompletedAt: t.CompletedAt, CompletedBy: t.CompletedBy, CancelledAt: t.CancelledAt,
		CancelReason: t.CancelReason, OriginType: t.OriginType, SessionID: t.SessionID, CaseID: t.CaseID, ContactID: t.ContactID, ReopenCount: t.ReopenCount,
		Version: t.Version, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

func writeTask(w http.ResponseWriter, status int, t *domain.Task) {
	w.Header().Set("ETag", etag(t.Version))
	writeJSON(w, status, toTaskDTO(t, time.Now().UTC()))
}

func optUUID(w http.ResponseWriter, r *http.Request, key string) (*uuid.UUID, bool) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil, true
	}
	id, err := uuid.Parse(v)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "invalid "+key)
		return nil, false
	}
	return &id, true
}

func limitParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "invalid limit")
		return 0, false
	}
	return n, true
}

// ---- cases ----

func (h *CasesHandler) openCase(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	var b struct {
		Title           string     `json:"title"`
		Description     string     `json:"description"`
		CustomerID      *uuid.UUID `json:"customer_id"`
		CategoryID      *uuid.UUID `json:"category_id"`
		Priority        string     `json:"priority"`
		AssignedTeamID  *uuid.UUID `json:"assigned_team_id"`
		AssignedAgentID *uuid.UUID `json:"assigned_agent_id"`
		SessionID       *uuid.UUID `json:"session_id"`
	}
	if !decode(w, r, &b) {
		return
	}
	res, err := h.svc.OpenCase(r.Context(), domain.CaseOpenInput{Title: b.Title, Description: b.Description, CustomerID: b.CustomerID, CategoryID: b.CategoryID,
		Priority: domain.Priority(b.Priority), AssignedTeamID: b.AssignedTeamID, AssignedAgentID: b.AssignedAgentID, SessionID: b.SessionID,
		IdempotencyKey: r.Header.Get("Idempotency-Key")})
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusCreated
	if !res.Created {
		status = http.StatusOK
	}
	if res.Replay {
		w.Header().Set("Idempotent-Replay", "true")
	}
	writeCase(w, status, res.Case)
}

func (h *CasesHandler) getCase(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "case_id")
	if !ok {
		return
	}
	c, err := h.svc.GetCase(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeCase(w, http.StatusOK, c)
}

func (h *CasesHandler) listCases(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	q := r.URL.Query()
	f := ports.CaseFilter{Status: domain.CaseStatus(q.Get("status")), Priority: domain.Priority(q.Get("priority")), CaseNumber: q.Get("case_number"), Cursor: q.Get("cursor"),
		OnlyWorking: q.Get("working") == "true"}
	var ok bool
	if f.CustomerID, ok = optUUID(w, r, "customer_id"); !ok {
		return
	}
	if f.AssignedAgentID, ok = optUUID(w, r, "assigned_agent_id"); !ok {
		return
	}
	if f.AssignedTeamID, ok = optUUID(w, r, "assigned_team_id"); !ok {
		return
	}
	if f.SessionID, ok = optUUID(w, r, "session_id"); !ok {
		return
	}
	if f.Limit, ok = limitParam(w, r); !ok {
		return
	}
	items, next, err := h.svc.ListCases(r.Context(), f)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]caseDTO, 0, len(items))
	for i := range items {
		out = append(out, toCaseDTO(&items[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}

func (h *CasesHandler) patchCase(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "case_id")
	if !ok {
		return
	}
	var b struct {
		Title       *string    `json:"title"`
		Description *string    `json:"description"`
		Priority    *string    `json:"priority"`
		CategoryID  *uuid.UUID `json:"category_id"`
		CustomerID  *uuid.UUID `json:"customer_id"`
	}
	if !decode(w, r, &b) {
		return
	}
	in := domain.CasePatchInput{Title: b.Title, Description: b.Description, CategoryID: b.CategoryID, CustomerID: b.CustomerID}
	if b.Priority != nil {
		p := domain.Priority(*b.Priority)
		in.Priority = &p
	}
	c, err := h.svc.PatchCase(r.Context(), id, in, ifMatch(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeCase(w, http.StatusOK, c)
}

func (h *CasesHandler) caseStatus(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "case_id")
	if !ok {
		return
	}
	var b struct {
		Status     string `json:"status"`
		Resolution string `json:"resolution"`
		Reason     string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := h.svc.SetCaseStatus(r.Context(), id, domain.CaseStatusInput{To: domain.CaseStatus(b.Status), Resolution: b.Resolution, Reason: b.Reason}, ifMatch(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeCase(w, http.StatusOK, c)
}

func (h *CasesHandler) assignCase(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "case_id")
	if !ok {
		return
	}
	var b struct {
		TeamID  *uuid.UUID `json:"assigned_team_id"`
		AgentID *uuid.UUID `json:"assigned_agent_id"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := h.svc.AssignCase(r.Context(), id, b.TeamID, b.AgentID, ifMatch(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeCase(w, http.StatusOK, c)
}

func (h *CasesHandler) linkSession(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "case_id")
	if !ok {
		return
	}
	var b struct {
		SessionID uuid.UUID `json:"session_id"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.SessionID == uuid.Nil {
		writeErr(w, http.StatusBadRequest, "invalid", "session_id is required")
		return
	}
	linked, err := h.svc.LinkSessionToCase(r.Context(), id, b.SessionID)
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusCreated
	if !linked {
		status = http.StatusOK // it already stood
	}
	writeJSON(w, status, map[string]any{"case_id": id, "session_id": b.SessionID, "linked": true, "created": linked})
}

func (h *CasesHandler) caseSessions(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "case_id")
	if !ok {
		return
	}
	items, err := h.svc.CaseSessions(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]sessionDTO, 0, len(items))
	for i := range items {
		out = append(out, toDTO(&items[i], nil))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *CasesHandler) caseTimeline(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "case_id")
	if !ok {
		return
	}
	cursor, limit, asc, ok := timelineParams(w, r)
	if !ok {
		return
	}
	items, next, err := h.svc.CaseTimeline(r.Context(), id, cursor, limit, asc)
	writeTimeline(w, items, next, err)
}

// ---- tasks ----

func (h *CasesHandler) createTask(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	var b struct {
		Title          string     `json:"title"`
		Description    string     `json:"description"`
		Priority       string     `json:"priority"`
		AssignedUserID *uuid.UUID `json:"assigned_user_id"`
		AssignedTeamID *uuid.UUID `json:"assigned_team_id"`
		DueAt          *time.Time `json:"due_at"`
		SessionID      *uuid.UUID `json:"session_id"`
		CaseID         *uuid.UUID `json:"case_id"`
		ContactID      *uuid.UUID `json:"contact_id"`
	}
	if !decode(w, r, &b) {
		return
	}
	// the origin is decided HERE from the door the request came through: a person at the Workspace. A caller cannot claim to be a flow or the AI.
	res, err := h.svc.CreateTask(r.Context(), domain.TaskOpenInput{Title: b.Title, Description: b.Description, Priority: domain.Priority(b.Priority), AssignedUserID: b.AssignedUserID,
		AssignedTeamID: b.AssignedTeamID, DueAt: b.DueAt, SessionID: b.SessionID, CaseID: b.CaseID, ContactID: b.ContactID, OriginType: domain.OriginHuman,
		IdempotencyKey: r.Header.Get("Idempotency-Key")})
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusCreated
	if !res.Created {
		status = http.StatusOK
	}
	if res.Replay {
		w.Header().Set("Idempotent-Replay", "true")
	}
	writeTask(w, status, res.Task)
}

func (h *CasesHandler) getTask(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "task_id")
	if !ok {
		return
	}
	t, err := h.svc.GetTask(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeTask(w, http.StatusOK, t)
}

func (h *CasesHandler) listTasks(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	q := r.URL.Query()
	f := ports.TaskFilter{Status: domain.TaskStatus(q.Get("status")), PendingOnly: q.Get("pending") == "true", OverdueOnly: q.Get("overdue") == "true", Cursor: q.Get("cursor")}
	var ok bool
	if f.AssignedUserID, ok = optUUID(w, r, "assigned_user_id"); !ok {
		return
	}
	if f.AssignedTeamID, ok = optUUID(w, r, "assigned_team_id"); !ok {
		return
	}
	if f.SessionID, ok = optUUID(w, r, "session_id"); !ok {
		return
	}
	if f.CaseID, ok = optUUID(w, r, "case_id"); !ok {
		return
	}
	if f.ContactID, ok = optUUID(w, r, "contact_id"); !ok {
		return
	}
	if f.Limit, ok = limitParam(w, r); !ok {
		return
	}
	items, next, err := h.svc.ListTasks(r.Context(), f)
	if err != nil {
		fail(w, err)
		return
	}
	now := time.Now().UTC()
	out := make([]taskDTO, 0, len(items))
	for i := range items {
		out = append(out, toTaskDTO(&items[i], now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}

func (h *CasesHandler) patchTask(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "task_id")
	if !ok {
		return
	}
	var b struct {
		Title       *string    `json:"title"`
		Description *string    `json:"description"`
		Priority    *string    `json:"priority"`
		DueAt       *time.Time `json:"due_at"`
		ClearDue    bool       `json:"clear_due"`
	}
	if !decode(w, r, &b) {
		return
	}
	in := domain.TaskPatchInput{Title: b.Title, Description: b.Description, DueAt: b.DueAt, ClearDue: b.ClearDue}
	if b.Priority != nil {
		p := domain.Priority(*b.Priority)
		in.Priority = &p
	}
	t, err := h.svc.PatchTask(r.Context(), id, in, ifMatch(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeTask(w, http.StatusOK, t)
}

func (h *CasesHandler) assignTask(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "task_id")
	if !ok {
		return
	}
	var b struct {
		UserID *uuid.UUID `json:"assigned_user_id"`
		TeamID *uuid.UUID `json:"assigned_team_id"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.AssignTask(r.Context(), id, b.UserID, b.TeamID, ifMatch(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeTask(w, http.StatusOK, t)
}

func (h *CasesHandler) actTask(action application.TaskAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authenticated(w, r) {
			return
		}
		id, ok := pathUUID(w, r, "task_id")
		if !ok {
			return
		}
		var b struct {
			Reason string `json:"reason"`
		}
		if r.ContentLength != 0 && !decode(w, r, &b) {
			return
		}
		t, err := h.svc.ActTask(r.Context(), id, action, b.Reason, ifMatch(r))
		if err != nil {
			fail(w, err)
			return
		}
		writeTask(w, http.StatusOK, t)
	}
}
