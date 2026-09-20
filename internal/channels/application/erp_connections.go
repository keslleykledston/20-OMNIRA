package application

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ErrInvalidCredentials indica formulário incompleto ou malformado. É do
// usuário, não do provedor: a mensagem precisa dizer qual campo está errado
// sem jamais ecoar o valor, que pode ser o segredo.
var ErrInvalidCredentials = errors.New("channel: invalid credentials")

// ErrCredentialRejected é a credencial guardada sendo recusada pelo sistema
// externo. É diferente de ErrInvalidCredentials, que é formulário malformado:
// aqui o formato está certo e quem recusou foi o outro lado.
var ErrCredentialRejected = errors.New("channel: credential rejected by the provider")

// CredentialProbe confirma que uma credencial é aceita pelo sistema externo.
//
// A sonda deve ser de leitura: "Testar" é um botão, e o operador vai clicar
// mais de uma vez. Uma sonda que escrevesse deixaria rastro no sistema do
// cliente a cada tentativa.
type CredentialProbe interface {
	Probe(ctx context.Context, fields map[string]string) error
}

// ERPConnectionService guarda as credenciais de um sistema de retaguarda
// (CRM/ERP) por tenant.
//
// Diferente de um canal de mensagem, aqui não há sessão nem pareamento: a
// conexão existe para guardar credencial cifrada e registrar se ela funciona.
// Por isso implementa apenas ConnectionManager, não SessionConnectionManager.
type ERPConnectionService struct {
	descriptor  ports.ProviderDescriptor
	conns       ports.ChannelConnectionRepository
	credentials ports.CredentialStore
	perms       ports.PermissionChecker
	audit       ports.ChannelAudit
	probe       CredentialProbe
}

func NewERPConnectionService(
	descriptor ports.ProviderDescriptor,
	conns ports.ChannelConnectionRepository,
	credentials ports.CredentialStore,
	perms ports.PermissionChecker,
	audit ports.ChannelAudit,
	probe CredentialProbe,
) *ERPConnectionService {
	return &ERPConnectionService{descriptor: descriptor, conns: conns, credentials: credentials, perms: perms, audit: audit, probe: probe}
}

// TestConnection verifica a credencial guardada e persiste o resultado.
//
// O estado reflete a última verificação real, nunca a intenção: sem sonda
// configurada a conexão permanece pendente, porque afirmar "conectado" sem ter
// falado com o sistema seria exatamente o "sucesso fantasma" que o projeto
// proíbe.
func (s *ERPConnectionService) TestConnection(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	tc, err := s.authorize(ctx)
	if err != nil {
		return ConnectionView{}, err
	}
	conn, err := s.conns.FindByID(ctx, id)
	if err != nil {
		return ConnectionView{}, err
	}
	if conn == nil || conn.TenantID != tc.TenantID || conn.Provider != s.descriptor.ID {
		return ConnectionView{}, ErrConnNotFound
	}
	if s.probe == nil {
		return ConnectionView{}, fmt.Errorf("%w: no probe for %s", ports.ErrNotConfigured, s.descriptor.ID)
	}
	credential, err := s.credentials.Resolve(ctx, conn.SecretRef)
	if err != nil {
		return ConnectionView{}, err
	}

	probeErr := s.probe.Probe(ctx, credential.Fields)
	conn.Status = domain.ConnectionStatusActive
	outcome := "ok"
	if probeErr != nil {
		conn.Status = domain.ConnectionStatusFailed
		outcome = "failed"
	}
	conn.UpdatedAt = time.Now().UTC()
	if err := s.conns.Update(ctx, conn); err != nil {
		return ConnectionView{}, err
	}
	// Registra o desfecho, não a causa detalhada: mensagens de erro de
	// provedor às vezes ecoam o que foi enviado.
	if err := s.audit.Record(ctx, "channel.connection_tested", "channel_connection", conn.ID, map[string]any{
		"provider": conn.Provider, "host": conn.ExternalAccountID, "outcome": outcome,
	}); err != nil {
		return ConnectionView{}, err
	}
	if probeErr != nil {
		if errors.Is(probeErr, ports.ErrAuthentication) {
			return view(conn, ""), fmt.Errorf("%w: %v", ErrCredentialRejected, probeErr)
		}
		return view(conn, ""), probeErr
	}
	return view(conn, ""), nil
}

func (s *ERPConnectionService) authorize(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
		return nil, ErrConnForbidden
	}
	ok, err := s.perms.HasPermission(ctx, tc.ActorID, PermissionChannelManage)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrConnForbidden
	}
	return tc, nil
}

