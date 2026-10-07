// Package ports define o contrato canônico que todo provider de canal
// (WhatsApp oficial via Meta Cloud, WhatsApp não oficial via WAHA/outros,
// e futuramente outros canais) deve implementar.
//
// Este é o "channel seam" do Wave D2 do kit de reuso DeskcommCRM: a
// interface é testada e estabilizada ANTES de qualquer implementação real
// (Meta Cloud entra no Wave D3). O domínio (Contact/Conversation/Ticket/
// Routing) nunca importa este pacote de adapters concretos — só esta
// interface.
//
// Regra de fronteira (docs/reference-kits/deskcomm-reuse/.../
// ARCHITECTURE-COMPATIBILITY.md): adapters traduzem formato e chamam o
// provider externo; nunca decidem regra de negócio (window policy,
// routing, retries, permissions, TenantContext, handoff ficam fora do
// adapter).
package ports

import (
	"context"
	"errors"

	"github.com/omnira/omnira/internal/channels/domain"
)

// Sentinel errors — o worker/application layer decide retry/backoff a
// partir destes, nunca inspecionando string de erro ou o tipo concreto do
// provider.
var (
	// ErrCapabilityNotSupported — o provider não implementa esta operação
	// (ex.: um provider unofficial sem suporte a template). O chamador
	// deveria ter checado ChannelConnection.HasCapability antes; retornar
	// este erro é a rede de segurança, não o caminho esperado.
	ErrCapabilityNotSupported = errors.New("channel: capability not supported by this provider")

	// ErrNotConfigured — credencial ausente/incompleta para esta conexão.
	// Deve ser um erro explícito, nunca um "sucesso" fantasma — o audit do
	// donor project documenta um bug de produção real causado exatamente
	// por isConfigured() checar env de forma síncrona e ficar
	// silenciosamente desatualizado (mensagens "queued" para sempre).
	ErrNotConfigured = errors.New("channel: connection not configured")

	// ErrInvalidWebhookSignature — a assinatura do webhook não bateu.
	// Nunca processar o payload quando este erro ocorre.
	ErrInvalidWebhookSignature = errors.New("channel: invalid webhook signature")

	// ErrTransient — falha classificada como retentável (timeout, 429,
	// 5xx, connection reset). O worker deve aplicar backoff exponencial.
	ErrTransient = errors.New("channel: transient error, retry with backoff")

	// ErrPermanent — falha classificada como definitiva (auth inválida,
	// payload inválido, forbidden, not found). Não retentar.
	ErrPermanent = errors.New("channel: permanent error, do not retry")

	// ErrMediaSourceNotAllowed — a URL/host de mídia retornada pelo
	// provider não está na allowlist. Proteção SSRF — nunca contornar
	// isto para "tentar mesmo assim".
	ErrMediaSourceNotAllowed = errors.New("channel: media source host not in allowlist")
	// ErrOutcomeUnknown — the request may or may not have been executed by the provider (timeout or 5xx AFTER the
	// request was sent) and the provider has no idempotency key to make a second attempt safe (Meta Cloud). The
	// delivery worker ends the message as "uncertain" and NEVER retries it automatically: a retry could deliver the
	// same message twice to a customer.
	ErrOutcomeUnknown = errors.New("channel: provider outcome unknown, not safe to retry automatically")

	// ErrSessionWindowClosed — the provider only accepts a pre-approved template now (WhatsApp's 24 h customer
	// service window is closed). Permanent for a free-text message: the person is told, nothing is retried.
	ErrSessionWindowClosed = errors.New("channel: customer service window closed, a template is required")

	ErrAuthentication      = errors.New("channel: provider authentication failed")
	ErrConfiguration       = errors.New("channel: provider configuration invalid")
	ErrRateLimited         = errors.New("channel: provider rate limited")
	ErrProviderUnavailable = errors.New("channel: provider unavailable")
	ErrSessionDisconnected = errors.New("channel: provider session disconnected")
	ErrUnknown             = errors.New("channel: provider error with unknown classification")

	// ErrProviderIDMismatch — the provider responded with a non-empty
	// message id that differs from the caller's reserved id (PILOT.4A1/
	// PILOT.4A2). Distinct from ErrUnknown: this is NOT safe to retry
	// automatically with the reserved id, because the provider may already
	// have dispatched something under the unexpected id — PILOT.4A0 only
	// proved deduplication for a repeated submission of the SAME id, not
	// for this anomaly. Callers must terminate as an unproven outcome
	// (never a confirmed failure — nothing proves the send didn't happen)
	// without automatically resending.
	ErrProviderIDMismatch = errors.New("channel: provider returned a different message id than reserved")
)

