package domain

import "time"

// MediaKind — tipo de mídia normalizado, independente do provider.
type MediaKind string

const (
	MediaKindImage    MediaKind = "image"
	MediaKindVideo    MediaKind = "video"
	MediaKindAudio    MediaKind = "audio"
	MediaKindDocument MediaKind = "document"
	MediaKindSticker  MediaKind = "sticker"
)

// DeliveryState — estado de entrega normalizado. O restante do sistema
// (Routing, Automation) nunca vê o vocabulário específico de um provider
// (ex.: "sent"/"delivered"/"read"/"failed" da Meta vs. o que WAHA usar).
type DeliveryState string

const (
	DeliveryStateQueued    DeliveryState = "queued"
	DeliveryStateSent      DeliveryState = "sent"
	DeliveryStateDelivered DeliveryState = "delivered"
	DeliveryStateRead      DeliveryState = "read"
	DeliveryStateFailed    DeliveryState = "failed"
)

// InboundMessage — mensagem recebida, já normalizada pelo adapter. Depois
// deste ponto o pipeline (Contact -> Conversation -> Ticket -> Routing)
// nunca mais toca o payload bruto do provider.
type InboundMessage struct {
	ProviderMessageID string // ID atribuído pelo provider — usado para dedupe
	ConnectionID      string
	FromE164          string // sempre em E.164, normalizado pelo adapter
	// SenderName é o nome de perfil informado pelo remetente (ex.: pushName do
	// WhatsApp). Serve como rótulo de exibição e NUNCA como identidade: quem
	// envia escolhe esse valor livremente. A identidade é FromE164.
	SenderName string
	Text       string
	Media             *InboundMedia
	Timestamp         time.Time
	RawProviderEvent  string // referência opcional ao payload bruto (auditoria), nunca segredo
}

// InboundMedia — referência a mídia recebida, ainda não baixada. O download
// em si (com allowlist/SSRF protection) é responsabilidade do adapter via
// ChannelProvider.DownloadMedia — nunca do domínio.
type InboundMedia struct {
	Kind      MediaKind
	MediaRef  string // referência opaca do provider (ex.: media ID da Meta)
	MimeType  string
	SizeBytes int64
}

// MediaContent — conteúdo de mídia já baixado e validado pelo adapter.
type MediaContent struct {
	Kind      MediaKind
	MimeType  string
	Data      []byte
	SizeBytes int64
}

// OutboundTextMessage — comando de envio de texto.
type OutboundTextMessage struct {
	ToE164 string
	Text   string
	// IdempotencyKey identifica este envio de forma única para o
	// provider/worker — reenviar a mesma chave nunca deve duplicar a
	// mensagem no destinatário.
	IdempotencyKey string
}

// OutboundMediaMessage — comando de envio de mídia.
type OutboundMediaMessage struct {
	ToE164         string
	Kind           MediaKind
	MediaURL       string // URL já validada/hospedada pelo OMNIRA, nunca repassada sem checagem
	Caption        string
	IdempotencyKey string
}

// OutboundTemplateMessage — comando de envio de template aprovado
// (obrigatório fora da janela de 24h em providers oficiais).
type OutboundTemplateMessage struct {
	ToE164         string
	TemplateName   string
	LanguageCode   string
	Params         []string
	IdempotencyKey string
}

// SendResult — resultado normalizado de um envio.
type SendResult struct {
	ProviderMessageID string
	State             DeliveryState
}

// DeliveryStatusUpdate — atualização de status vinda de um webhook de
// delivery/read receipt, já normalizada.
type DeliveryStatusUpdate struct {
	ProviderMessageID string
	State             DeliveryState
	OccurredAt        time.Time
	// Reason carrega o motivo de falha quando State == DeliveryStateFailed,
	// classificado pelo adapter (ver ErrTransient/ErrPermanent em ports).
	Reason string
}
