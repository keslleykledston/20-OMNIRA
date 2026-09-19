// Package domain — entidades do canal de comunicação (WhatsApp e, no
// futuro, outros canais). O domínio nunca conhece detalhes de um provider
// concreto (Meta Cloud, WAHA, etc.) — só fala com o contrato canônico em
// internal/channels/ports.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// ProviderKind — classificação obrigatória de todo provider. A UI nunca
// deve esconder essa distinção do usuário (docs/WHATSAPP-PROVIDER-STRATEGY.md
// do kit de reuso DeskcommCRM: "Não esconder essa diferença").
type ProviderKind string

const (
	ProviderKindOfficial   ProviderKind = "official"
	ProviderKindUnofficial ProviderKind = "unofficial"
)

// ConnectionStatus — estado operacional de uma ChannelConnection.
type ConnectionStatus string

const (
	ConnectionStatusPending      ConnectionStatus = "pending"
	ConnectionStatusActive       ConnectionStatus = "active"
	ConnectionStatusDegraded     ConnectionStatus = "degraded"
	ConnectionStatusDisconnected ConnectionStatus = "disconnected"
	ConnectionStatusRevoked      ConnectionStatus = "revoked"
)

// Capability — operação que um provider pode declarar suporte. Nem todo
// provider suporta toda capability (ex.: um provider unofficial baseado em
// sessão pode não suportar templates aprovados pela Meta).
type Capability string

const (
	CapabilityText                Capability = "text"
	CapabilityMedia               Capability = "media"
	CapabilityTemplate            Capability = "template"
	CapabilityDeliveryStatus      Capability = "delivery_status"
	CapabilityReadStatus          Capability = "read_status"
	CapabilityTyping              Capability = "typing"
	CapabilitySessionPairing      Capability = "session_pairing"
	CapabilityQRPairing           Capability = "qr_pairing"
	CapabilityVoice               Capability = "voice"
	CapabilityInteractiveMessages Capability = "interactive_messages"
)

// ChannelConnection — credencial/configuração de canal pertencente a
// exatamente um Tenant.
//
// TenantID NUNCA é opcional. O audit do donor project (DeskcommCRM, ver
// docs/research/deskcomm/REUSE-AUDIT.md, entrada "ChannelAdapter /
// ChannelProvider") documenta um bug de produção real (issue #236) causado
// por um campo de tenant scope opcional que permitiu resolver credencial
// sem checar a organização — mensagem saiu pela conta errada. Aqui o campo
// é obrigatório por construção (NewChannelConnection valida) e nunca pode
// ser deixado como zero-value silenciosamente.
type ChannelConnection struct {
	ID                 uuid.UUID
	TenantID           uuid.UUID
	Provider           string // identificador do provider concreto, ex.: "meta_cloud", "waha"
	ProviderKind       ProviderKind
	ExternalAccountID  string
	ExternalNumberID   string
	Status             ConnectionStatus
	Capabilities       []Capability
	SecretRef          string // referência a um secret (vault/KMS), NUNCA o secret em si
	RiskAcknowledgedAt *time.Time
	RiskAcknowledgedBy *uuid.UUID
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// HasCapability — verifica se esta conexão declara suporte a uma capability.
// Preferir isto a checar o tipo concreto do provider.
func (c *ChannelConnection) HasCapability(cap Capability) bool {
	for _, existing := range c.Capabilities {
		if existing == cap {
			return true
		}
	}
	return false
}

// IsUnofficial — atalho para a checagem de risco mais comum na UI/regras
// de produto (aviso de risco, aceite auditado, etc.).
func (c *ChannelConnection) IsUnofficial() bool {
	return c.ProviderKind == ProviderKindUnofficial
}

// RequiresRiskAcknowledgement — providers unofficial exigem aceite de
// risco explícito do admin do Tenant antes de ficarem "active" (ver
// docs/WHATSAPP-PROVIDER-STRATEGY.md: "exigir confirmação do admin do
// Tenant; registrar aceite").
func (c *ChannelConnection) RequiresRiskAcknowledgement() bool {
	return c.IsUnofficial() && c.RiskAcknowledgedAt == nil
}