// ProviderMetadata — descrição estática de um provider, usada para exibir
// a distinção official/unofficial na UI (nunca escondida) e para telemetria
// de baixa cardinalidade (provider + provider_kind, nunca tenant_id como
// label).
type ProviderMetadata struct {
	Name         string // ex.: "meta_cloud", "waha"
	Kind         domain.ProviderKind
	Capabilities []domain.Capability
}

// ConnectMethod tells clients which onboarding flow a provider requires.
// Values are deliberately provider-neutral so the web UI can render a
// wizard from the descriptor without importing adapter-specific knowledge.
type ConnectMethod string

const (
	ConnectMethodQRSession     ConnectMethod = "qr_session"
	ConnectMethodCredentials   ConnectMethod = "credentials"
	ConnectMethodOAuthRedirect ConnectMethod = "oauth_redirect"
)

type ProviderInputDescriptor struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Pattern  string   `json:"pattern,omitempty"`
	Help     string   `json:"help,omitempty"`
	Example  string   `json:"example,omitempty"`
	Secret   bool     `json:"secret"`
	Options  []string `json:"options,omitempty"`
}

type ProviderDisplayDescriptor struct {
	Key           string `json:"key"`
	Label         string `json:"label"`
	ValueTemplate string `json:"value_template,omitempty"`
	Copyable      bool   `json:"copyable"`
	Sensitive     bool   `json:"sensitive"`
	Help          string `json:"help,omitempty"`
}

// ProviderDescriptor is the declarative, secret-free catalog entry exposed
// to tenant administrators. Availability describes server configuration;
// it never contains credentials or their values.
type ProviderDescriptor struct {
	ID                string                      `json:"id"`
	Name              string                      `json:"name"`
	Channel           domain.Channel              `json:"channel"`
	Kind              domain.ProviderKind         `json:"kind"`
	ConnectMethod     ConnectMethod               `json:"connect_method"`
	RiskNotice        string                      `json:"risk_notice,omitempty"`
	Capabilities      []domain.Capability         `json:"capabilities"`
	Enabled           bool                        `json:"enabled"`
	UnavailableReason string                      `json:"unavailable_reason,omitempty"`
	Inputs            []ProviderInputDescriptor   `json:"inputs"`
	Displays          []ProviderDisplayDescriptor `json:"displays"`
}

// WebhookVerificationRequest — dados brutos necessários para verificar a
// autenticidade de um webhook antes de processá-lo.
type WebhookVerificationRequest struct {
	Headers map[string]string
	Body    []byte
	// Query carrega parâmetros de verificação de handshake inicial (ex.:
	// hub.challenge da Meta), quando aplicável.
	Query map[string]string
}

// HealthStatus — resultado normalizado de um health check. Reachable=false
// significa erro de rede/timeout (não é o canal que caiu); Degraded=true
// com Reachable=true significa que o provider respondeu mas reportou um
// problema real (token inválido, número desconectado).
type HealthStatus struct {
	Reachable bool
	Degraded  bool
	Detail    string
}

