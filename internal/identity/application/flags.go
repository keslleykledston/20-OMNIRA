// Package application holds the feature flags of the identity / classification work (ADR-0018).
package application

import (
	"os"
	"strings"
)

// Flags are the independent switches of contact classification, customer accounts and internal identities. Read once
// at startup from the environment (same mechanism as Conversation Intelligence).
type Flags struct {
	ContactClassificationEnabled     bool // the classification UI/API: on
	CustomerAccountsEnabled          bool // customer accounts and company links: on
	InternalChannelIdentityEnabled   bool // the inbound resolver uses VERIFIED internal identities: on (nothing matches until one is verified)
	ConversationKindEnabled          bool // conversation_kind gates (no queue/placeholder ticket for internal): on
	AIAutoClassificationEnabled      bool // an AI may CONFIRM a classification by itself: off (it can only suggest)
	CRMAutoContactCreationEnabled    bool // create a contact in the CRM automatically: off
	AutoCustomerTicketUnclassifiedOn bool // open a customer ticket for an unclassified contact on its own: off
}

func DefaultFlags() Flags {
	return Flags{ContactClassificationEnabled: true, CustomerAccountsEnabled: true, InternalChannelIdentityEnabled: true, ConversationKindEnabled: true}
}

// FlagsFromEnv overrides the defaults from OMNIRA_<FLAG> variables ("true"/"false"). Unknown values keep the default.
func FlagsFromEnv(get func(string) string) Flags {
	if get == nil {
		get = os.Getenv
	}
	f := DefaultFlags()
	set := func(name string, dst *bool) {
		switch strings.ToLower(strings.TrimSpace(get(name))) {
		case "true", "1", "yes", "on":
			*dst = true
		case "false", "0", "no", "off":
			*dst = false
		}
	}
	set("OMNIRA_CONTACT_CLASSIFICATION_ENABLED", &f.ContactClassificationEnabled)
	set("OMNIRA_CUSTOMER_ACCOUNTS_ENABLED", &f.CustomerAccountsEnabled)
	set("OMNIRA_INTERNAL_CHANNEL_IDENTITY_ENABLED", &f.InternalChannelIdentityEnabled)
	set("OMNIRA_CONVERSATION_KIND_ENABLED", &f.ConversationKindEnabled)
	set("OMNIRA_AI_AUTO_CLASSIFICATION_ENABLED", &f.AIAutoClassificationEnabled)
	set("OMNIRA_CRM_AUTO_CONTACT_CREATION_ENABLED", &f.CRMAutoContactCreationEnabled)
	set("OMNIRA_AUTO_CUSTOMER_TICKET_UNCLASSIFIED_ENABLED", &f.AutoCustomerTicketUnclassifiedOn)
	return f
}
