package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/routing/application"
)

// ParticipantHandler serves invite, transfer, accept, reject, leave endpoints.
type ParticipantHandler struct {
	svc *application.ParticipantService
}

func NewParticipantHandler(svc *application.ParticipantService) *ParticipantHandler {
	return &ParticipantHandler{svc: svc}
}

type inviteRequest struct {
	TargetUserID uuid.UUID `json:"target_user_id"`
}

type inviteResponse struct {
	ParticipantID uuid.UUID `json:"participant_id"`
	TargetUserID  uuid.UUID `json:"target_user_id"`
	Role          string    `json:"role"`
	Changed       bool      `json:"changed"`
}

// Invite convida um técnico para co-atender.
// POST /conversations/{id}/invite
func (h *ParticipantHandler) Invite(w http.ResponseWriter, r *http.Request) {
	id, ok := conversationIDFromPath(w, r)
	if !ok {
		return
	}

	var req inviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.TargetUserID == uuid.Nil {
		http.Error(w, "target_user_id required", http.StatusBadRequest)
		return
	}

	res, err := h.svc.Invite(r.Context(), id, req.TargetUserID)
	if err != nil {
		respondParticipantError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(inviteResponse{
		ParticipantID: res.ParticipantID,
		TargetUserID:  res.TargetUserID,
		Role:          string(res.Role),
		Changed:       res.Changed,
	})
}

type transferRequest struct {
	TargetUserID uuid.UUID `json:"target_user_id"`
}

type transferResponse struct {
	PreviousAssignee *uuid.UUID `json:"previous_assignee,omitempty"`
	NewAssignee      *uuid.UUID `json:"new_assignee,omitempty"`
	Changed          bool       `json:"changed"`
}

// Transfer transfere a conversation para outro técnico.
// POST /conversations/{id}/transfer
func (h *ParticipantHandler) Transfer(w http.ResponseWriter, r *http.Request) {
	id, ok := conversationIDFromPath(w, r)
	if !ok {
		return
	}

	var req transferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.TargetUserID == uuid.Nil {
		http.Error(w, "target_user_id required", http.StatusBadRequest)
		return
	}

	res, err := h.svc.Transfer(r.Context(), id, req.TargetUserID)
	if err != nil {
		respondParticipantError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(transferResponse{
		PreviousAssignee: res.PreviousAssignee,
		NewAssignee:      res.NewAssignee,
		Changed:          res.Changed,
	})
}

type acceptResponse struct {
	ParticipantID uuid.UUID `json:"participant_id"`
	JoinedAt      *string   `json:"joined_at,omitempty"`
}

// AcceptInvite aceita um convite para co-atender.
// POST /conversations/{id}/accept-invite
func (h *ParticipantHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := conversationIDFromPath(w, r)
	if !ok {
		return
	}

	res, err := h.svc.AcceptInvite(r.Context(), id)
	if err != nil {
		respondParticipantError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	resp := acceptResponse{ParticipantID: res.ParticipantID}
	if res.JoinedAt != nil {
		joined := res.JoinedAt.Format("2006-01-02T15:04:05Z")
		resp.JoinedAt = &joined
	}
	json.NewEncoder(w).Encode(resp)
}

// RejectInvite rejeita um convite pendente.
// POST /conversations/{id}/reject-invite
func (h *ParticipantHandler) RejectInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := conversationIDFromPath(w, r)
	if !ok {
		return
	}

	if err := h.svc.RejectInvite(r.Context(), id); err != nil {
		respondParticipantError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Leave remove um co-attendee do atendimento.
// POST /conversations/{id}/leave
func (h *ParticipantHandler) Leave(w http.ResponseWriter, r *http.Request) {
	id, ok := conversationIDFromPath(w, r)
	if !ok {
		return
	}

	if err := h.svc.Leave(r.Context(), id); err != nil {
		respondParticipantError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func conversationIDFromPath(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func respondParticipantError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		http.Error(w, "request timeout", http.StatusRequestTimeout)
		return
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, "forbidden"):
		http.Error(w, "forbidden", http.StatusForbidden)
	case strings.Contains(msg, "not found"):
		http.Error(w, "not found", http.StatusNotFound)
	case strings.Contains(msg, "not an eligible agent"):
		http.Error(w, "target is not an eligible agent", http.StatusUnprocessableEntity)
	case strings.Contains(msg, "pending invitations"):
		http.Error(w, "only pending invitations can be accepted", http.StatusConflict)
	case strings.Contains(msg, "co-attendees can"):
		http.Error(w, msg, http.StatusConflict)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}
