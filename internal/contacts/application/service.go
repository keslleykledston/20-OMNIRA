package application

import (
	"context"
	"fmt"

	contactdomain "github.com/omnira/omnira/internal/contacts/domain"
	"github.com/omnira/omnira/internal/contacts/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type Service struct {
	repo ports.ContactRepository
}

func NewService(repo ports.ContactRepository) *Service {
	return &Service{repo: repo}
}

// UpsertByPhone is the identity-resolution entry point used by inbound
// channel adapters. Tenant ownership comes from TenantContext, not payload.
func (s *Service) UpsertByPhone(ctx context.Context, phoneE164, displayName string) (*contactdomain.Contact, error) {
	tenantContext, err := tenancydomain.FromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("contact: tenant context required: %w", err)
	}
	contact, err := contactdomain.NewContact(tenantContext.TenantID, phoneE164, displayName)
	if err != nil {
		return nil, err
	}
	return s.repo.UpsertByPhone(ctx, contact)
}
