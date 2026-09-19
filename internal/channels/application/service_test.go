package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/application"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
)

// fakeProvider — implementação em memória de ports.ChannelProvider. Prova
// que o seam funciona ponta a ponta sem nenhuma implementação real de
// Meta/WAHA (essas entram no Wave D3/D3+). Se este teste passar, qualquer
// provider futuro que satisfaça a interface se encaixa sem mudar
// ChannelService.
type fakeProvider struct {
	name         string
	kind         domain.ProviderKind
	capabilities []domain.Capability
	configured   bool
	sentTexts    []domain.OutboundTextMessage
}

func (f *fakeProvider) Metadata() ports.ProviderMetadata {
	return ports.ProviderMetadata{Name: f.name, Kind: f.kind, Capabilities: f.capabilities}
}

func (f *fakeProvider) IsConfigured(ctx context.Context, conn domain.ChannelConnection) bool {
	return f.configured
}

func (f *fakeProvider) CheckHealth(ctx context.Context, conn domain.ChannelConnection) (ports.HealthStatus, error) {
	return ports.HealthStatus{Reachable: true}, nil
}

func (f *fakeProvider) VerifyWebhook(ctx context.Context, conn domain.ChannelConnection, req ports.WebhookVerificationRequest) error {
	return nil
}

func (f *fakeProvider) ParseInbound(ctx context.Context, conn domain.ChannelConnection, payload []byte) (*domain.InboundMessage, error) {
	return &domain.InboundMessage{ProviderMessageID: "fake-1", Text: string(payload)}, nil
}

func (f *fakeProvider) SendText(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundTextMessage) (*domain.SendResult, error) {
	f.sentTexts = append(f.sentTexts, msg)
	return &domain.SendResult{ProviderMessageID: "fake-sent-1", State: domain.DeliveryStateSent}, nil
}

func (f *fakeProvider) SendMedia(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundMediaMessage) (*domain.SendResult, error) {
	return nil, ports.ErrCapabilityNotSupported
}

func (f *fakeProvider) SendTemplate(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundTemplateMessage) (*domain.SendResult, error) {
	return nil, ports.ErrCapabilityNotSupported
}

func (f *fakeProvider) DownloadMedia(ctx context.Context, conn domain.ChannelConnection, media domain.InboundMedia) (*domain.MediaContent, error) {
	return nil, ports.ErrCapabilityNotSupported
}

func (f *fakeProvider) HandleDeliveryStatus(ctx context.Context, conn domain.ChannelConnection, payload []byte) (*domain.DeliveryStatusUpdate, error) {
	return &domain.DeliveryStatusUpdate{ProviderMessageID: "fake-1", State: domain.DeliveryStateDelivered}, nil
}

// fakeConnRepo — implementação em memória de ports.ChannelConnectionRepository.
type fakeConnRepo struct {
	byID               map[uuid.UUID]*domain.ChannelConnection
	byExternalNumberID map[string]*domain.ChannelConnection
}

func newFakeConnRepo() *fakeConnRepo {
	return &fakeConnRepo{
		byID:               make(map[uuid.UUID]*domain.ChannelConnection),
		byExternalNumberID: make(map[string]*domain.ChannelConnection),
	}
}

func (r *fakeConnRepo) Store(ctx context.Context, conn *domain.ChannelConnection) error {
	r.byID[conn.ID] = conn
	r.byExternalNumberID[conn.Provider+"|"+conn.ExternalNumberID] = conn
	return nil
}

func (r *fakeConnRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.ChannelConnection, error) {
	return r.byID[id], nil
}

func (r *fakeConnRepo) FindByExternalNumberID(ctx context.Context, provider, externalNumberID string) (*domain.ChannelConnection, error) {
	return r.byExternalNumberID[provider+"|"+externalNumberID], nil
}

func (r *fakeConnRepo) FindByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.ChannelConnection, error) {
	var result []*domain.ChannelConnection
	for _, c := range r.byID {
		if c.TenantID == tenantID {
			result = append(result, c)
		}
	}
	return result, nil
}

func (r *fakeConnRepo) Update(ctx context.Context, conn *domain.ChannelConnection) error {
	r.byID[conn.ID] = conn
	return nil
}

