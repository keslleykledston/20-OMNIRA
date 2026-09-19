package adapters

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/routing/application"
)

// AssignHandler serves manual assign/unassign. It must be mounted behind the
// authn + tenant-session middleware: tenant and actor come exclusively from
// the TenantContext, never from the URL or body.
type AssignHandler struct{ svc *application.Assigner }

func NewAssignHandler(svc *application.Assigner) *AssignHandler { return &AssignHandler{svc: svc} }

type assignRequest struct {
	// AssigneeUserID is optional. Empty/absent = claim for myself. Targeting
	// another agent requires conversation.manage and is validated server-side.
	AssigneeUserID *uuid.UUID `json:"assignee_user_id"`
}

type assignResponse struct {
	ConversationID   uuid.UUID  `json:"conversation_id"`
	AssignedToUserID *uuid.UUID `json:"assigned_to_user_id"`
	Changed          bool       `json:"changed"`
}

func (h *AssignHandler) Assign(w http.ResponseWriter, r *http.Request) {
	id, ok := conversationID(w, r)
	if !ok {
		return
	}
	var req assignRequest
	if r.ContentLength != 0 {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
		if err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
		}
	}
	target := uuid.Nil
	if req.AssigneeUserID != nil {
		target = *req.AssigneeUserID
	}
	res, err := h.svc.Assign(r.Context(), id, target)
	respond(w, id, res, err)
}

func (h *AssignHandler) Unassign(w http.ResponseWriter, r *http.Request) {
	id, ok := conversationID(w, r)
	if !ok {
		return
	}
	res, err := h.svc.Unassign(r.Context(), id)
	respond(w, id, res, err)
}

func conversationID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func respond(w http.ResponseWriter, id uuid.UUID, res application.AssignResult, err error) {
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(assignResponse{ConversationID: id, AssignedToUserID: res.AssignedTo, Changed: res.Changed})
	case errors.Is(err, application.ErrForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, application.ErrNotFound):
		http.Error(w, "conversation not found", http.StatusNotFound)
	case errors.Is(err, application.ErrConflict):
		http.Error(w, "conversation already assigned to another agent", http.StatusConflict)
	case errors.Is(err, application.ErrInvalidAssignee):
		http.Error(w, "assignee is not an eligible agent of this tenant", http.StatusUnprocessableEntity)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}
