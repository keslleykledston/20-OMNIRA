package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// RealtimeEvent — evento de tempo real para conversas/mensagens
type RealtimeEvent struct {
	Type      string    `json:"type"`      // "conversation_created", "message_received", "status_changed"
	ID        uuid.UUID `json:"id"`        // conversation_id ou message_id
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`      // ConversationItem ou MessageItem ou DeliveryStatusUpdate
}

// RealtimeHandler — SSE realtime events com reauthorização e isolamento A/B
type RealtimeHandler struct {
	nc *nats.Conn
}

func NewRealtimeHandler(nc *nats.Conn) *RealtimeHandler {
	return &RealtimeHandler{nc: nc}
}

// StreamConversationEvents — SSE stream de eventos da conversa (tenant-scoped)
// Reautoriza a cada evento usando TenantContext
func (h *RealtimeHandler) StreamConversationEvents(w http.ResponseWriter, r *http.Request) {
	// Reauthorize on first request
	tenantID, err := requestTenant(r)
	if err != nil {
		http.Error(w, "tenant context required", http.StatusUnauthorized)
		return
	}

	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}

	// Configurar SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Flusher para enviar eventos imediatamente
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe a eventos NATS para esta conversa
	// Subject: inbox.events.<tenant_id>.<conversation_id>
	subject := fmt.Sprintf("inbox.events.%s.%s", tenantID, conversationID)

	sub, err := h.nc.Subscribe(subject, func(msg *nats.Msg) {
		// Reauthorize antes de enviar cada evento
		tc, authErr := tenancydomain.FromContext(r.Context())
		if authErr != nil || tc.TenantID != tenantID {
			// Silenciosamente desconecta se TenantContext mudou (raro, mas possível em multi-tab)
			return
		}

		var event RealtimeEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			fmt.Fprintf(w, "data: {\"error\": \"invalid event\"}\n\n")
			flusher.Flush()
			return
		}

		// SSE format: "data: {json}\n\n"
		data, _ := json.Marshal(event)
		fmt.Fprintf(w, "data: %s\n\n", string(data))
		flusher.Flush()
	})
	if err != nil {
		http.Error(w, "failed to subscribe", http.StatusInternalServerError)
		return
	}
	defer sub.Unsubscribe()

	// Keep connection open; client will reconnect on network error
	// This goroutine waits until context is cancelled (client disconnects)
	<-r.Context().Done()
}

// StreamInboxEvents — SSE stream de todos os eventos da tenant (conversations + messages)
// Reautoriza a cada evento
func (h *RealtimeHandler) StreamInboxEvents(w http.ResponseWriter, r *http.Request) {
	tenantID, err := requestTenant(r)
	if err != nil {
		http.Error(w, "tenant context required", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe a todos os eventos da tenant
	// Subject: inbox.events.<tenant_id>.*
	subject := fmt.Sprintf("inbox.events.%s.>", tenantID)

	sub, err := h.nc.Subscribe(subject, func(msg *nats.Msg) {
		// Reauthorize before sending each event
		tc, authErr := tenancydomain.FromContext(r.Context())
		if authErr != nil || tc.TenantID != tenantID {
			return
		}

		var event RealtimeEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			fmt.Fprintf(w, "data: {\"error\": \"invalid event\"}\n\n")
			flusher.Flush()
			return
		}

		data, _ := json.Marshal(event)
		fmt.Fprintf(w, "data: %s\n\n", string(data))
		flusher.Flush()
	})
	if err != nil {
		http.Error(w, "failed to subscribe", http.StatusInternalServerError)
		return
	}
	defer sub.Unsubscribe()

	<-r.Context().Done()
}

// PublishConversationEvent — publica evento de conversa via NATS
// Chamado por operações que criam/atualizam conversas
func PublishConversationEvent(nc *nats.Conn, tenantID, conversationID uuid.UUID, eventType string, data any) error {
	if nc == nil {
		return errors.New("nats connection required")
	}

	event := RealtimeEvent{
		Type:      eventType,
		ID:        conversationID,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	subject := fmt.Sprintf("inbox.events.%s.%s", tenantID, conversationID)
	return nc.Publish(subject, payload)
}

// PublishInboxEvent — publica evento genérico para inbox (todos os eventos)
func PublishInboxEvent(nc *nats.Conn, tenantID uuid.UUID, event RealtimeEvent) error {
	if nc == nil {
		return errors.New("nats connection required")
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	subject := fmt.Sprintf("inbox.events.%s.%s", tenantID, event.ID)
	return nc.Publish(subject, payload)
}
