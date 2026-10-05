package adapters

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// DirectoryHandler lists the tenant's WhatsApp lines an attendant can talk through (the channel selector of the
// Inbox). Any tenant member may read it; it is a directory, not management: no secret, no credential field and no
// session detail ever leaves it, only what is needed to recognise a line.
type DirectoryHandler struct {
	conns       ports.ChannelConnectionRepository
	credentials ports.CredentialStore
}

func NewDirectoryHandler(conns ports.ChannelConnectionRepository, credentials ports.CredentialStore) *DirectoryHandler {
	return &DirectoryHandler{conns: conns, credentials: credentials}
}

type directoryItem struct {
	ID           uuid.UUID `json:"id"`
	Provider     string    `json:"provider"`
	ProviderKind string    `json:"provider_kind"`
	// Label is a human name for the line: the number when known.
	Label string `json:"label"`
	// Number is the line's phone number in display form, when known.
	Number string `json:"number,omitempty"`
	Status string `json:"status"`
	// CanSendText: active and able to send text; an inactive line is listed (history stays readable) but not offered.
	CanSendText bool `json:"can_send_text"`
	// WindowRequired: the provider only accepts free text within 24 h of the customer's last message.
	WindowRequired bool `json:"window_required"`
}

func (h *DirectoryHandler) lineNumber(ctx context.Context, c *domain.ChannelConnection) string {
	switch c.Provider {
	case domain.ProviderWAHA:
		if digits := onlyDigits(c.ExternalAccountID); digits != "" {
			return "+" + digits
		}
	case domain.ProviderMetaCloud:
		if c.SecretRef != "" {
			if cred, err := h.credentials.Resolve(ctx, c.SecretRef); err == nil {
				return strings.TrimSpace(cred.Fields["display_phone_number"])
			}
		}
	}
	return ""
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (h *DirectoryHandler) List(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	all, err := h.conns.FindByTenant(r.Context(), tc.TenantID)
	if err != nil {
		http.Error(w, "failed to list channels", http.StatusInternalServerError)
		return
	}
	items := []directoryItem{}
	for _, c := range all {
		if c.Channel != domain.ChannelWhatsApp || !c.HasCapability(domain.CapabilityText) {
			continue // CRM/ERP connections are not conversation lines
		}
		number := h.lineNumber(r.Context(), c)
		name := "WhatsApp"
		if c.ProviderKind == domain.ProviderKindOfficial {
			name = "WhatsApp oficial"
		}
		label := name
		if number != "" {
			label = name + " · " + number
		}
		items = append(items, directoryItem{
			ID: c.ID, Provider: c.Provider, ProviderKind: string(c.ProviderKind), Label: label, Number: number,
			Status: string(c.Status), CanSendText: c.Status == domain.ConnectionStatusActive,
			WindowRequired: c.Provider == domain.ProviderMetaCloud,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
