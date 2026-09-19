package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/routing/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type claimRepo struct {
	claimed        bool
	gotUser        uuid.UUID
	roundRobinUser uuid.UUID
}

func (r *claimRepo) ClaimUnassigned(_ context.Context, _ uuid.UUID, userID uuid.UUID, _ string) (bool, error) {
	r.gotUser = userID
	return r.claimed, nil
}
func (r *claimRepo) AssignRoundRobin(_ context.Context, _ uuid.UUID, _ string) (uuid.UUID, bool, error) {
	return r.roundRobinUser, r.roundRobinUser != uuid.Nil, nil
}

func TestClaimOwnDerivesUserFromTenantContext(t *testing.T) {
	actor := uuid.New()
	tc, _ := tenancydomain.NewTenantContext(uuid.New(), actor, tenancydomain.AccessSourceDirect)
	repo := &claimRepo{claimed: true}
	if err := application.NewService(repo).ClaimOwn(tenancydomain.WithTenantContext(context.Background(), tc), uuid.New()); err != nil {
		t.Fatal(err)
	}
	if repo.gotUser != actor {
		t.Fatal("claim target was not derived from actor")
	}
}

func TestRoundRobinRequiresSystemContext(t *testing.T) {
	selected := uuid.New()
	system, _ := tenancydomain.NewTenantContext(uuid.New(), uuid.Nil, tenancydomain.AccessSourceSystem)
	got, err := application.NewService(&claimRepo{roundRobinUser: selected}).AssignRoundRobin(tenancydomain.WithTenantContext(context.Background(), system), uuid.New())
	if err != nil || got != selected {
		t.Fatalf("got=%s err=%v", got, err)
	}
	human, _ := tenancydomain.NewTenantContext(uuid.New(), uuid.New(), tenancydomain.AccessSourceDirect)
	if _, err := application.NewService(&claimRepo{roundRobinUser: selected}).AssignRoundRobin(tenancydomain.WithTenantContext(context.Background(), human), uuid.New()); err == nil {
		t.Fatal("human context used system routing")
	}
}

func TestClaimOwnReturnsConflictAndRejectsSystemActor(t *testing.T) {
	actor := uuid.New()
	tc, _ := tenancydomain.NewTenantContext(uuid.New(), actor, tenancydomain.AccessSourceDirect)
	if err := application.NewService(&claimRepo{}).ClaimOwn(tenancydomain.WithTenantContext(context.Background(), tc), uuid.New()); !errors.Is(err, application.ErrAlreadyAssigned) {
		t.Fatalf("expected conflict, got %v", err)
	}
	system, _ := tenancydomain.NewTenantContext(uuid.New(), uuid.Nil, tenancydomain.AccessSourceSystem)
	if err := application.NewService(&claimRepo{claimed: true}).ClaimOwn(tenancydomain.WithTenantContext(context.Background(), system), uuid.New()); err == nil {
		t.Fatal("system actor used manual claim")
	}
}
