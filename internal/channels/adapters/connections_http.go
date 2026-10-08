package adapters

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/application"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/entitlements"
)

// ConnectionHandler serves WAHA connection/session management. Mount it
// behind authn + the tenant-session middleware; tenant and actor come only
// from the TenantContext, never from the URL or body.
type ConnectionHandler struct {
	svc *application.WahaConnectionService
}

// ManagementHandler serves the provider-neutral I0 API. Legacy WAHA routes
// keep using ConnectionHandler as aliases until the web migrates in I1.
type ManagementHandler struct {
	svc *application.ConnectionManagementService
}

func NewManagementHandler(svc *application.ConnectionManagementService) *ManagementHandler {
	return &ManagementHandler{svc: svc}
}

func NewConnectionHandler(svc *application.WahaConnectionService) *ConnectionHandler {
	return &ConnectionHandler{svc: svc}
}

type connectionJSON struct {
	ID                 uuid.UUID `json:"id"`
	Provider           string    `json:"provider"`
	ProviderKind       string    `json:"provider_kind"`
	Status             string    `json:"status"`
	SessionStatus      string    `json:"session_status,omitempty"`
	ExternalAccountID  string    `json:"external_account_id,omitempty"`
	Capabilities       []string  `json:"capabilities"`
	RiskAcknowledgedAt *string   `json:"risk_acknowledged_at,omitempty"`
	CreatedAt          string    `json:"created_at"`
	// CheckedAt (PILOT.4C) — when the provider was actually asked for this
	// live session status; omitted for views that never called the provider
	// (List/Create). Operators use this to tell a fresh read from a stale one.
	CheckedAt string `json:"checked_at,omitempty"`
	// Displays: non-secret values to show the operator (callback URL, verify token, number facts).
	Displays map[string]string `json:"displays,omitempty"`
}

func toJSON(v application.ConnectionView) connectionJSON {
	caps := make([]string, len(v.Capabilities))
	for i, c := range v.Capabilities {
		caps[i] = string(c)
	}
	out := connectionJSON{ID: v.ID, Provider: v.Provider, ProviderKind: string(v.ProviderKind), Status: string(v.Status),
		SessionStatus: string(v.SessionStatus), ExternalAccountID: v.ExternalAccountID, Capabilities: caps, CreatedAt: v.CreatedAt.UTC().Format(time.RFC3339)}
	if v.RiskAcknowledgedAt != nil {
		s := v.RiskAcknowledgedAt.UTC().Format(time.RFC3339)
		out.RiskAcknowledgedAt = &s
	}
	if !v.CheckedAt.IsZero() {
		out.CheckedAt = v.CheckedAt.UTC().Format(time.RFC3339)
	}
	out.Displays = v.Displays
	return out
}

func (h *ConnectionHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RiskAcknowledged bool `json:"risk_acknowledged"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil || (len(body) > 0 && json.Unmarshal(body, &req) != nil) {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	v, err := h.svc.Create(r.Context(), req.RiskAcknowledged)
	h.respond(w, http.StatusCreated, v, err)
}

func (h *ConnectionHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	out := make([]connectionJSON, len(items))
	for i, it := range items {
		out[i] = toJSON(it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *ConnectionHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := connectionID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.Get(r.Context(), id)
	h.respond(w, http.StatusOK, v, err)
}

func (h *ConnectionHandler) StartSession(w http.ResponseWriter, r *http.Request) {
	id, ok := connectionID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.StartSession(r.Context(), id)
	h.respond(w, http.StatusOK, v, err)
}

func (h *ConnectionHandler) StopSession(w http.ResponseWriter, r *http.Request) {
	id, ok := connectionID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.StopSession(r.Context(), id)
	h.respond(w, http.StatusOK, v, err)
}

func (h *ConnectionHandler) QR(w http.ResponseWriter, r *http.Request) {
	id, ok := connectionID(w, r)
	if !ok {
		return
	}
	qr, err := h.svc.QR(r.Context(), id)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"mimetype": qr.MIMEType, "data": qr.Data})
}

func (h *ManagementHandler) Providers(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.Providers(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *ManagementHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider         string            `json:"provider"`
		Inputs           map[string]string `json:"inputs"`
		RiskAcknowledged bool              `json:"risk_acknowledged"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil || json.Unmarshal(body, &req) != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	v, err := h.svc.Create(r.Context(), application.ConnectionCreateRequest{
		Provider: req.Provider, Inputs: req.Inputs, RiskAcknowledged: req.RiskAcknowledged,
	})
	h.respond(w, http.StatusCreated, v, err)
}

