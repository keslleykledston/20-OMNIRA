package application_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/contacts/application"
	contactdomain "github.com/omnira/omnira/internal/contacts/domain"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

type fakeRepository struct {
	got *contactdomain.Contact
}

func (r *fakeRepository) Store(context.Context, *contactdomain.Contact) error { return nil }
func (r *fakeRepository) FindByID(context.Context, uuid.UUID) (*contactdomain.Contact, error) {
	return nil, nil
}
func (r *fakeRepository) FindByPhone(context.Context, string) (*contactdomain.Contact, error) {
	return nil, nil
}
func (r *fakeRepository) List(context.Context, int, int) ([]*contactdomain.Contact, error) {
	return nil, nil
}
func (r *fakeRepository) Update(context.Context, *contactdomain.Contact) error { return nil }
func (r *fakeRepository) UpsertByPhone(_ context.Context, contact *contactdomain.Contact) (*contactdomain.Contact, error) {
	r.got = contact
	return contact, nil
}

func TestUpsertByPhoneUsesTenantContextNotPayload(t *testing.T) {
	tenantID := uuid.New()
	tc, err := domain.NewTenantContext(tenantID, uuid.New(), domain.AccessSourceDirect)
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepository{}
	service := application.NewService(repo)
	ctx := domain.WithTenantContext(context.Background(), tc)
	contact, err := service.UpsertByPhone(ctx, "+5511999999999", "Ana")
	if err != nil || contact == nil || repo.got.TenantID != tenantID {
		t.Fatalf("unexpected contact: %+v, %v", contact, err)
	}
}

func TestUpsertByPhoneRequiresTenantContext(t *testing.T) {
	service := application.NewService(&fakeRepository{})
	if _, err := service.UpsertByPhone(context.Background(), "+5511999999999", "Ana"); err == nil {
		t.Fatal("missing tenant context accepted")
	}
}
