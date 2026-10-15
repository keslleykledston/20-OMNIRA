package adapters

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

// ExternalHandler is the HTTP surface of the ERP/CRM side: records and links always; asking the ERP for anything (service orders, reconciliation) only when the
// company-wide switch OMNIRA_SERVICE_EXTERNAL_ORDERS_ENABLED is on.
type ExternalHandler struct {
	svc    *application.Service
	orders bool
}

func (h *ExternalHandler) Routes(mux Registrar, wrap func(http.HandlerFunc) http.Handler, delegable func(key string, next http.Handler) http.Handler) {
	const base = "/api/v1/tenants/{tenant_id}"
	route := func(key string, fn http.HandlerFunc) http.Handler {
		if delegable == nil || key == "" {
			return wrap(fn)
		}
		return delegable(key, wrap(fn))
	}
	mux.Handle("POST "+base+"/external-records", route(application.PermLinkExternal, h.link))
	mux.Handle("GET "+base+"/external-records", route(application.PermRead, h.listRecords))
	mux.Handle("GET "+base+"/external-records/{record_id}", route(application.PermRead, h.getRecord))
	mux.Handle("POST "+base+"/external-records/{record_id}/refresh", route(application.PermLinkExternal, h.refresh))
	mux.Handle("POST "+base+"/external-record-links/{link_id}/unlink", route(application.PermLinkExternal, h.unlink))
	if !h.orders {
		return
	}
	// asking the ERP to do something is never delegable to a Hub (the key is not in the delegable catalogue)
	mux.Handle("POST "+base+"/service-sessions/{session_id}/external-service-orders", wrap(h.requestOrder))
	mux.Handle("GET "+base+"/external-requests", wrap(h.listRequests)) // the requests to the ERP are the company's own: never delegable
	mux.Handle("GET "+base+"/external-requests/{request_id}", wrap(h.getRequest))
	mux.Handle("POST "+base+"/external-requests/{request_id}/reconcile", wrap(h.reconcile))
	mux.Handle("POST "+base+"/external-requests/{request_id}/resolve", wrap(h.resolve))
}