func TestChannelSeam_SendText_EndToEnd(t *testing.T) {
	ctx := context.Background()
	repo := newFakeConnRepo()
	registry := application.NewMapProviderRegistry()
	provider := &fakeProvider{name: "fake_provider", kind: domain.ProviderKindOfficial, capabilities: []domain.Capability{domain.CapabilityText}, configured: true}
	registry.Register("fake_provider", provider)

	tenantA := uuid.New()
	connID := uuid.New()
	conn := &domain.ChannelConnection{
		ID:               connID,
		TenantID:         tenantA,
		Provider:         "fake_provider",
		ProviderKind:     domain.ProviderKindOfficial,
		ExternalNumberID: "5511999999999",
		Status:           domain.ConnectionStatusActive,
		Capabilities:     []domain.Capability{domain.CapabilityText},
	}
	repo.Store(ctx, conn)

	svc := application.NewChannelService(repo, registry)

	result, err := svc.SendText(ctx, connID, domain.OutboundTextMessage{ToE164: "+5511988888888", Text: "oi"})
	if err != nil {
		t.Fatalf("SendText não deveria falhar: %v", err)
	}
	if result.State != domain.DeliveryStateSent {
		t.Errorf("esperava state=sent, obteve %s", result.State)
	}
	if len(provider.sentTexts) != 1 {
		t.Errorf("esperava 1 mensagem enviada no provider fake, obteve %d", len(provider.sentTexts))
	}
}

func TestChannelSeam_SendText_ProviderNotConfigured(t *testing.T) {
	ctx := context.Background()
	repo := newFakeConnRepo()
	registry := application.NewMapProviderRegistry()
	provider := &fakeProvider{name: "fake_provider", configured: false}
	registry.Register("fake_provider", provider)

	connID := uuid.New()
	repo.Store(ctx, &domain.ChannelConnection{ID: connID, TenantID: uuid.New(), Provider: "fake_provider"})

	svc := application.NewChannelService(repo, registry)
	_, err := svc.SendText(ctx, connID, domain.OutboundTextMessage{ToE164: "+5511988888888", Text: "oi"})
	if !errors.Is(err, ports.ErrNotConfigured) {
		t.Errorf("esperava ErrNotConfigured, obteve: %v", err)
	}
}

func TestChannelSeam_SendText_ProviderNotRegistered(t *testing.T) {
	ctx := context.Background()
	repo := newFakeConnRepo()
	registry := application.NewMapProviderRegistry() // nenhum provider registrado

	connID := uuid.New()
	repo.Store(ctx, &domain.ChannelConnection{ID: connID, TenantID: uuid.New(), Provider: "unregistered_provider"})

	svc := application.NewChannelService(repo, registry)
	_, err := svc.SendText(ctx, connID, domain.OutboundTextMessage{ToE164: "+5511988888888", Text: "oi"})
	if err == nil {
		t.Error("esperava erro para provider não registrado")
	}
}

func TestChannelSeam_ResolveInboundConnection_NeverTrustsPayloadTenant(t *testing.T) {
	// Prova que a resolução de tenant para webhooks inbound usa SÓ a
	// fonte confiável (provider + external_number_id), nunca um campo
	// vindo de dentro do corpo do payload — o service nem aceita um
	// tenant_id como parâmetro nesta chamada.
	ctx := context.Background()
	repo := newFakeConnRepo()
	registry := application.NewMapProviderRegistry()
	svc := application.NewChannelService(repo, registry)

	tenantA := uuid.New()
	conn := &domain.ChannelConnection{
		ID:               uuid.New(),
		TenantID:         tenantA,
		Provider:         "fake_provider",
		ExternalNumberID: "5511999999999",
	}
	repo.Store(ctx, conn)

	resolved, err := svc.ResolveInboundConnection(ctx, "fake_provider", "5511999999999")
	if err != nil {
		t.Fatalf("não deveria falhar: %v", err)
	}
	if resolved.TenantID != tenantA {
		t.Errorf("esperava tenant %s, obteve %s", tenantA, resolved.TenantID)
	}

	// Número desconhecido -> erro explícito, nunca uma conexão "qualquer".
	_, err = svc.ResolveInboundConnection(ctx, "fake_provider", "0000000000000")
	if err == nil {
		t.Error("esperava erro para external_number_id desconhecido")
	}
}

func TestChannelConnection_RequiresRiskAcknowledgement(t *testing.T) {
	unofficial := domain.ChannelConnection{ProviderKind: domain.ProviderKindUnofficial}
	if !unofficial.RequiresRiskAcknowledgement() {
		t.Error("provider unofficial sem aceite deveria exigir reconhecimento de risco")
	}

	official := domain.ChannelConnection{ProviderKind: domain.ProviderKindOfficial}
	if official.RequiresRiskAcknowledgement() {
		t.Error("provider official nunca deveria exigir reconhecimento de risco")
	}
}

func TestChannelConnection_HasCapability(t *testing.T) {
	conn := domain.ChannelConnection{Capabilities: []domain.Capability{domain.CapabilityText, domain.CapabilityMedia}}
	if !conn.HasCapability(domain.CapabilityText) {
		t.Error("esperava suporte a text")
	}
	if conn.HasCapability(domain.CapabilityTemplate) {
		t.Error("não esperava suporte a template")
	}
}
