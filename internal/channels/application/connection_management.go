package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

var (
	ErrProviderNotFound      = errors.New("channel: provider not found")
	ErrProviderUnavailable   = errors.New("channel: provider unavailable")
	ErrInvalidProviderInputs = errors.New("channel: invalid provider inputs")
)

type ConnectionCreateRequest struct {
	Provider         string
	Inputs           map[string]string
	RiskAcknowledged bool
}

// ConnectionManager is the provider-specific lifecycle seam used by the
// generic API. Session operations stay optional because credential and OAuth
// providers do not necessarily expose QR/start/stop semantics.
type ConnectionManager interface {
	CreateConnection(context.Context, ConnectionCreateRequest) (ConnectionView, error)
	List(context.Context) ([]ConnectionView, error)
	Get(context.Context, uuid.UUID) (ConnectionView, error)
}

type SessionConnectionManager interface {
	ConnectionManager
	StartSession(context.Context, uuid.UUID) (ConnectionView, error)
	StopSession(context.Context, uuid.UUID) (ConnectionView, error)
	QR(context.Context, uuid.UUID) (ports.QRImage, error)
}

// ConnectionManagementService provides the provider-neutral management API.
// Concrete managers are registered at bootstrap; adding one does not change
// handlers or route shapes.
type ConnectionManagementService struct {
	registry ProviderRegistry
	perms    ports.PermissionChecker
	managers map[string]ConnectionManager
}

func NewConnectionManagementService(registry ProviderRegistry, perms ports.PermissionChecker) *ConnectionManagementService {
	return &ConnectionManagementService{registry: registry, perms: perms, managers: make(map[string]ConnectionManager)}
}

func (s *ConnectionManagementService) Register(provider string, manager ConnectionManager) {
	if manager != nil {
		s.managers[provider] = manager
	}
}

func (s *ConnectionManagementService) authorize(ctx context.Context) error {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
		return ErrConnForbidden
	}
	ok, err := s.perms.HasPermission(ctx, tc.ActorID, PermissionChannelManage)
	if err != nil {
		return err
	}
	if !ok {
		return ErrConnForbidden
	}
	return nil
}

func (s *ConnectionManagementService) Providers(ctx context.Context) ([]ports.ProviderDescriptor, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.registry.Descriptors(), nil
}

func (s *ConnectionManagementService) Create(ctx context.Context, req ConnectionCreateRequest) (ConnectionView, error) {
	if err := s.authorize(ctx); err != nil {
		return ConnectionView{}, err
	}
	req.Provider = strings.TrimSpace(req.Provider)
	descriptor, err := s.registry.Descriptor(req.Provider)
	if err != nil {
		return ConnectionView{}, ErrProviderNotFound
	}
	if !descriptor.Enabled {
		return ConnectionView{}, fmt.Errorf("%w: %s", ErrProviderUnavailable, descriptor.UnavailableReason)
	}
	if err := validateProviderInputs(descriptor, req.Inputs); err != nil {
		return ConnectionView{}, err
	}
	manager, ok := s.managers[req.Provider]
	if !ok {
		return ConnectionView{}, ErrProviderUnavailable
	}
	return manager.CreateConnection(ctx, req)
}

func validateProviderInputs(descriptor ports.ProviderDescriptor, values map[string]string) error {
	allowed := make(map[string]ports.ProviderInputDescriptor, len(descriptor.Inputs))
	for _, input := range descriptor.Inputs {
		allowed[input.Key] = input
		if input.Required && strings.TrimSpace(values[input.Key]) == "" {
			return fmt.Errorf("%w: missing %s", ErrInvalidProviderInputs, input.Key)
		}
	}
	for key := range values {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("%w: unexpected %s", ErrInvalidProviderInputs, key)
		}
	}
	return nil
}

func (s *ConnectionManagementService) List(ctx context.Context) ([]ConnectionView, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	providers := make([]string, 0, len(s.managers))
	for provider := range s.managers {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	items := []ConnectionView{}
	for _, provider := range providers {
		found, err := s.managers[provider].List(ctx)
		if err != nil {
			return nil, err
		}
		items = append(items, found...)
	}
	return items, nil
}

func (s *ConnectionManagementService) Get(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	if err := s.authorize(ctx); err != nil {
		return ConnectionView{}, err
	}
	for _, manager := range s.managers {
		item, err := manager.Get(ctx, id)
		if errors.Is(err, ErrConnNotFound) {
			continue
		}
		return item, err
	}
	return ConnectionView{}, ErrConnNotFound
}

func (s *ConnectionManagementService) session(ctx context.Context, id uuid.UUID) (SessionConnectionManager, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	for _, manager := range s.managers {
		if _, err := manager.Get(ctx, id); errors.Is(err, ErrConnNotFound) {
			continue
		} else if err != nil {
			return nil, err
		}
		sessions, ok := manager.(SessionConnectionManager)
		if !ok {
			return nil, ports.ErrCapabilityNotSupported
		}
		return sessions, nil
	}
	return nil, ErrConnNotFound
}

func (s *ConnectionManagementService) StartSession(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	manager, err := s.session(ctx, id)
	if err != nil {
		return ConnectionView{}, err
	}
	return manager.StartSession(ctx, id)
}

func (s *ConnectionManagementService) StopSession(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	manager, err := s.session(ctx, id)
	if err != nil {
		return ConnectionView{}, err
	}
	return manager.StopSession(ctx, id)
}

func (s *ConnectionManagementService) QR(ctx context.Context, id uuid.UUID) (ports.QRImage, error) {
	manager, err := s.session(ctx, id)
	if err != nil {
		return ports.QRImage{}, err
	}
	return manager.QR(ctx, id)
}
