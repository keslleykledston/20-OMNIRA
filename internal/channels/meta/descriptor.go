package meta

import (
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
)

// Descriptor declares the per-connection onboarding of an official WhatsApp number (Cloud API). Everything secret is
// stored encrypted per connection; the callback URL and the verify token are shown once the connection exists so they
// can be pasted into the Meta app's webhook settings.
func Descriptor(enabled bool, unavailableReason string) ports.ProviderDescriptor {
	return ports.ProviderDescriptor{
		ID:            domain.ProviderMetaCloud,
		Name:          "WhatsApp oficial (Meta Cloud API)",
		Channel:       domain.ChannelWhatsApp,
		Kind:          domain.ProviderKindOfficial,
		ConnectMethod: ports.ConnectMethodCredentials,
		Capabilities:  capabilities(),
		Enabled:       enabled, UnavailableReason: unavailableReason,
		Inputs: []ports.ProviderInputDescriptor{
			{Key: "phone_number_id", Label: "Phone Number ID", Type: "text", Required: true, Pattern: `^[0-9]{5,20}$`, Help: "Meta for Developers → WhatsApp → API Setup", Example: "123456789012345"},
			{Key: "waba_id", Label: "WhatsApp Business Account ID (WABA)", Type: "text", Required: true, Pattern: `^[0-9]{5,20}$`, Help: "Meta for Developers → WhatsApp → API Setup", Example: "109876543210987"},
			{Key: "access_token", Label: "Token de acesso permanente", Type: "secret", Required: true, Secret: true, Help: "Token do usuário de sistema com whatsapp_business_messaging e whatsapp_business_management. Guardado cifrado; nunca é exibido de novo."},
			{Key: "app_secret", Label: "Chave secreta do app (App Secret)", Type: "secret", Required: true, Secret: true, Help: "Configurações do app → Básico. Usada para validar a assinatura de cada webhook. Guardada cifrada."},
		},
		Displays: []ports.ProviderDisplayDescriptor{
			{Key: "callback_url", Label: "URL de retorno de chamada (Callback URL)", Copyable: true, Help: "Cole em WhatsApp → Configuration → Webhook."},
			{Key: "verify_token", Label: "Token de verificação (Verify Token)", Copyable: true, Sensitive: true, Help: "Cole no mesmo lugar. Depois assine o campo \"messages\"."},
		},
	}
}
