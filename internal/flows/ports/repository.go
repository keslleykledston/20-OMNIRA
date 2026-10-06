// Package ports holds the interfaces the flows application depends on. Every method runs inside the caller's tenant
// session (platformdb.WithTenantSession): the tenant is read from the TenantContext, never from an argument.
package ports

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/domain"
)

type ListFilter struct {
	Status domain.FlowStatus
	Type   domain.FlowType
	Limit  int
	Offset int
}

// Settings are the resolver-facing attributes of a flow, edited separately from the definition.
type Settings struct {
	Priority      int
	IsDefault     bool
	TriggerFilter domain.TriggerFilter
	RestartPolicy domain.RestartPolicy
}

type FlowRepository interface {
	CreateFlow(ctx context.Context, f *domain.Flow) error
	GetFlow(ctx context.Context, id uuid.UUID) (*domain.Flow, error)
	GetFlowBySlug(ctx context.Context, slug string) (*domain.Flow, error)
	ListFlows(ctx context.Context, filter ListFilter) ([]*domain.Flow, error)
	// SaveDraft is optimistic: it only applies when the stored revision equals expectedRevision.
	SaveDraft(ctx context.Context, id uuid.UUID, expectedRevision int, name, description string, definition json.RawMessage) (*domain.Flow, error)
	UpdateSettings(ctx context.Context, id uuid.UUID, s Settings) (*domain.Flow, error)
	// Publish snapshots the draft at expectedRevision into a new immutable version and makes it the active one,
	// atomically and serialized per flow. It never publishes content other than the revision the caller validated.
	Publish(ctx context.Context, id uuid.UUID, expectedRevision int, note string, by *uuid.UUID) (*domain.FlowVersion, error)
	// ActivateVersion points new runs at an existing version (rollback); history is untouched.
	ActivateVersion(ctx context.Context, flowID uuid.UUID, version int) (*domain.Flow, *domain.FlowVersion, error)
	Archive(ctx context.Context, id uuid.UUID) error
	GetVersion(ctx context.Context, id uuid.UUID) (*domain.FlowVersion, error)
	GetVersionByNumber(ctx context.Context, flowID uuid.UUID, version int) (*domain.FlowVersion, error)
	ListVersions(ctx context.Context, flowID uuid.UUID, limit, offset int) ([]*domain.FlowVersion, error)
	RecordPackInstallation(ctx context.Context, p *domain.PackInstallation) error
	RecordTemplateInstallation(ctx context.Context, t *domain.TemplateInstallation) error
	ListTemplateInstallations(ctx context.Context, flowID uuid.UUID) ([]*domain.TemplateInstallation, error)
}
