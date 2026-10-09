package domain

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// TenantContext — contexto imutável de uma operação tenant-aware.
// Construído APÓS autenticação + autorização, nunca antes.
// Nenhuma parte do código aceita tenant_id do request como autoridade.
type TenantContext struct {
	TenantID uuid.UUID
	ActorID  uuid.UUID // UserID humano; vazio somente quando Source=system
	Source   AccessSource

	// Hub delegation (populated only if Source=hub)
	HubID             *uuid.UUID // which hub (if hub access)
	ServiceContractID *uuid.UUID // which contract
	EffectiveGrantID  *uuid.UUID // which grant
	WorkPoolID        *uuid.UUID // grant's work pool, when it has one
	CanReply          bool       // hub access only: the grant allows claiming and replying, not just reading
	// Permissions — delegated serving only (AccessSourceHubServe): the permission keys the grant AND the contract's ceiling hold at the
	// moment the request was admitted. A snapshot for display and logging; every decision is still asked of the database
	// (actor_has_permission), live.
	Permissions []string

	// Audit trail
	CorrelationID string // trace requests across system
}

// AccessSource — origin da requisição (direto, hub, etc).
type AccessSource string

const (
	AccessSourceDirect AccessSource = "direct" // usuário tem membership direto no tenant
	AccessSourceHub    AccessSource = "hub"    // usuário acessa via hub grant
	AccessSourceSystem AccessSource = "system" // operação de sistema
	// AccessSourceHubManage — o Hub GERE (canais/integrações) a empresa, por contrato + papel/grant (ADR-0038 fase 3). É uma fonte
	// PRÓPRIA de propósito: todo código que exige AccessSourceDirect (envio de mensagem, tickets, roteamento...) continua recusando
	// este contexto; só os serviços de gestão de canal o aceitam, e cada permissão é revalidada no banco.
	AccessSourceHubManage AccessSource = "hub_manage"
	// AccessSourceHubServe — a Hub agent ATTENDS an instance with its operational context (ADR-0040): the request declared
	// `X-Omnira-Acting-As: hub:<id>` and the server proved hub -> contract -> grant -> permissions. Own source on purpose, like HubManage:
	// code that requires AccessSourceDirect keeps refusing it, and it never carries a member's privileges.
	AccessSourceHubServe AccessSource = "hub_serve"
)

// NewTenantContext — factory. Exige tenant_id e, para acesso humano,
// actor_id válido. Operações de sistema são não humanas e usam actor_id vazio.
// Nenhum campo é aceito do request ou do cliente.
func NewTenantContext(tenantID, actorID uuid.UUID, source AccessSource) (*TenantContext, error) {
	if tenantID == uuid.Nil {
		return nil, errors.New("tenant_id is required")
	}
	if actorID == uuid.Nil && source != AccessSourceSystem {
		return nil, errors.New("actor_id is required")
	}
	// Hub access carries grant metadata and is built only by NewHubTenantContext; accepting it here
	// would mint a "hub" context with no contract or grant behind it.
	if source != AccessSourceDirect && source != AccessSourceSystem {
		return nil, errors.New("unsupported access source for NewTenantContext: " + string(source))
	}

	return &TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
		Source:   source,
	}, nil
}

// NewHubTenantContext — extended factory for Hub-delegated access.
// Constructs a TenantContext with Hub grant metadata.
// All IDs must be pre-validated server-side (not from client).
func NewHubTenantContext(
	tenantID, actorID, hubID, contractID, grantID uuid.UUID,
	correlationID string,
) (*TenantContext, error) {
	if tenantID == uuid.Nil || actorID == uuid.Nil || hubID == uuid.Nil {
		return nil, errors.New("tenant_id, actor_id, hub_id required for hub access")
	}
	if contractID == uuid.Nil || grantID == uuid.Nil {
		return nil, errors.New("contract_id, grant_id required for hub access")
	}

	return &TenantContext{
		TenantID:          tenantID,
		ActorID:           actorID,
		Source:            AccessSourceHub,
		HubID:             &hubID,
		ServiceContractID: &contractID,
		EffectiveGrantID:  &grantID,
		CorrelationID:     correlationID,
	}, nil
}

// NewHubManageTenantContext — contexto de GESTÃO delegada pelo Hub. grantID é nulo para o administrador do Hub (que gere pelo
// papel, sem grant). O escopo (canais, integrações...) NÃO vai no contexto: cada permissão pergunta ao banco, ao vivo.
func NewHubManageTenantContext(tenantID, actorID, hubID, contractID uuid.UUID, grantID *uuid.UUID, correlationID string) (*TenantContext, error) {
	if tenantID == uuid.Nil || actorID == uuid.Nil || hubID == uuid.Nil || contractID == uuid.Nil {
		return nil, errors.New("tenant_id, actor_id, hub_id, contract_id required for hub management")
	}
	return &TenantContext{
		TenantID: tenantID, ActorID: actorID, Source: AccessSourceHubManage,
		HubID: &hubID, ServiceContractID: &contractID, EffectiveGrantID: grantID, CorrelationID: correlationID,
	}, nil
}

// NewHubServeTenantContext — context of a Hub agent attending an instance (ADR-0040). Every ID comes from the server's own validation
// (lock_served_tenant), never from the client. permissions is a snapshot (see TenantContext.Permissions).
func NewHubServeTenantContext(tenantID, actorID, hubID, contractID, grantID uuid.UUID, permissions []string, correlationID string) (*TenantContext, error) {
	if tenantID == uuid.Nil || actorID == uuid.Nil || hubID == uuid.Nil || contractID == uuid.Nil || grantID == uuid.Nil {
		return nil, errors.New("tenant_id, actor_id, hub_id, contract_id, grant_id required for delegated serving")
	}
	return &TenantContext{
		TenantID: tenantID, ActorID: actorID, Source: AccessSourceHubServe,
		HubID: &hubID, ServiceContractID: &contractID, EffectiveGrantID: &grantID,
		Permissions: append([]string(nil), permissions...), CorrelationID: correlationID,
	}, nil
}

// ActingAs — the context this request acts in, as the audit trail names it.
func (tc *TenantContext) ActingAs() string {
	if tc != nil && tc.Source == AccessSourceHubServe && tc.HubID != nil {
		return ActingAs{HubID: *tc.HubID}.String()
	}
	return "member"
}

// MayManageAsTenant — o contexto pode chegar a um serviço de gestão de canal? Direto (membership) ou gestão delegada pelo Hub com
// hub e contrato conhecidos. Qualquer outra fonte (inclusive o Hub de leitura/resposta) é recusada.
func (tc *TenantContext) MayManageAsTenant() bool {
	if tc == nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		return false
	}
	switch tc.Source {
	case AccessSourceDirect:
		return true
	case AccessSourceHubManage:
		return tc.HubID != nil && *tc.HubID != uuid.Nil && tc.ServiceContractID != nil && *tc.ServiceContractID != uuid.Nil
	}
	return false
}

// ContextKey — chave para armazenar TenantContext no context.
type ContextKey string

const TenantContextKey ContextKey = "tenant_context"

// WithTenantContext — armazena TenantContext no context.
func WithTenantContext(ctx context.Context, tc *TenantContext) context.Context {
	return context.WithValue(ctx, TenantContextKey, tc)
}

// FromContext — recupera TenantContext do context.
func FromContext(ctx context.Context) (*TenantContext, error) {
	tc, ok := ctx.Value(TenantContextKey).(*TenantContext)
	if !ok {
		return nil, errors.New("tenant_context not found in context")
	}
	return tc, nil
}