type recordDTO struct {
	ID              uuid.UUID  `json:"id"`
	IntegrationID   uuid.UUID  `json:"integration_id"`
	ExternalSystem  string     `json:"external_system"`
	ExternalType    string     `json:"external_type"`
	ExternalID      string     `json:"external_id"`
	DisplayRef      string     `json:"display_ref"`
	ExternalStatus  string     `json:"external_status"`
	StatusClass     string     `json:"status_class"`
	SyncState       string     `json:"sync_state"`
	SourceUpdatedAt *time.Time `json:"source_updated_at"`
	LastSyncedAt    *time.Time `json:"last_synced_at"`
	Version         int        `json:"version"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func toRecordDTO(x *domain.ExternalRecord) recordDTO {
	return recordDTO{ID: x.ID, IntegrationID: x.IntegrationID, ExternalSystem: x.ExternalSystem, ExternalType: x.ExternalType, ExternalID: x.ExternalID, DisplayRef: x.DisplayRef,
		ExternalStatus: x.ExternalStatus, StatusClass: string(x.StatusClass), SyncState: x.SyncState, SourceUpdatedAt: x.SourceUpdatedAt, LastSyncedAt: x.LastSyncedAt,
		Version: x.Version, CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt}
}

type linkDTO struct {
	ID               uuid.UUID  `json:"id"`
	ExternalRecordID uuid.UUID  `json:"external_record_id"`
	SessionID        *uuid.UUID `json:"session_id"`
	CaseID           *uuid.UUID `json:"case_id"`
	TaskID           *uuid.UUID `json:"task_id"`
	LinkedAt         time.Time  `json:"linked_at"`
	UnlinkedAt       *time.Time `json:"unlinked_at"`
}

func toLinkDTO(l *domain.ExternalLink) linkDTO {
	return linkDTO{ID: l.ID, ExternalRecordID: l.ExternalRecordID, SessionID: l.SessionID, CaseID: l.CaseID, TaskID: l.TaskID, LinkedAt: l.LinkedAt, UnlinkedAt: l.UnlinkedAt}
}

type requestDTO struct {
	ID               uuid.UUID  `json:"id"`
	IntegrationID    uuid.UUID  `json:"integration_id"`
	SessionID        uuid.UUID  `json:"session_id"`
	CaseID           *uuid.UUID `json:"case_id"`
	Operation        string     `json:"operation"`
	Title            string     `json:"title"`
	State            string     `json:"state"`
	ExternalRecordID *uuid.UUID `json:"external_record_id"`
	FailureClass     *string    `json:"failure_class"`
	ResolutionSource *string    `json:"resolution_source"`
	RequestedBy      *uuid.UUID `json:"requested_by_user_id"`
	CreatedAt        time.Time  `json:"created_at"`
	ResolvedAt       *time.Time `json:"resolved_at"`
	Version          int        `json:"version"`
}

func toRequestDTO(x *domain.ExternalRequest) requestDTO {
	return requestDTO{ID: x.ID, IntegrationID: x.IntegrationID, SessionID: x.SessionID, CaseID: x.CaseID, Operation: x.Operation, Title: x.Title, State: string(x.State),
		ExternalRecordID: x.ExternalRecordID, FailureClass: x.FailureClass, ResolutionSource: x.ResolutionSource, RequestedBy: x.RequestedBy, CreatedAt: x.CreatedAt,
		ResolvedAt: x.ResolvedAt, Version: x.Version}
}

func writeRecord(w http.ResponseWriter, status int, x *domain.ExternalRecord) {
	w.Header().Set("ETag", etag(x.Version))
	writeJSON(w, status, toRecordDTO(x))
}

func (h *ExternalHandler) link(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	var b struct {
		IntegrationID uuid.UUID  `json:"integration_id"`
		ExternalType  string     `json:"external_type"`
		ExternalID    string     `json:"external_id"`
		SessionID     *uuid.UUID `json:"session_id"`
		CaseID        *uuid.UUID `json:"case_id"`
		TaskID        *uuid.UUID `json:"task_id"`
	}
	if !decode(w, r, &b) {
		return
	}
	res, err := h.svc.LinkExternalRecord(r.Context(), domain.LinkInput{IntegrationID: b.IntegrationID, ExternalType: b.ExternalType, ExternalID: b.ExternalID,
		SessionID: b.SessionID, CaseID: b.CaseID, TaskID: b.TaskID})
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusCreated
	if !res.LinkCreated {
		status = http.StatusOK // it already stood
	}
	w.Header().Set("ETag", etag(res.Record.Version))
	writeJSON(w, status, map[string]any{"record": toRecordDTO(res.Record), "link": toLinkDTO(res.Link), "record_created": res.RecordCreated, "link_created": res.LinkCreated})
}

func (h *ExternalHandler) getRecord(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "record_id")
	if !ok {
		return
	}
	x, err := h.svc.GetExternalRecord(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeRecord(w, http.StatusOK, x)
}

func (h *ExternalHandler) listRecords(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	q := r.URL.Query()
	f := ports.RecordFilter{ExternalType: q.Get("external_type"), ExternalID: q.Get("external_id"), StatusClass: domain.StatusClass(q.Get("status_class")), Cursor: q.Get("cursor")}
	var ok bool
	if f.IntegrationID, ok = optUUID(w, r, "integration_id"); !ok {
		return
	}
	if f.SessionID, ok = optUUID(w, r, "session_id"); !ok {
		return
	}
	if f.CaseID, ok = optUUID(w, r, "case_id"); !ok {
		return
	}
	if f.TaskID, ok = optUUID(w, r, "task_id"); !ok {
		return
	}
	if f.Limit, ok = limitParam(w, r); !ok {
		return
	}
	items, next, err := h.svc.ListExternalRecords(r.Context(), f)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]recordDTO, 0, len(items))
	for i := range items {
		out = append(out, toRecordDTO(&items[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}

func (h *ExternalHandler) refresh(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "record_id")
	if !ok {
		return
	}
	x, err := h.svc.RefreshExternalRecord(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeRecord(w, http.StatusOK, x)
}

func (h *ExternalHandler) unlink(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "link_id")
	if !ok {
		return
	}
	l, err := h.svc.UnlinkExternalRecord(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toLinkDTO(l))
}

// ---- asking the ERP ----

func (h *ExternalHandler) requestOrder(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	sessionID, ok := pathUUID(w, r, "session_id")
	if !ok {
		return
	}
	var b struct {
		IntegrationID uuid.UUID  `json:"integration_id"`
		CaseID        *uuid.UUID `json:"case_id"`
		Title         string     `json:"title"`
		Description   string     `json:"description"`
	}
	if !decode(w, r, &b) {
		return
	}
	res, err := h.svc.RequestServiceOrder(r.Context(), domain.RequestInput{IntegrationID: b.IntegrationID, SessionID: sessionID, CaseID: b.CaseID, Title: b.Title,
		Description: b.Description, IdempotencyKey: r.Header.Get("Idempotency-Key")})
	if err != nil {
		fail(w, err)
		return
	}
	body := map[string]any{"outcome": string(res.Outcome), "request": toRequestDTO(res.Request)}
	if res.Record != nil {
		body["record"] = toRecordDTO(res.Record)
	}
	switch res.Outcome {
	case application.OutcomeCreated:
		writeJSON(w, http.StatusCreated, body)
	case application.OutcomeReplay:
		w.Header().Set("Idempotent-Replay", "true")
		writeJSON(w, http.StatusOK, body)
	case application.OutcomeRejected:
		body["error"] = "external_rejected"
		writeJSON(w, http.StatusUnprocessableEntity, body)
	case application.OutcomeBlocked:
		body["error"] = "request_blocked"
		body["detail"] = "another request of this session is in flight or of unknown outcome; reconcile it first"
		writeJSON(w, http.StatusConflict, body)
	default: // outcome unknown: accepted, but NOT done — and nothing will be repeated until it is reconciled
		writeJSON(w, http.StatusAccepted, body)
	}
}

func (h *ExternalHandler) getRequest(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "request_id")
	if !ok {
		return
	}
	x, err := h.svc.GetExternalRequest(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("ETag", etag(x.Version))
	writeJSON(w, http.StatusOK, toRequestDTO(x))
}

func (h *ExternalHandler) listRequests(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	q := r.URL.Query()
	f := ports.RequestFilter{State: domain.RequestState(q.Get("state")), Cursor: q.Get("cursor")}
	var ok bool
	if f.SessionID, ok = optUUID(w, r, "session_id"); !ok {
		return
	}
	if f.Limit, ok = limitParam(w, r); !ok {
		return
	}
	items, next, err := h.svc.ListExternalRequests(r.Context(), f)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]requestDTO, 0, len(items))
	for i := range items {
		out = append(out, toRequestDTO(&items[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}

func reconcileBody(res application.ReconcileResult) map[string]any {
	body := map[string]any{"request": toRequestDTO(res.Request), "needs_manual": res.NeedsManual, "too_early": res.TooEarly}
	if res.Record != nil {
		body["record"] = toRecordDTO(res.Record)
	}
	return body
}

func (h *ExternalHandler) reconcile(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "request_id")
	if !ok {
		return
	}
	res, err := h.svc.ReconcileExternalRequest(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reconcileBody(res))
}

func (h *ExternalHandler) resolve(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "request_id")
	if !ok {
		return
	}
	var b struct {
		Created    bool   `json:"created"`
		ExternalID string `json:"external_id"`
		Reason     string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	res, err := h.svc.ResolveExternalRequest(r.Context(), id, domain.ResolveInput{Created: b.Created, ExternalID: b.ExternalID, Reason: b.Reason})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reconcileBody(res))
}
