package adapters

import (
	"context"

	"github.com/google/uuid"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	"github.com/omnira/omnira/internal/servicectx/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type auditor struct {
	repo auditports.AuditEventRepository
}

// NewAuditor writes the module's actions to the append-only audit log inside the request's transaction. Metadata is ids, counts and enums only.
func NewAuditor(repo auditports.AuditEventRepository) ports.Auditor { return &auditor{repo: repo} }

func (a *auditor) Record(ctx context.Context, action, resourceType string, resourceID uuid.UUID, meta map[string]any) {
	if a == nil || a.repo == nil {
		return
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, auditdomain.AuditAction(action), auditdomain.ResourceType(resourceType), resourceID, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	for k, v := range meta {
		ev.SetMetadata(k, v)
	}
	_ = a.repo.Store(ctx, ev)
}
