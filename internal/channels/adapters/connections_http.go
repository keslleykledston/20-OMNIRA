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
)

// ConnectionHandler serves WAHA connection/session management. Mount it
// behind authn + the tenant-session middleware; tenant and actor come only
// from the TenantContext, never from the URL or body.
type ConnectionHandler struct {
	svc *application.WahaConnectionService
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
	switch {
	case errors.Is(err, application.ErrConnForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, application.ErrConnNotFound):
		http.Error(w, "connection not found", http.StatusNotFound)
	case errors.Is(err, application.ErrRiskNotAcknowledged):
		http.Error(w, "risk_acknowledged must be true for unofficial providers", http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrQRUnavailable):
		http.Error(w, "no QR code available in the current session state", http.StatusConflict)
	case errors.Is(err, application.ErrPublicURLMissing), errors.Is(err, ports.ErrNotConfigured):
		http.Error(w, "channel provider is not configured", http.StatusServiceUnavailable)
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