// ChannelProvider — contrato canônico. Todo método aceita a
// ChannelConnection explicitamente (nunca um estado implícito/global) e
// deve tratar TenantID como não-opcional.
//
// Capacidades opcionais (SendTemplate, DownloadMedia, HandleDeliveryStatus
// quando o provider não suporta delivery receipts, etc.) retornam
// ErrCapabilityNotSupported em vez de o chamador precisar fazer uma type
// assertion para "descobrir" o que o provider suporta — o jeito correto de
// descobrir é ChannelConnection.HasCapability, consultado ANTES da
// chamada; o erro é a rede de segurança.
type ChannelProvider interface {
	// Metadata — descrição estática, nunca requer I/O.
	Metadata() ProviderMetadata

	// IsConfigured — a conexão tem credencial suficiente para operar.
	// Nunca deve ser a única defesa contra credencial ausente: Send* deve
	// retornar ErrNotConfigured mesmo que IsConfigured tenha sido pulado.
	IsConfigured(ctx context.Context, conn domain.ChannelConnection) bool

	// CheckHealth — reaproveita, quando possível, a mesma chamada usada
	// para validar a credencial (evita duas fontes de verdade divergentes
	// sobre "o canal está bem").
	CheckHealth(ctx context.Context, conn domain.ChannelConnection) (HealthStatus, error)

	// VerifyWebhook — valida autenticidade antes de qualquer parsing.
	// Deve retornar ErrInvalidWebhookSignature em caso de falha.
	VerifyWebhook(ctx context.Context, conn domain.ChannelConnection, req WebhookVerificationRequest) error

	// ParseInbound — traduz o payload já verificado para o formato
	// canônico. Nunca deve resolver tenant a partir do payload — a
	// resolução de ChannelConnection acontece antes, por uma fonte
	// confiável (ex.: phone_number_id do path/query do webhook).
	ParseInbound(ctx context.Context, conn domain.ChannelConnection, payload []byte) (*domain.InboundMessage, error)

	// SendText, SendMedia, SendTemplate — outbound. IdempotencyKey em cada
	// mensagem deve ser respeitada pelo adapter quando o provider suportar
	// deduplicação nativa; caso contrário, a idempotência é garantida pela
	// camada de aplicação antes de chamar o adapter.
	SendText(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundTextMessage) (*domain.SendResult, error)
	SendMedia(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundMediaMessage) (*domain.SendResult, error)
	SendTemplate(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundTemplateMessage) (*domain.SendResult, error)

	// NewMessageID reserves a stable, provider-generated message id with no
	// delivery side effect (PILOT.4A1) — the caller persists it durably
	// BEFORE calling SendText and reuses the exact same id on every
	// retry/redelivery, so a crash between a successful send and recording
	// that success can never cause a second visible delivery on a provider
	// that deduplicates by this id. A provider without this capability
	// (e.g. one that only assigns its own message id in the send response)
	// returns ErrCapabilityNotSupported, same as SendMedia/SendTemplate do
	// today — callers fall back to not reserving an id ahead of time.
	NewMessageID(ctx context.Context, conn domain.ChannelConnection) (string, error)

	// DownloadMedia — o adapter é responsável por validar a URL/host
	// devolvida pelo provider contra uma allowlist antes de baixar
	// (proteção SSRF); deve retornar ErrMediaSourceNotAllowed quando a
	// origem não é confiável.
	DownloadMedia(ctx context.Context, conn domain.ChannelConnection, media domain.InboundMedia) (*domain.MediaContent, error)

	// HandleDeliveryStatus — traduz um payload de delivery/read receipt
	// para o formato canônico.
	HandleDeliveryStatus(ctx context.Context, conn domain.ChannelConnection, payload []byte) (*domain.DeliveryStatusUpdate, error)
}

// InteractiveSender is an optional provider capability: reply buttons and lists. A provider without it (WAHA) is not an
// error for the caller: the same menu is sent as numbered text.
type InteractiveSender interface {
	SendInteractive(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundInteractiveMessage) (*domain.SendResult, error)
}