func (h *ManagementHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	out := make([]connectionJSON, len(items))
	for i, item := range items {
		out[i] = toJSON(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *ManagementHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := connectionID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.Get(r.Context(), id)
	h.respond(w, http.StatusOK, v, err)
}

func (h *ManagementHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := connectionID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.TestConnection(r.Context(), id)
	h.respond(w, http.StatusOK, v, err)
}

func (h *ManagementHandler) StartSession(w http.ResponseWriter, r *http.Request) {
	id, ok := connectionID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.StartSession(r.Context(), id)
	h.respond(w, http.StatusOK, v, err)
}

func (h *ManagementHandler) StopSession(w http.ResponseWriter, r *http.Request) {
	id, ok := connectionID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.StopSession(r.Context(), id)
	h.respond(w, http.StatusOK, v, err)
}

func (h *ManagementHandler) QR(w http.ResponseWriter, r *http.Request) {
	id, ok := connectionID(w, r)
	if !ok {
		return
	}
	qr, err := h.svc.QR(r.Context(), id)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"mimetype": qr.MIMEType, "data": qr.Data})
}

func (h *ManagementHandler) respond(w http.ResponseWriter, status int, v application.ConnectionView, err error) {
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, status, toJSON(v))
}

func (h *ManagementHandler) fail(w http.ResponseWriter, err error) {
	failConnection(w, err)
}

func connectionID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("connection_id"))
	if err != nil {
		http.Error(w, "invalid connection_id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func (h *ConnectionHandler) respond(w http.ResponseWriter, status int, v application.ConnectionView, err error) {
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, status, toJSON(v))
}

func (h *ConnectionHandler) fail(w http.ResponseWriter, err error) {
	failConnection(w, err)
}

func failConnection(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrConnForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, entitlements.ErrDisabled):
		http.Error(w, "this capability is disabled for your company", http.StatusForbidden)
	case errors.Is(err, application.ErrConnNotFound):
		http.Error(w, "connection not found", http.StatusNotFound)
	case errors.Is(err, application.ErrRiskNotAcknowledged):
		http.Error(w, "risk_acknowledged must be true for unofficial providers", http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrMetaNumberTaken):
		http.Error(w, "that WhatsApp number is already connected", http.StatusConflict)
	case errors.Is(err, application.ErrCredentialRejected):
		http.Error(w, "credential rejected by the provider: check the token and try again", http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrInvalidCredentials):
		http.Error(w, "invalid credentials: check the required fields", http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrProviderNotFound), errors.Is(err, application.ErrInvalidProviderInputs):
		http.Error(w, "invalid provider or inputs", http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrProviderUnavailable), errors.Is(err, ports.ErrNotConfigured):
		http.Error(w, "channel provider is not configured", http.StatusServiceUnavailable)
	case errors.Is(err, application.ErrQRUnavailable):
		http.Error(w, "no QR code available in the current session state", http.StatusConflict)
	case errors.Is(err, application.ErrPublicURLMissing):
		http.Error(w, "channel provider is not configured", http.StatusServiceUnavailable)
	case errors.Is(err, ports.ErrCapabilityNotSupported):
		http.Error(w, "operation is not supported by this provider", http.StatusConflict)
	case errors.Is(err, ports.ErrTransient), errors.Is(err, ports.ErrProviderUnavailable), errors.Is(err, ports.ErrRateLimited):
		http.Error(w, "channel provider unavailable", http.StatusBadGateway)
	case errors.Is(err, ports.ErrAuthentication), errors.Is(err, ports.ErrPermanent), errors.Is(err, ports.ErrSessionDisconnected):
		http.Error(w, "channel provider rejected the request", http.StatusBadGateway)
	default:
		log.Printf("channel connections: %v", err) // never includes secrets: errors carry no credential material
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
