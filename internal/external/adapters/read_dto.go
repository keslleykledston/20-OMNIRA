package adapters

import (
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/external/domain"
)

// The external DTOs are minimal on purpose (Codex/OWASP API3): no user id, no e-mail, no name of a person who holds a conversation, no
// assigned_to_user_id, no provider URL, no tenant id. A holder is a KIND plus "is it me?".

type HolderDTO struct {
	Kind string `json:"kind"` // tenant_user | hub_user | crm_application | crm_agent_verified
	Mine bool   `json:"mine"` // this very installation
}

type ControlDTO struct {
	State   string     `json:"state"` // unassigned | held
	Holder  *HolderDTO `json:"holder,omitempty"`
	Version int64      `json:"version"`
	Origin  *string    `json:"origin,omitempty"`
	Since   *time.Time `json:"since,omitempty"`
}

// CanDTO is informational (it drives a button in the CRM). The command re-checks everything inside its own transaction.
type CanDTO struct {
	Claim    bool `json:"claim"`
	Reply    bool `json:"reply"`
	Release  bool `json:"release"`
	Transfer bool `json:"transfer"`
	Finalize bool `json:"finalize"`
}

type ChannelDTO struct {
	ConnectionID *uuid.UUID `json:"connection_id,omitempty"`
	Provider     string     `json:"provider,omitempty"`
}

// LastMessagePreviewDTO is the preview requested in OMNIRA_API_PENDENCIAS_CRM.md §3.3: enough for a conversation list to render without a
// second call per row, never the full message (no media, no sender) — that stays behind GET .../messages.
type LastMessagePreviewDTO struct {
	ID        uuid.UUID `json:"id"`
	Direction string    `json:"direction"` // inbound | outbound
	Type      string    `json:"type"`
	Body      string    `json:"body,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type ConversationDTO struct {
	ID            uuid.UUID              `json:"id"`
	Status        string                 `json:"status"` // open | closed
	ContactID     *uuid.UUID             `json:"contact_id,omitempty"`
	Channel       ChannelDTO             `json:"channel"`
	QueueID       *uuid.UUID             `json:"queue_id,omitempty"`
	Control       ControlDTO             `json:"control"`
	Can           CanDTO                 `json:"can"`
	LastMessageAt *time.Time             `json:"last_message_at,omitempty"`
	LastMessage   *LastMessagePreviewDTO `json:"last_message,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

type SenderDTO struct {
	Kind string `json:"kind"` // contact | agent | crm_application | automation
	Mine bool   `json:"mine,omitempty"`
}

type MediaDTO struct {
	ID        uuid.UUID `json:"id"`
	Kind      string    `json:"kind,omitempty"` // image | audio | video | document
	Mime      string    `json:"mime,omitempty"`
	SizeBytes int64     `json:"size_bytes"`
	Status    string    `json:"status"` // pending | clean | infected | rejected | source_gone | failed (only "clean" can be downloaded)
}

type MessageDTO struct {
	ID        uuid.UUID `json:"id"`
	Direction string    `json:"direction"` // inbound | outbound
	Type      string    `json:"type"`
	Body      string    `json:"body,omitempty"`
	Status    string    `json:"status"` // received | queued | sent | delivered | read | failed | uncertain
	Sender    SenderDTO `json:"sender"`
	Media     *MediaDTO `json:"media,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type ContactDTO struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	WhatsAppName string    `json:"whatsapp_name,omitempty"`
	Phone        string    `json:"phone_e164,omitempty"`
	Kind         string    `json:"kind"` // unclassified | customer | other
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type PageDTO[T any] struct {
	Items      []T    `json:"items"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// can computes the informational flags from the scopes and the state. Nothing here authorizes anything.
func can(p *domain.Principal, status string, c ControlDTO) CanDTO {
	mine := c.Holder != nil && c.Holder.Mine
	open := status == "open"
	return CanDTO{
		Claim:    open && c.State == "unassigned" && p.Has(domain.ScopeConversationClaim),
		Reply:    open && mine && p.Has(domain.ScopeMessageSend),
		Release:  open && mine && p.Has(domain.ScopeConversationClaim),
		Transfer: open && mine && p.Has(domain.ScopeConversationTransfer),
		Finalize: open && mine && p.Has(domain.ScopeConversationFinalize),
	}
}
