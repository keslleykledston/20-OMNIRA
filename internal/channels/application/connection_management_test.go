package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/application"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type catalogPermission bool

func (p catalogPermission) HasPermission(context.Context, uuid.UUID, string) (bool, error) {
	return bool(p), nil
}

type fakeConnectionManager struct{ created int }

func (m *fakeConnectionManager) CreateConnection(_ context.Context, _ application.ConnectionCreateRequest) (application.ConnectionView, error) {
	m.created++
	return application.ConnectionView{Provider: domain.ProviderWAHA}, nil
}
func (*fakeConnectionManager) List(context.Context) ([]application.ConnectionView, error) {
	return []application.ConnectionView{}, nil
}
func (*fakeConnectionManager) Get(context.Context, uuid.UUID) (application.ConnectionView, error) {
	return application.ConnectionView{}, application.ErrConnNotFound
}

func directTenantContext(t *testing.T) context.Context {
	t.Helper()
	tc, err := tenancydomain.NewTenantContext(uuid.New(), uuid.New(), tenancydomain.AccessSourceDirect)
	if err != nil {
		t.Fatal(err)
	}
	return tenancydomain.WithTenantContext(context.Background(), tc)
}

func TestProviderDescriptorsAreStableAndDisabledProvidersCannotExecute(t *testing.T) {
	registry := application.NewMapProviderRegistry()
	enabledProvider := &fakeProvider{name: domain.ProviderWAHA, kind: domain.ProviderKindUnofficial, configured: true}
	if err := registry.RegisterDescriptor(ports.ProviderDescriptor{
		ID: domain.ProviderWAHA, Name: "WAHA", Channel: domain.ChannelWhatsApp, Kind: domain.ProviderKindUnofficial,
		ConnectMethod: ports.ConnectMethodQRSession, Enabled: true,
	}, enabledProvider); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterDescriptor(ports.ProviderDescriptor{
		ID: domain.ProviderMetaCloud, Name: "Meta", Channel: domain.ChannelWhatsApp, Kind: domain.ProviderKindOfficial,
		ConnectMethod: ports.ConnectMethodCredentials, Enabled: false, UnavailableReason: "I2",
	}, nil); err != nil {
		t.Fatal(err)
	}

	descriptors := registry.Descriptors()
	if len(descriptors) != 2 || descriptors[0].ID != domain.ProviderMetaCloud || descriptors[1].ID != domain.ProviderWAHA {
		t.Fatalf("descriptors must be deterministically sorted: %#v", descriptors)
	}
	if _, err := registry.Resolve(domain.ProviderMetaCloud); !errors.Is(err, ports.ErrNotConfigured) {
		t.Fatalf("disabled provider resolved: %v", err)
	}
}

func TestConnectionManagementAuthorizesCatalogAndValidatesDescriptorInputs(t *testing.T) {
	registry := application.NewMapProviderRegistry()
	provider := &fakeProvider{name: domain.ProviderWAHA, kind: domain.ProviderKindUnofficial, configured: true}
	if err := registry.RegisterDescriptor(ports.ProviderDescriptor{
		ID: domain.ProviderWAHA, Name: "WAHA", Channel: domain.ChannelWhatsApp, Kind: domain.ProviderKindUnofficial,
		ConnectMethod: ports.ConnectMethodQRSession, Enabled: true,
	}, provider); err != nil {
		t.Fatal(err)
	}
	manager := &fakeConnectionManager{}
	svc := application.NewConnectionManagementService(registry, catalogPermission(true))
	svc.Register(domain.ProviderWAHA, manager)
	ctx := directTenantContext(t)

	providers, err := svc.Providers(ctx)
	if err != nil || len(providers) != 1 {
		t.Fatalf("providers: len=%d err=%v", len(providers), err)
	}
	if _, err := svc.Create(ctx, application.ConnectionCreateRequest{Provider: domain.ProviderWAHA, Inputs: map[string]string{"secret": "must-not-pass"}}); !errors.Is(err, application.ErrInvalidProviderInputs) {
		t.Fatalf("unexpected input was accepted: %v", err)
	}
	if manager.created != 0 {
		t.Fatal("invalid request reached provider manager")
	}
	if _, err := svc.Create(ctx, application.ConnectionCreateRequest{Provider: domain.ProviderWAHA, RiskAcknowledged: true}); err != nil {
		t.Fatal(err)
	}
	if manager.created != 1 {
		t.Fatalf("create calls=%d", manager.created)
	}

	forbidden := application.NewConnectionManagementService(registry, catalogPermission(false))
	if _, err := forbidden.Providers(ctx); !errors.Is(err, application.ErrConnForbidden) {
		t.Fatalf("catalog did not enforce channel.manage: %v", err)
	}
}
