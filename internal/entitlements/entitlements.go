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
)

// ErrDisabled: the company's operator has switched this capability off.
var ErrDisabled = errors.New("entitlements: capability is disabled for this company")

// Capability describes one switch for the control-plane UI. Gates says what the server really refuses when it is off.
type Capability struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Gates       string `json:"gates"`
}

// Registry is the closed list of switches. Adding one means adding a gate in the code that enforces it.
var Registry = []Capability{
	{WhatsAppChannel, "Canal WhatsApp", "Conectar linhas de WhatsApp (WAHA e Meta).",
		"Criar nova conexão de WhatsApp. Conexões e sessões já existentes continuam."},
	{ERPCRM, "ERP / CRM", "Integrar um sistema de retaguarda (CRM/ERP).",
		"Criar nova integração de ERP/CRM. Integrações já existentes continuam."},
	{OutboundAttachments, "Anexos de saída", "Enviar arquivos pelo atendimento.",
		"Enviar e remover anexos no atendimento. Mensagens de texto continuam."},
}

func Known(key string) bool {
	for _, c := range Registry {
		if c.Key == key {
			return true
		}
	}
	return false
}

// Checker reads the switches with the CALLER's own database session (RLS decides what it may read).
type Checker struct{ pool *pgxpool.Pool }

func NewChecker(pool *pgxpool.Pool) *Checker { return &Checker{pool: pool} }

// Enabled is true when no decision exists (default on) or the decision is "enabled". An unknown key is never enabled.
func (c *Checker) Enabled(ctx context.Context, tenant uuid.UUID, capability string) (bool, error) {
	if !Known(capability) {
		return false, nil
	}
	var enabled bool
	err := platformdb.QuerierFromContext(ctx, c.pool).QueryRow(ctx,
		`SELECT enabled FROM tenant_entitlements WHERE tenant_id = $1 AND capability = $2`, tenant, capability).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
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
