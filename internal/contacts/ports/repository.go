package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/contacts/domain"
)

type ContactRepository interface {
	Store(ctx context.Context, contact *domain.Contact) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Contact, error)
	FindByPhone(ctx context.Context, phoneE164 string) (*domain.Contact, error)
	UpsertByPhone(ctx context.Context, contact *domain.Contact) (*domain.Contact, error)
	List(ctx context.Context, limit, offset int) ([]*domain.Contact, error)
	Update(ctx context.Context, contact *domain.Contact) error
}
