package meta

import (
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
)

// Descriptor advertises the future per-connection Meta onboarding flow.
// It remains disabled until I2 moves the current global credentials into the
// tenant-owned CredentialStore.
func Descriptor() ports.ProviderDescriptor {
	return ports.ProviderDescriptor{
		ID:                domain.ProviderMetaCloud,
		Name:              "WhatsApp · Meta Cloud",
		Channel:           domain.ChannelWhatsApp,
		Kind:              domain.ProviderKindOfficial,
		ConnectMethod:     ports.ConnectMethodCredentials,
		Capabilities:      []domain.Capability{domain.CapabilityText, domain.CapabilityMedia, domain.CapabilityTemplate, domain.CapabilityDeliveryStatus},
		Enabled:           false,
		UnavailableReason: "A configuração por conexão será disponibilizada na fase I2.",
		Inputs: []ports.ProviderInputDescriptor{
			{Key: "phone_number_id", Label: "Phone Number ID", Type: "text", Required: true},
			{Key: "waba_id", Label: "WABA ID", Type: "text", Required: true},
			{Key: "access_token", Label: "Access Token", Type: "secret", Required: true, Secret: true},
			{Key: "app_secret", Label: "App Secret", Type: "secret", Required: true, Secret: true},
		},
		Displays: []ports.ProviderDisplayDescriptor{
			{Key: "callback_url", Label: "Callback URL", Copyable: true},
			{Key: "verify_token", Label: "Verify Token", Copyable: true, Sensitive: true},
		},
	}
}
