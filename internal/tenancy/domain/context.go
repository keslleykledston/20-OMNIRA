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
}

// AccessSource — origin da requisição (direto, hub, etc).
type AccessSource string

const (
	AccessSourceDirect AccessSource = "direct" // usuário tem membership direto no tenant
	AccessSourceHub    AccessSource = "hub"    // usuário acessa via hub grant
	AccessSourceSystem AccessSource = "system" // operação de sistema
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
	if source == "" {
		source = AccessSourceDirect
	}

	return &TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
		Source:   source,
	}, nil
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
