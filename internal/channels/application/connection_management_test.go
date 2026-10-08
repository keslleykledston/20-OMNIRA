package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/application"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/entitlements"
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

// ADR-0038: a company whose operator switched a channel family off cannot create a NEW connection of that family; the
// other family and the other companies are untouched, and the provider is never reached.
func TestConnectionManagementHonoursPerCompanySwitches(t *testing.T) {
	registry := application.NewMapProviderRegistry()
	for _, d := range []ports.ProviderDescriptor{
		{ID: domain.ProviderWAHA, Name: "WAHA", Channel: domain.ChannelWhatsApp, Kind: domain.ProviderKindUnofficial, ConnectMethod: ports.ConnectMethodQRSession, Enabled: true},
		{ID: "k3g_crm", Name: "K3G CRM", Channel: domain.ChannelERP, Kind: domain.ProviderKindOfficial, ConnectMethod: ports.ConnectMethodCredentials, Enabled: true},
	} {
		if err := registry.RegisterDescriptor(d, &fakeProvider{name: d.ID, kind: d.Kind, configured: true}); err != nil {
			t.Fatal(err)
		}
	}
	waha, erp := &fakeConnectionManager{}, &fakeConnectionManager{}
	var asked []string
	off := map[string]bool{}
	svc := application.NewConnectionManagementService(registry, catalogPermission(true)).WithEntitlements(
		func(_ context.Context, _ uuid.UUID, capability string) error {
			asked = append(asked, capability)
			if off[capability] {
				return entitlements.ErrDisabled
			}
			return nil
		})
	svc.Register(domain.ProviderWAHA, waha)
	svc.Register("k3g_crm", erp)
	ctx := directTenantContext(t)

	if _, err := svc.Create(ctx, application.ConnectionCreateRequest{Provider: domain.ProviderWAHA, RiskAcknowledged: true}); err != nil || waha.created != 1 {
		t.Fatalf("default is on: err=%v created=%d", err, waha.created)
	}
	off[entitlements.WhatsAppChannel] = true
	if _, err := svc.Create(ctx, application.ConnectionCreateRequest{Provider: domain.ProviderWAHA, RiskAcknowledged: true}); !errors.Is(err, entitlements.ErrDisabled) || waha.created != 1 {
		t.Fatalf("WhatsApp is off: err=%v created=%d", err, waha.created)
	}
	if _, err := svc.Create(ctx, application.ConnectionCreateRequest{Provider: "k3g_crm", Inputs: map[string]string{}}); errors.Is(err, entitlements.ErrDisabled) {
		t.Fatal("switching WhatsApp off must not block the ERP/CRM family")
	}
	off[entitlements.ERPCRM] = true
	before := erp.created
	if _, err := svc.Create(ctx, application.ConnectionCreateRequest{Provider: "k3g_crm", Inputs: map[string]string{}}); !errors.Is(err, entitlements.ErrDisabled) || erp.created != before {
		t.Fatalf("ERP/CRM is off: err=%v", err)
	}
	if got := strings.Join(asked, ","); !strings.Contains(got, entitlements.WhatsAppChannel) || !strings.Contains(got, entitlements.ERPCRM) {
		t.Fatalf("each family must be asked about its own switch: %s", got)
	}
}
