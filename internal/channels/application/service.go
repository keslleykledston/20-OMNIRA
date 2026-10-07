// Package application orquestra o channel seam: resolve a
// ChannelConnection correta (nunca confiando em tenant_id do payload),
// escolhe o ChannelProvider registrado para aquele provider concreto, e
// delega a operação. Nenhuma regra de negócio de canal (idempotência,
// window policy, routing) vive nos adapters — vive aqui ou em pacotes
// específicos (ex.: internal/platform/idempotency, quando existir).
package application

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
)

// ProviderRegistry — resolve qual ChannelProvider concreto atende um nome
// de provider (ex.: "meta_cloud" -> adapter Meta, "waha" -> adapter WAHA).
// Trocar/adicionar um provider nunca deve exigir mudar ChannelService.
type ProviderRegistry interface {
	Resolve(providerName string) (ports.ChannelProvider, error)
	Descriptor(providerName string) (ports.ProviderDescriptor, error)
	Descriptors() []ports.ProviderDescriptor
}

// MapProviderRegistry — implementação simples de ProviderRegistry baseada
// em mapa; suficiente para o MVP (poucos providers, registro estático no
// bootstrap da aplicação).
type MapProviderRegistry struct {
	providers   map[string]ports.ChannelProvider
	descriptors map[string]ports.ProviderDescriptor
}

func NewMapProviderRegistry() *MapProviderRegistry {
	return &MapProviderRegistry{providers: make(map[string]ports.ChannelProvider), descriptors: make(map[string]ports.ProviderDescriptor)}
}

func (r *MapProviderRegistry) Register(name string, provider ports.ChannelProvider) {
	r.providers[name] = provider
	if provider != nil {
		metadata := provider.Metadata()
		r.descriptors[name] = ports.ProviderDescriptor{
			ID: name, Name: name, Kind: metadata.Kind, Capabilities: append([]domain.Capability(nil), metadata.Capabilities...),
			Enabled: true, Inputs: []ports.ProviderInputDescriptor{}, Displays: []ports.ProviderDisplayDescriptor{},
		}
	}
}

// RegisterDescriptor records a UI descriptor independently from the runtime
// adapter. This keeps disabled providers visible in the catalog while Resolve
// still refuses to execute them.
func (r *MapProviderRegistry) RegisterDescriptor(descriptor ports.ProviderDescriptor, provider ports.ChannelProvider) error {
	descriptor.ID = strings.TrimSpace(descriptor.ID)
	if descriptor.ID == "" || descriptor.Name == "" || descriptor.Channel == "" || descriptor.ConnectMethod == "" {
		return fmt.Errorf("channel: invalid provider descriptor")
	}
	if descriptor.Inputs == nil {
		descriptor.Inputs = []ports.ProviderInputDescriptor{}
	}
	if descriptor.Displays == nil {
		descriptor.Displays = []ports.ProviderDisplayDescriptor{}
	}
	if descriptor.Capabilities == nil {
		descriptor.Capabilities = []domain.Capability{}
	}
	r.descriptors[descriptor.ID] = descriptor
	if provider != nil {
		r.providers[descriptor.ID] = provider
	} else {
		delete(r.providers, descriptor.ID)
	}
	return nil
}

func (r *MapProviderRegistry) Resolve(providerName string) (ports.ChannelProvider, error) {
	descriptor, described := r.descriptors[providerName]
	if described && !descriptor.Enabled {
		return nil, fmt.Errorf("%w: %s", ports.ErrNotConfigured, descriptor.UnavailableReason)
	}
	p, ok := r.providers[providerName]
	if !ok {
		return nil, fmt.Errorf("channel: provider %q não registrado", providerName)
	}
	return p, nil
}

func (r *MapProviderRegistry) Descriptor(providerName string) (ports.ProviderDescriptor, error) {
	d, ok := r.descriptors[providerName]
	if !ok {
		return ports.ProviderDescriptor{}, fmt.Errorf("channel: provider %q not registered", providerName)
	}
	return d, nil
}

