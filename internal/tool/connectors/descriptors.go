package connectors

import (
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
)

// Descritores dos sistemas de retaguarda (CRM/ERP) para a aba de Integrações.
//
// A tela é dirigida por estes descritores: o administrador preenche o
// formulário sem que o frontend conheça nenhum campo específico de IXC ou do
// CRM K3G. Acrescentar um ERP novo é acrescentar um descritor e um adapter,
// não uma tela.
//
// Todo campo marcado Secret é write-only: nunca volta pela API, nem em leitura
// nem em erro. A UI mostra apenas máscara e data da última rotação.

// K3GCRMDescriptor — CRM próprio da K3G. Autentica por e-mail e senha e
// devolve um token de sessão, então a senha é o segredo guardado.
func K3GCRMDescriptor(enabled bool, unavailableReason string) ports.ProviderDescriptor {
	return ports.ProviderDescriptor{
		ID:            "k3g_crm",
		Name:          "CRM K3G",
		Channel:       domain.ChannelERP,
		Kind:          domain.ProviderKindOfficial,
		ConnectMethod: ports.ConnectMethodCredentials,
		Capabilities: []domain.Capability{
			domain.CapabilityText,
		},
		Enabled:           enabled,
		UnavailableReason: unavailableReason,
		Inputs: ProviderInputs{
			{Key: "base_url", Label: "URL da API", Type: "text", Required: true,
				Help:    "Endereço base da API do CRM, sem barra no fim.",
				Example: "https://api.k3gsolutions.com.br"},
			{Key: "email", Label: "E-mail de integração", Type: "text", Required: true,
				Help:    "Use um usuário dedicado à integração, não a conta de uma pessoa.",
				Example: "integracao@k3gsolutions.com.br"},
			{Key: "password", Label: "Senha", Type: "secret", Required: true, Secret: true,
				Help: "Guardada cifrada e nunca exibida de volta."},
		}.toPorts(),
		Displays: []ports.ProviderDisplayDescriptor{},
	}
}

// IXCDescriptor — ERP IXCSoft, usado por provedores de internet. Autentica por
// usuário e token fixo de API; não há troca de senha por sessão.
func IXCDescriptor(enabled bool, unavailableReason string) ports.ProviderDescriptor {
	return ports.ProviderDescriptor{
		ID:            "ixc",
		Name:          "IXCSoft (ERP)",
		Channel:       domain.ChannelERP,
		Kind:          domain.ProviderKindOfficial,
		ConnectMethod: ports.ConnectMethodCredentials,
		Capabilities: []domain.Capability{
			domain.CapabilityText,
		},
		Enabled:           enabled,
		UnavailableReason: unavailableReason,
		Inputs: ProviderInputs{
			{Key: "base_url", Label: "URL do webservice", Type: "text", Required: true,
				Help:    "Inclui o caminho do webservice.",
				Example: "https://ixc.exemplo.com.br/webservice/v1"},
			{Key: "user", Label: "Usuário da API", Type: "text", Required: true,
				Help: "Usuário com permissão de consultar cliente e abrir chamado."},
			{Key: "token", Label: "Token da API", Type: "secret", Required: true, Secret: true,
				Help: "Gerado no IXC em Configurações → API. Guardado cifrado."},
		}.toPorts(),
		Displays: []ports.ProviderDisplayDescriptor{},
	}
}

// providerInput existe só para deixar a declaração dos descritores legível; o
// formato que atravessa a API continua sendo o da porta.
type providerInput struct {
	Key      string
	Label    string
	Type     string
	Required bool
	Secret   bool
	Help     string
	Example  string
	Pattern  string
}

type ProviderInputs []providerInput

func (in ProviderInputs) toPorts() []ports.ProviderInputDescriptor {
	out := make([]ports.ProviderInputDescriptor, 0, len(in))
	for _, i := range in {
		out = append(out, ports.ProviderInputDescriptor{
			Key: i.Key, Label: i.Label, Type: i.Type, Required: i.Required,
			Secret: i.Secret, Help: i.Help, Example: i.Example, Pattern: i.Pattern,
		})
	}
	return out
}
