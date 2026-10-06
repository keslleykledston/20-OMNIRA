package adapters

import (
	"context"

	"github.com/google/uuid"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	"github.com/omnira/omnira/internal/flows/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ResourceFlow is the audit resource type of flows (a plain conversion: the shared audit domain file stays untouched).
const ResourceFlow auditdomain.ResourceType = "flow"

type auditor struct {
	repo auditports.AuditEventRepository
}

// NewAuditor writes flow administrative actions to the append-only audit log, inside the request's transaction.
func NewAuditor(repo auditports.AuditEventRepository) ports.Auditor { return &auditor{repo: repo} }

func (a *auditor) Record(ctx context.Context, action string, flowID uuid.UUID, meta map[string]any) {
	if a == nil || a.repo == nil {
		return
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, auditdomain.AuditAction(action), ResourceFlow, flowID, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	for k, v := range meta {
		ev.SetMetadata(k, v)
	}
	_ = a.repo.Store(ctx, ev)
}