func (r *MapProviderRegistry) Descriptors() []ports.ProviderDescriptor {
	out := make([]ports.ProviderDescriptor, 0, len(r.descriptors))
	for _, descriptor := range r.descriptors {
		out = append(out, descriptor)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ChannelService — orquestra resolução de conexão + delegação ao provider.
type ChannelService struct {
	connRepo ports.ChannelConnectionRepository
	registry ProviderRegistry
}

func NewChannelService(connRepo ports.ChannelConnectionRepository, registry ProviderRegistry) *ChannelService {
	return &ChannelService{connRepo: connRepo, registry: registry}
}

// ResolveInboundConnection — ponto único de resolução de tenant para
// webhooks inbound. externalNumberID deve vir de uma fonte confiável (path
// da rota, ou campo de identificação do provider validado na verificação
// de assinatura), nunca de um campo arbitrário dentro do corpo do payload.
func (s *ChannelService) ResolveInboundConnection(ctx context.Context, providerName, externalNumberID string) (*domain.ChannelConnection, error) {
	conn, err := s.connRepo.FindByExternalNumberID(ctx, providerName, externalNumberID)
	if err != nil {
		return nil, fmt.Errorf("channel: falha ao resolver conexão: %w", err)
	}
	if conn == nil {
		return nil, fmt.Errorf("channel: nenhuma conexão encontrada para provider=%s external_number_id=%s", providerName, externalNumberID)
	}
	return conn, nil
}

// ResolveWahaConnection resolves the opaque path token used by a WAHA
// webhook. The token is a connection UUID, not a tenant claim or session
// name. Caller must use an authorized system context for this unauthenticated
// provider callback, then derive TenantContext from the returned connection.
func (s *ChannelService) ResolveWahaConnection(ctx context.Context, connectionToken string) (*domain.ChannelConnection, error) {
	connectionID, err := uuid.Parse(connectionToken)
	if err != nil {
		return nil, fmt.Errorf("channel: invalid WAHA connection token")
	}
	conn, err := s.connRepo.FindByID(ctx, connectionID)
	if err != nil {
		return nil, fmt.Errorf("channel: failed to resolve WAHA connection: %w", err)
	}
	if conn == nil || conn.Provider != domain.ProviderWAHA || conn.ProviderKind != domain.ProviderKindUnofficial {
		return nil, fmt.Errorf("channel: unknown WAHA connection")
	}
	return conn, nil
}

// SendText — resolve o provider da conexão e delega o envio. O
// TenantContext do chamador já deve ter autorizado o acesso a esta
// connection antes de chegar aqui (a autorização não é responsabilidade
// deste serviço — ver internal/tenancy).
func (s *ChannelService) SendText(ctx context.Context, connID uuid.UUID, msg domain.OutboundTextMessage) (*domain.SendResult, error) {
	conn, err := s.connRepo.FindByID(ctx, connID)
	if err != nil {
		return nil, fmt.Errorf("channel: falha ao buscar conexão: %w", err)
	}
	if conn == nil {
		return nil, fmt.Errorf("channel: conexão %s não encontrada", connID)
	}

	provider, err := s.registry.Resolve(conn.Provider)
	if err != nil {
		return nil, err
	}

	if !provider.IsConfigured(ctx, *conn) {
		return nil, ports.ErrNotConfigured
	}

	return provider.SendText(ctx, *conn, msg)
}

// SendTemplate sends an approved template through the connection's provider (Meta Cloud API).
func (s *ChannelService) SendTemplate(ctx context.Context, connID uuid.UUID, msg domain.OutboundTemplateMessage) (*domain.SendResult, error) {
	conn, err := s.connRepo.FindByID(ctx, connID)
	if err != nil {
		return nil, fmt.Errorf("channel: falha ao buscar conexão: %w", err)
	}
	if conn == nil {
		return nil, fmt.Errorf("channel: conexão %s não encontrada", connID)
	}
	provider, err := s.registry.Resolve(conn.Provider)
	if err != nil {
		return nil, err
	}
	if !provider.IsConfigured(ctx, *conn) {
		return nil, ports.ErrNotConfigured
	}
	return provider.SendTemplate(ctx, *conn, msg)
}

// NewMessageID — resolve o provider da conexão e delega a reserva de um id
// estável. Mesma resolução de conexão/provider de SendText; ver
// ports.ChannelProvider.NewMessageID.
func (s *ChannelService) NewMessageID(ctx context.Context, connID uuid.UUID) (string, error) {
	conn, err := s.connRepo.FindByID(ctx, connID)
	if err != nil {
		return "", fmt.Errorf("channel: falha ao buscar conexão: %w", err)
	}
	if conn == nil {
		return "", fmt.Errorf("channel: conexão %s não encontrada", connID)
	}

	provider, err := s.registry.Resolve(conn.Provider)
	if err != nil {
		return "", err
	}

	if !provider.IsConfigured(ctx, *conn) {
		return "", ports.ErrNotConfigured
	}

	return provider.NewMessageID(ctx, *conn)
}
