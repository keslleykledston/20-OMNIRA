// Package domain — entidades do canal de comunicação (WhatsApp e, no
// futuro, outros canais). O domínio nunca conhece detalhes de um provider
// concreto (Meta Cloud, WAHA, etc.) — só fala com o contrato canônico em
// internal/channels/ports.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// ProviderKind — classificação obrigatória de todo provider (equivalente a
// "provider_type" na nomenclatura do PROMPT-CLAUDE-CODE.txt/
// WHATSAPP-PROVIDER-STRATEGY.md). A UI nunca deve esconder essa distinção
// do usuário ("Não esconder essa diferença").
type ProviderKind string

const (
	ProviderKindOfficial   ProviderKind = "official"
	ProviderKindUnofficial ProviderKind = "unofficial"
)

// Identificadores de provider conhecidos. NÃO é um enum fechado — o campo
// ChannelConnection.Provider continua string livre de propósito, para que
// adicionar um provider novo (WahaProvider, um BSP futuro, um provider de
// outro canal) nunca exija alterar o domínio. Estas constantes existem só
// para os poucos pontos do código (bootstrap/registry) que precisam se
// referir a um provider concreto por nome, evitando strings mágicas
// duplicadas.
const (
	ProviderMetaCloud          = "meta_cloud"
	ProviderWAHA               = "waha"
	ProviderFutureBSP          = "future_bsp"              // placeholder conceitual, sem adapter
	ProviderFutureSessionBased = "future_session_provider" // placeholder conceitual, sem adapter
)

// Channel — canal de comunicação. Hoje só "whatsapp" tem adapter; o campo
// existe desde já para que Email/Instagram/Telegram (fora do MVP atual,
// ver docs/product/MVP.md) não exijam uma nova versão de
// ChannelConnection quando chegarem.
type Channel string

const (
	ChannelWhatsApp Channel = "whatsapp"
)

// ConnectionStatus — estado operacional de uma ChannelConnection.
type ConnectionStatus string

const (
	ConnectionStatusPending      ConnectionStatus = "pending"
	ConnectionStatusActive       ConnectionStatus = "active"
	ConnectionStatusDegraded     ConnectionStatus = "degraded"
	ConnectionStatusDisconnected ConnectionStatus = "disconnected"
	ConnectionStatusFailed       ConnectionStatus = "failed"
	ConnectionStatusRevoked      ConnectionStatus = "revoked"
)

// Canonical names used by session-based providers. Aliases preserve D3.1
// persisted vocabulary: pending == connecting, active == connected.
const (
	ConnectionStatusConnecting = ConnectionStatusPending
	ConnectionStatusConnected  = ConnectionStatusActive
)

// Capability — operação que um provider pode declarar suporte. Nem todo
// provider suporta toda capability (ex.: um provider unofficial baseado em
// sessão pode não suportar templates aprovados pela Meta).
type Capability string

const (
	CapabilityText           Capability = "text"
	CapabilityMedia          Capability = "media"
	CapabilityTemplate       Capability = "template"
	CapabilityDeliveryStatus Capability = "delivery_status"
	CapabilityReadStatus     Capability = "read_status"
	CapabilityHealth         Capability = "health"
	CapabilitySessionPairing Capability = "session_pairing"
	CapabilityQRPairing      Capability = "qr_pairing"
	CapabilityInteractive    Capability = "interactive"
	CapabilityTyping         Capability = "typing" // extra além da lista mínima pedida; útil para providers session-based
	CapabilityVoice          Capability = "voice"  // extra além da lista mínima pedida
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
	ID       uuid.UUID
	TenantID uuid.UUID
	Channel  Channel // "whatsapp" hoje; outros canais reaproveitam esta struct sem migração de schema conceitual
	Provider string  // identificador do provider concreto, ex.: "meta_cloud", "waha"

	ProviderKind       ProviderKind
	ExternalAccountID  string
	ExternalNumberID   string
	ProviderSessionRef string // referência opaca persistida; nunca prova autorização
	Status             ConnectionStatus
	Capabilities       []Capability
	SecretRef          string // UUID de internal/channels/ports.CredentialStore — NUNCA o segredo em si (ver ADR-0009)
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
