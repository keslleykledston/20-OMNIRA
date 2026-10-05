package application

import "testing"

func TestDefaultsAreSafe(t *testing.T) {
	f := FlagsFromEnv(func(string) string { return "" })
	if !f.ContactClassificationEnabled || !f.CustomerAccountsEnabled || !f.InternalChannelIdentityEnabled || !f.ConversationKindEnabled {
		t.Errorf("classification, accounts, verified-identity resolver and conversation kind default ON: %+v", f)
	}
	if f.AIAutoClassificationEnabled || f.CRMAutoContactCreationEnabled || f.AutoCustomerTicketUnclassifiedOn {
		t.Errorf("AI auto-classification, CRM auto contact creation and auto customer tickets for unclassified default OFF: %+v", f)
	}
}

func TestEnvOverridesAndIgnoresGarbage(t *testing.T) {
	env := map[string]string{"OMNIRA_INTERNAL_CHANNEL_IDENTITY_ENABLED": "off", "OMNIRA_AI_AUTO_CLASSIFICATION_ENABLED": " TRUE ", "OMNIRA_CONVERSATION_KIND_ENABLED": "banana"}
	f := FlagsFromEnv(func(k string) string { return env[k] })
	if f.InternalChannelIdentityEnabled || !f.AIAutoClassificationEnabled || !f.ConversationKindEnabled {
		t.Errorf("%+v", f)
	}
}
