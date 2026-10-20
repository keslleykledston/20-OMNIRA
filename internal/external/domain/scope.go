// Package domain is the vocabulary of the external (Headless) integration, ADR-0048: what an installed application is allowed to do,
// what a credential looks like and who the authenticated caller is. No SQL, no HTTP.
package domain

import (
	"fmt"
	"sort"
	"strings"
)

// Scope is one capability an installation may hold. The list is closed and mirrors the CHECK on external_app_installations.scopes.
type Scope string

const (
	ScopeConversationRead     Scope = "conversation.read"
	ScopeMessageRead          Scope = "message.read"
	ScopeMediaRead            Scope = "media.read"
	ScopeContactRead          Scope = "contact.read"
	ScopeEventsRead           Scope = "events.read"
	ScopeConversationClaim    Scope = "conversation.claim"
	ScopeConversationTransfer Scope = "conversation.transfer"
	ScopeConversationFinalize Scope = "conversation.finalize"
	ScopeMessageSend          Scope = "message.send"
	// ScopeServiceRead lets the application hear about the company's service protocols, cases, tasks and ERP records (ADR-0049 gate 4): the events, never the
	// conversation itself (that is conversation.read). It never changes anything.
	ScopeServiceRead Scope = "service.read"
	// ScopeTemplateRead (ADR-0052) lets the application list the company's APPROVED, sendable WhatsApp templates — never sync, create or edit one
	// (that stays exclusive to channel.manage in the Workspace), and never a template outside the installation's channel allow-list.
	ScopeTemplateRead Scope = "template.read"
	// ScopeMessageNote (ADR-0053) lets the application record an internal note on a conversation it holds — a message that is NEVER queued for
	// delivery. Independent of message.send: an installation may hold one without the other.
	ScopeMessageNote Scope = "message.note"
	// ScopeGroupReadExternal (ADR-0051) lets the application list the company's ENABLED WhatsApp groups and read their message history —
	// never a group's participant phone/JID (name only, same minimization as the Workspace's own group.read), and never group.manage
	// (enable/disable/purge history stays exclusive to the Workspace). Independent of group.read, the Workspace-user permission: a
	// closed-list scope of its own, same pattern as every other capability in this catalog.
	ScopeGroupReadExternal Scope = "group.read.external"
	// ScopeContactWrite (ADR-0050) lets the application find-or-create a contact by phone — only what POST /conversations needs to open an
	// outbound conversation, never an arbitrary field edit (there is no general "edit a contact" capability in this catalog).
	ScopeContactWrite Scope = "contact.write"
	// ScopeConversationOpen (ADR-0050) lets the application create-or-reuse a conversation by phone (POST /conversations). Independent of
	// conversation.claim: creating a NEW conversation this way auto-claims it for the caller (there was no prior holder to take from), but
	// reusing one already held by someone else is 409 held_by_other — this scope never substitutes for conversation.claim on a pre-existing one.
	ScopeConversationOpen Scope = "conversation.open"
)

// AllScopes is every scope in a stable order.
var AllScopes = []Scope{
	ScopeConversationRead, ScopeMessageRead, ScopeMediaRead, ScopeContactRead, ScopeEventsRead,
	ScopeConversationClaim, ScopeConversationTransfer, ScopeConversationFinalize, ScopeMessageSend, ScopeServiceRead, ScopeTemplateRead, ScopeMessageNote,
	ScopeGroupReadExternal, ScopeContactWrite, ScopeConversationOpen,
}

// ReadScopes are the scopes that never change anything.
var ReadScopes = []Scope{
	ScopeConversationRead, ScopeMessageRead, ScopeMediaRead, ScopeContactRead, ScopeEventsRead, ScopeServiceRead, ScopeTemplateRead,
	ScopeGroupReadExternal,
}

func (s Scope) Valid() bool {
	for _, k := range AllScopes {
		if k == s {
			return true
		}
	}
	return false
}

// ParseScopes validates, de-duplicates and sorts. An unknown scope is an error (never silently dropped): a typo must not grant less or
// more than the administrator meant.
func ParseScopes(raw []string) ([]Scope, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("at least one scope is required")
	}
	seen := map[Scope]bool{}
	out := make([]Scope, 0, len(raw))
	for _, r := range raw {
		s := Scope(strings.TrimSpace(r))
		if !s.Valid() {
			return nil, fmt.Errorf("unknown scope %q", r)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// ScopeStrings is the wire/DB form.
func ScopeStrings(scopes []Scope) []string {
	out := make([]string, len(scopes))
	for i, s := range scopes {
		out[i] = string(s)
	}
	return out
}
