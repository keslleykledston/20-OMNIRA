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
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type erpConnRepo struct {
	stored  []*domain.ChannelConnection
	updated []*domain.ChannelConnection
}

func (r *erpConnRepo) Store(_ context.Context, c *domain.ChannelConnection) error {
	r.stored = append(r.stored, c)
	return nil
}
func (r *erpConnRepo) Update(_ context.Context, c *domain.ChannelConnection) error {
	r.updated = append(r.updated, c)
	return nil
}
func (r *erpConnRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.ChannelConnection, error) {
	for _, c := range r.stored {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, nil
}
func (r *erpConnRepo) FindByTenant(_ context.Context, tenant uuid.UUID) ([]*domain.ChannelConnection, error) {
	out := []*domain.ChannelConnection{}
	for _, c := range r.stored {
		if c.TenantID == tenant {
			out = append(out, c)
		}
	}
	return out, nil
}

func (r *erpConnRepo) FindByExternalNumberID(_ context.Context, provider, external string) (*domain.ChannelConnection, error) {
	for _, c := range r.stored {
		if c.Provider == provider && c.ExternalNumberID == external {
			return c, nil
		}
	}
	return nil, nil
}

type erpCredStore struct{ saved map[string]string }

func (s *erpCredStore) Store(_ context.Context, id uuid.UUID, c ports.Credential) (string, error) {
	s.saved = c.Fields
	return "ref-" + id.String(), nil
}
func (s *erpCredStore) Resolve(context.Context, string) (ports.Credential, error) {
	return ports.Credential{Fields: s.saved}, nil
}
func (s *erpCredStore) Rotate(context.Context, string, ports.Credential) error { return nil }

type erpPerms struct{ allow bool }

func (p erpPerms) HasPermission(context.Context, uuid.UUID, string) (bool, error) {
	return p.allow, nil
}

type erpAudit struct{ payloads []map[string]any }

func (a *erpAudit) Record(_ context.Context, _ string, _ string, _ uuid.UUID, payload map[string]any) error {
	a.payloads = append(a.payloads, payload)
	return nil
}

func erpDescriptor() ports.ProviderDescriptor {
	return ports.ProviderDescriptor{
		ID: "k3g_crm", Name: "CRM K3G", Channel: domain.ChannelERP,
		Kind: domain.ProviderKindOfficial, ConnectMethod: ports.ConnectMethodCredentials,
		Capabilities: []domain.Capability{domain.CapabilityText}, Enabled: true,
		Inputs: []ports.ProviderInputDescriptor{
			{Key: "base_url", Label: "URL", Type: "text", Required: true},
			{Key: "token", Label: "Token", Type: "secret", Required: true, Secret: true},
		},
	}
}

func erpCtx(t *testing.T) (context.Context, uuid.UUID) {
	t.Helper()
	tenant, actor := uuid.New(), uuid.New()
	tc, err := tenancydomain.NewTenantContext(tenant, actor, tenancydomain.AccessSourceDirect)
	if err != nil {
		t.Fatal(err)
	}
	return tenancydomain.WithTenantContext(context.Background(), tc), tenant
}

func newERP(t *testing.T, allow bool) (*application.ERPConnectionService, *erpConnRepo, *erpCredStore, *erpAudit) {
	t.Helper()
	repo, creds, audit := &erpConnRepo{}, &erpCredStore{}, &erpAudit{}
	return application.NewERPConnectionService(erpDescriptor(), repo, creds, erpPerms{allow: allow}, audit), repo, creds, audit
}

func TestERPCreateStoresCredentialEncryptedAndNeverInTheRow(t *testing.T) {
	svc, repo, creds, audit := newERP(t, true)
	ctx, tenant := erpCtx(t)

	v, err := svc.CreateConnection(ctx, application.ConnectionCreateRequest{
		Provider: "k3g_crm",
		Inputs:   map[string]string{"base_url": "https://api.k3gsolutions.com.br/", "token": "s3cr3t-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.Provider != "k3g_crm" || v.Status != domain.ConnectionStatusPending {
		t.Fatalf("view inesperada: %+v", v)
	}

	// O token só pode existir cifrado, nunca na linha da conexão.
	if creds.saved["token"] != "s3cr3t-token" {
		t.Fatalf("token não foi para o cofre: %v", creds.saved)
	}
	row := repo.stored[0]
	blob := strings.Join([]string{row.ExternalAccountID, row.ExternalNumberID, row.ProviderSessionRef, row.SecretRef}, " ")
	if strings.Contains(blob, "s3cr3t-token") {
		t.Fatalf("token vazou para a linha da conexão: %q", blob)
	}
	if row.TenantID != tenant || row.Channel != domain.ChannelERP {
		t.Fatalf("conexão mal formada: %+v", row)
	}
	// Barra final removida, para o adapter não montar URL com barra dupla.
	if creds.saved["base_url"] != "https://api.k3gsolutions.com.br" {
		t.Fatalf("base_url não normalizada: %q", creds.saved["base_url"])
	}
	// Auditoria registra o quê e onde, nunca o segredo.
	if len(audit.payloads) != 1 {
		t.Fatalf("esperava 1 evento de auditoria, veio %d", len(audit.payloads))
	}
	for k, val := range audit.payloads[0] {
		if s, ok := val.(string); ok && strings.Contains(s, "s3cr3t-token") {
			t.Fatalf("token vazou na auditoria em %q", k)
		}
	}
}

func TestERPRejectsIncompleteAndUnknownFields(t *testing.T) {
	ctx, _ := erpCtx(t)
	for name, inputs := range map[string]map[string]string{
		"sem token":        {"base_url": "https://api.k3gsolutions.com.br"},
		"sem url":          {"token": "t"},
		"token em branco":  {"base_url": "https://api.k3gsolutions.com.br", "token": "   "},
		"campo desconhece": {"base_url": "https://api.k3gsolutions.com.br", "token": "t", "senha": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, _, _ := newERP(t, true)
			_, err := svc.CreateConnection(ctx, application.ConnectionCreateRequest{Provider: "k3g_crm", Inputs: inputs})
			if !errors.Is(err, application.ErrInvalidCredentials) {
				t.Fatalf("esperava recusa, veio %v", err)
			}
		})
	}
}

// A credencial acompanha toda requisição ao CRM; mandá-la em claro pela rede
// entrega o sistema inteiro a quem estiver no caminho.
func TestERPRefusesPlainHTTPExceptLocalhost(t *testing.T) {
	ctx, _ := erpCtx(t)
	for _, badURL := range []string{
		"http://api.k3gsolutions.com.br",
		"ftp://api.k3gsolutions.com.br",
		"api.k3gsolutions.com.br",
		"",
	} {
		svc, _, _, _ := newERP(t, true)
		_, err := svc.CreateConnection(ctx, application.ConnectionCreateRequest{
			Provider: "k3g_crm", Inputs: map[string]string{"base_url": badURL, "token": "t"},
		})
		if !errors.Is(err, application.ErrInvalidCredentials) {
			t.Fatalf("URL insegura %q foi aceita: %v", badURL, err)
		}
	}
	// Ambiente local não sai da máquina; recusar atrapalharia desenvolvimento.
	svc, _, _, _ := newERP(t, true)
	if _, err := svc.CreateConnection(ctx, application.ConnectionCreateRequest{
		Provider: "k3g_crm", Inputs: map[string]string{"base_url": "http://localhost:3000", "token": "t"},
	}); err != nil {
		t.Fatalf("localhost deveria ser aceito: %v", err)
	}
}

func TestERPRequiresChannelManagePermission(t *testing.T) {
	svc, _, _, _ := newERP(t, false)
	ctx, _ := erpCtx(t)
	_, err := svc.CreateConnection(ctx, application.ConnectionCreateRequest{
		Provider: "k3g_crm", Inputs: map[string]string{"base_url": "https://api.k3gsolutions.com.br", "token": "t"},
	})
	if err == nil {
		t.Fatal("criou conexão sem permissão de gerenciar canal")
	}
}

func TestERPDoesNotServeAnotherProvider(t *testing.T) {
	svc, _, _, _ := newERP(t, true)
	ctx, _ := erpCtx(t)
	_, err := svc.CreateConnection(ctx, application.ConnectionCreateRequest{
		Provider: "ixc", Inputs: map[string]string{"base_url": "https://ixc.example", "token": "t"},
	})
	if !errors.Is(err, application.ErrProviderNotFound) {
		t.Fatalf("serviço do CRM aceitou conexão de outro provedor: %v", err)
	}
}
