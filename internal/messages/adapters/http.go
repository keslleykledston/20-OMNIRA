package adapters

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/messages/application"
)

// SendHandler serves outbound text. Mount it behind authn + the tenant-session
// middleware: tenant and sender come only from the TenantContext.
type SendHandler struct{ svc *application.Sender }

func NewSendHandler(svc *application.Sender) *SendHandler { return &SendHandler{svc: svc} }

type sendRequest struct {
	Text string `json:"text"`
}

type sendResponse struct {
	ID             uuid.UUID `json:"id"`
	ConversationID uuid.UUID `json:"conversation_id"`
	Direction      string    `json:"direction"`
	Body           string    `json:"body"`
	Status         string    `json:"status"`
	CreatedAt      string    `json:"created_at"`
}

func (h *SendHandler) Send(w http.ResponseWriter, r *http.Request) {
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}
	var req sendRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil || json.Unmarshal(body, &req) != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	res, err := h.svc.Send(r.Context(), conversationID, req.Text, r.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusAccepted
	if res.Replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(sendResponse{
		ID: res.Message.ID, ConversationID: res.Message.ConversationID, Direction: "outbound",
		Body: res.Message.Body, Status: res.Message.Status, CreatedAt: res.Message.CreatedAt.UTC().Format(time.RFC3339),
	})
}

type templateRequest struct {
	TemplateID uuid.UUID `json:"template_id"`
	Params     []string  `json:"params"`
}

// SendTemplate serves POST /inbox/conversations/{id}/template.
func (h *SendHandler) SendTemplate(w http.ResponseWriter, r *http.Request) {
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}
	var req templateRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil || json.Unmarshal(body, &req) != nil || req.TemplateID == uuid.Nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	res, err := h.svc.SendTemplate(r.Context(), conversationID, req.TemplateID, req.Params, r.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusAccepted
	if res.Replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(sendResponse{
		ID: res.Message.ID, ConversationID: res.Message.ConversationID, Direction: "outbound",
		Body: res.Message.Body, Status: res.Message.Status, CreatedAt: res.Message.CreatedAt.UTC().Format(time.RFC3339),
	})
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrTemplateUnsupported), errors.Is(err, application.ErrTemplateNotAllowed), errors.Is(err, application.ErrTemplateParams):
		http.Error(w, err.Error()[len("messages: "):], http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrForbidden), errors.Is(err, application.ErrNotAssignedToYou):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, application.ErrNotFound):
		http.Error(w, "conversation not found", http.StatusNotFound)
	case errors.Is(err, application.ErrConversationClosed):
		http.Error(w, "conversation is finalized: the contact's next message starts a new attendance", http.StatusConflict)
	case errors.Is(err, application.ErrUnassigned):
		http.Error(w, "conversation must be assigned before replying", http.StatusConflict)
	case errors.Is(err, application.ErrConversationChanged):
		http.Error(w, "conversation changed, retry", http.StatusConflict)
	case errors.Is(err, application.ErrWindowClosed):
		http.Error(w, "customer service window closed: free text is only accepted within 24 h of the customer's last message", http.StatusConflict)
	case errors.Is(err, application.ErrChannelUnavailable):
		http.Error(w, "conversation has no active text channel", http.StatusConflict)
	case errors.Is(err, application.ErrInvalidKey):
		http.Error(w, err.Error()[len("messages: "):], http.StatusBadRequest)
	case errors.Is(err, application.ErrInvalidText):
		http.Error(w, err.Error()[len("messages: "):], http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrIdempotencyMismatch):
		http.Error(w, err.Error()[len("messages: "):], http.StatusUnprocessableEntity)
	default:
		log.Printf("messages send: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}
