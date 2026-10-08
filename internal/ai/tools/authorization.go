package tools

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// ToolExecutionContext wraps effective tenant context for tool authorization (Phase 10)
type ToolExecutionContext struct {
	EffectiveTenantContext *domain.TenantContext
	ConversationID         uuid.UUID
	ActorID                uuid.UUID
}

// Tool represents an AI tool that operates in tenant-scoped context
type Tool interface {
	// Name returns tool identifier (e.g., "create_service_order")
	Name() string

	// Description returns what the tool does
	Description() string

	// RequiredCapability returns the integration capability needed
	// (e.g., "SERVICE_ORDER_CREATE")
	RequiredCapability() string

	// Execute runs the tool ONLY in effective tenant context
	// Never accepts tenant_id, integration_id, or credentials from parameters
	// All are resolved server-side from ToolExecutionContext
	Execute(ctx context.Context, execCtx *ToolExecutionContext, params map[string]interface{}) (interface{}, error)
}

// Example tools (Phase 10+)
// - create_service_order (tenant + integration from context)
// - query_billing (tenant + integration from context)
// - run_network_diagnostic (tenant + integration from context)
// - create_incident (tenant + integration from context)