func (s *ERPConnectionService) CreateConnection(ctx context.Context, req ConnectionCreateRequest) (ConnectionView, error) {
	if req.Provider != "" && req.Provider != s.descriptor.ID {
		return ConnectionView{}, ErrProviderNotFound
	}
	tc, err := s.authorize(ctx)
	if err != nil {
		return ConnectionView{}, err
	}
	fields, err := s.validate(req.Inputs)
	if err != nil {
		return ConnectionView{}, err
	}

	now := time.Now().UTC()
	id := uuid.New()
	conn := &domain.ChannelConnection{
		ID: id, TenantID: tc.TenantID,
		Channel: domain.ChannelERP, Provider: s.descriptor.ID, ProviderKind: s.descriptor.Kind,
		// O host fica visível para o operador reconhecer a conexão na lista.
		// O restante — inclusive o token — só existe cifrado.
		ExternalNumberID:  id.String(),
		ExternalAccountID: hostOf(fields["base_url"]),
		Status:            domain.ConnectionStatusPending,
		Capabilities:      append([]domain.Capability(nil), s.descriptor.Capabilities...),
		CreatedAt:         now, UpdatedAt: now,
	}
	if err := s.conns.Store(ctx, conn); err != nil {
		return ConnectionView{}, err
	}
	ref, err := s.credentials.Store(ctx, conn.ID, ports.Credential{Fields: fields})
	if err != nil {
		return ConnectionView{}, err
	}
	conn.SecretRef = ref
	if err := s.conns.Update(ctx, conn); err != nil {
		return ConnectionView{}, err
	}
	// O evento registra o provedor e o host, nunca os campos: auditoria não é
	// lugar de segredo.
	if err := s.audit.Record(ctx, "channel.connection_created", "channel_connection", conn.ID, map[string]any{
		"provider": conn.Provider, "host": conn.ExternalAccountID,
	}); err != nil {
		return ConnectionView{}, err
	}
	return view(conn, ""), nil
}

// validate confere o formulário contra o descritor. Só aceita campos que o
// descritor declara: um campo a mais seria guardado sem ninguém saber que
// existe, e campo desconhecido costuma ser engano de quem preenche.
func (s *ERPConnectionService) validate(inputs map[string]string) (map[string]string, error) {
	declared := make(map[string]ports.ProviderInputDescriptor, len(s.descriptor.Inputs))
	for _, in := range s.descriptor.Inputs {
		declared[in.Key] = in
	}
	fields := make(map[string]string, len(inputs))
	for key, raw := range inputs {
		spec, ok := declared[key]
		if !ok {
			return nil, fmt.Errorf("%w: unknown field %q", ErrInvalidCredentials, key)
		}
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if key == "base_url" {
			normalized, err := normalizeBaseURL(value)
			if err != nil {
				return nil, err
			}
			value = normalized
		}
		_ = spec
		fields[key] = value
	}
	for _, in := range s.descriptor.Inputs {
		if in.Required && strings.TrimSpace(fields[in.Key]) == "" {
			return nil, fmt.Errorf("%w: %q is required", ErrInvalidCredentials, in.Key)
		}
	}
	return fields, nil
}

// normalizeBaseURL recusa endereço que não seja HTTP(S) absoluto e remove a
// barra final, para o adapter poder concatenar caminho sem dobrar barra.
//
// HTTP puro é aceito apenas para host local: a credencial acompanha toda
// requisição, e mandá-la em claro pela rede entrega o CRM inteiro a quem
// estiver no caminho.
func normalizeBaseURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("%w: base_url must be an absolute URL", ErrInvalidCredentials)
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !isLocalHost(parsed.Hostname()) {
			return "", fmt.Errorf("%w: base_url must use https", ErrInvalidCredentials)
		}
	default:
		return "", fmt.Errorf("%w: base_url must use https", ErrInvalidCredentials)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

func isLocalHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func hostOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Host
}

func (s *ERPConnectionService) List(ctx context.Context) ([]ConnectionView, error) {
	tc, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.conns.FindByTenant(ctx, tc.TenantID)
	if err != nil {
		return nil, err
	}
	out := []ConnectionView{}
	for _, c := range all {
		if c.Provider == s.descriptor.ID && c.TenantID == tc.TenantID {
			out = append(out, view(c, ""))
		}
	}
	return out, nil
}

func (s *ERPConnectionService) Get(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	tc, err := s.authorize(ctx)
	if err != nil {
		return ConnectionView{}, err
	}
	conn, err := s.conns.FindByID(ctx, id)
	if err != nil {
		return ConnectionView{}, err
	}
	// RLS já esconde linha de outro tenant; a checagem explícita é defesa em
	// profundidade, como no serviço do WAHA.
	if conn == nil || conn.TenantID != tc.TenantID || conn.Provider != s.descriptor.ID {
		return ConnectionView{}, ErrConnNotFound
	}
	return view(conn, ""), nil
}
