// Package entitlements holds the per-company capability switches of ADR-0038: which things the platform control plane
// has allowed a company to do. They are enforced on the SERVER (a disabled capability answers 403 whatever the UI shows);
// the table (migration 100) stores only decisions, and no row means enabled, so existing companies keep everything.
//
// Disabling a capability stops NEW use of it. It never deletes data, and it does not tear down what already runs (for
// example an existing WhatsApp session keeps its connection): the registry below says exactly what each key gates.
package entitlements

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

const (
	WhatsAppChannel     = "whatsapp_channel"
	ERPCRM              = "erp_crm"
	OutboundAttachments = "outbound_attachments"
	// ServiceProtocolNotice tells the customer, in the conversation, when the service protocol opens and ends (ADR-0049 addendum). It messages customers, so it is
	// the one switch that starts OFF: a company gets it only when an operator turns it on.
	ServiceProtocolNotice = "service_protocol_notice"
	// HubTicketStatus lets a Hub agent change the ERP status of the ticket of a conversation they hold. It starts ON (the owner's decision); the company can turn it off.
	HubTicketStatus = "hub_ticket_status"
	// SectorScope turns on the visibility by queue (ADR-0043 S3): an agent sees only the queues they are an active member of, their own conversations and the ones without a queue;
	// administrators and supervisors (conversation.read_all_queues) still see everything. It changes what people see, so it starts OFF and is the owner's call per company.
	SectorScope = "sector_scope"
)

// ErrDisabled: the company's operator has switched this capability off.
var ErrDisabled = errors.New("entitlements: capability is disabled for this company")

// Capability describes one switch for the control-plane UI. Gates says what the server really refuses when it is off.
type Capability struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Gates       string `json:"gates"`
	// DefaultOff marks a switch that starts OFF (no row = disabled). Every other switch starts on, so existing companies keep everything they have.
	DefaultOff bool `json:"default_off,omitempty"`
}

// Registry is the closed list of switches. Adding one means adding a gate in the code that enforces it.
var Registry = []Capability{
	{WhatsAppChannel, "Canal WhatsApp", "Conectar linhas de WhatsApp (WAHA e Meta).",
		"Criar nova conexão de WhatsApp. Conexões e sessões já existentes continuam.", false},
	{ERPCRM, "ERP / CRM", "Integrar um sistema de retaguarda (CRM/ERP).",
		"Criar nova integração de ERP/CRM. Integrações já existentes continuam.", false},
	{OutboundAttachments, "Anexos de saída", "Enviar arquivos pelo atendimento.",
		"Enviar e remover anexos no atendimento. Mensagens de texto continuam.", false},
	{ServiceProtocolNotice, "Aviso de protocolo ao cliente", "Avisar o cliente, na conversa, quando o protocolo de atendimento abre e quando encerra.",
		"O aviso de início e de fim do protocolo (as mensagens ao cliente). O protocolo em si continua abrindo e encerrando.", true},
	{HubTicketStatus, "Hub altera o status do chamado ERP", "Permitir que o atendente do Hub mude o status do chamado no ERP.",
		"A mudança de status do chamado ERP feita por quem atende pelo Hub (403). Membros da empresa, atualizar o chamado e abrir chamado não mudam.", false},
	{SectorScope, "Visibilidade por fila", "O atendente só vê as filas de que participa (e o que atende); administrador e supervisor continuam vendo tudo.",
		"O que cada pessoa enxerga: conversas de filas fora do alcance do atendente somem das listas e respondem 404 (rotas, mídia, chamados, tópicos, tempo real). Não apaga nem move nada; desligar devolve a visão completa.", true},
}

func Known(key string) bool {
	for _, c := range Registry {
		if c.Key == key {
			return true
		}
	}
	return false
}

// DefaultEnabled is the value of a switch while no decision exists: on, except the ones marked DefaultOff.
func DefaultEnabled(key string) bool {
	for _, c := range Registry {
		if c.Key == key {
			return !c.DefaultOff
		}
	}
	return false
}

// Checker reads the switches with the CALLER's own database session (RLS decides what it may read).
type Checker struct{ pool *pgxpool.Pool }

func NewChecker(pool *pgxpool.Pool) *Checker { return &Checker{pool: pool} }

// Enabled is the company's decision, or the switch's default when none exists (on, except the DefaultOff ones). An unknown key is never enabled.
func (c *Checker) Enabled(ctx context.Context, tenant uuid.UUID, capability string) (bool, error) {
	if !Known(capability) {
		return false, nil
	}
	var enabled bool
	err := platformdb.QuerierFromContext(ctx, c.pool).QueryRow(ctx,
		`SELECT enabled FROM tenant_entitlements WHERE tenant_id = $1 AND capability = $2`, tenant, capability).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultEnabled(capability), nil
	}
	return enabled, err
}

// Gate returns ErrDisabled when the capability is off. A read error fails closed.
func (c *Checker) Gate(ctx context.Context, tenant uuid.UUID, capability string) error {
	ok, err := c.Enabled(ctx, tenant, capability)
	if err != nil {
		return err
	}
	if !ok {
		return ErrDisabled
	}
	return nil
}

// Require wraps a handler that already runs behind the tenant session: the company comes from the resolved tenant
// context, never from the request.
func (c *Checker) Require(capability string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tc, err := tenancydomain.FromContext(r.Context())
			if err != nil || tc.TenantID == uuid.Nil {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if err := c.Gate(r.Context(), tc.TenantID, capability); err != nil {
				if errors.Is(err, ErrDisabled) {
					http.Error(w, "this capability is disabled for your company", http.StatusForbidden)
					return
				}
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
