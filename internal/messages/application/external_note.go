package application

import (
	"context"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/messages/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ExternalNoteWriter records an internal note for an external application (ADR-0053). It is deliberately its own type, not a mode of
// ExternalSender: nothing here ever calls a provider-facing store method, so adding a new way to send a real message can never accidentally
// also cover notes, and a bug in note-writing can never accidentally queue a real delivery.
type ExternalNoteWriter struct{ store ports.ExternalNoteStore }

func NewExternalNoteWriter(store ports.ExternalNoteStore) *ExternalNoteWriter {
	return &ExternalNoteWriter{store: store}
}

// ExternalNote is one note to record.
type ExternalNote struct {
	InstallationID uuid.UUID
	ConversationID uuid.UUID
	ControlVersion int64
	Text           string
}

func (s *ExternalNoteWriter) Note(ctx context.Context, n ExternalNote) (*ports.QueuedMessage, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.Source != tenancydomain.AccessSourceSystem || n.InstallationID == uuid.Nil {
		return nil, ErrForbidden // only the external write path, in a system session, records a note
	}
	if n.ConversationID == uuid.Nil {
		return nil, ErrNotFound
	}
	text := strings.TrimSpace(n.Text)
	if text == "" || utf8.RuneCountInString(text) > MaxNoteRunes {
		return nil, ErrInvalidNote
	}
	msg, err := s.store.InsertNote(ctx, n.InstallationID, n.ConversationID, n.ControlVersion, text)
	if err != nil {
		return nil, err
	}
	log.Printf("messages: accepted external note message_id=%s tenant_id=%s conversation_id=%s", msg.ID, tc.TenantID, n.ConversationID)
	return msg, nil
}
