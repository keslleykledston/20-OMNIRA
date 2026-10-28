package adapters

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/external/domain"
)

// Visibility is THE definition of which conversations an installation may see (ADR-0048 §2.4, Codex H0 HIGH): the company of the installation, never a
// conversation with staff (internal_user_id set, or conversation_kind internal), never a contact classified internal or spam (the derivation of
// conversation_kind files spam under external_other, so the contact's own classification has to be asked), and, when the administrator limited the
// installation, only its queues and its lines. Every read of the external API — lists, one conversation, messages, contacts, media, snapshot and events —
// builds its WHERE clause here, so the rule cannot be forgotten in one endpoint.
type Visibility struct {
	TenantID   uuid.UUID
	QueueIDs   []uuid.UUID
	ChannelIDs []uuid.UUID
	// ServiceRead: the installation holds the scope service.read (ADR-0049 gate 4). Without it the journal's service facts are invisible, whatever the conversation.
	ServiceRead bool
}

// Restricted says the administrator limited the installation to some queues or lines.
func (v Visibility) Restricted() bool { return len(v.QueueIDs) > 0 || len(v.ChannelIDs) > 0 }

// NewVisibility reads the tenant from the authenticated installation, never from the request.
func NewVisibility(p *domain.Principal, a Allowlists) Visibility {
	return Visibility{TenantID: p.TenantID, QueueIDs: a.QueueIDs, ChannelIDs: a.ChannelIDs, ServiceRead: p.Has(domain.ScopeServiceRead)}
}

// Args collects positional parameters while a query is built.
type Args struct{ vals []any }

func (a *Args) Add(v any) string {
	a.vals = append(a.vals, v)
	return fmt.Sprintf("$%d", len(a.vals))
}

func (a *Args) Values() []any { return a.vals }

// Conversation returns the predicate for conversations aliased `alias`, adding its parameters to args.
func (v Visibility) Conversation(alias string, args *Args) string {
	t := args.Add(v.TenantID)
	parts := []string{
		alias + ".tenant_id = " + t,
		alias + ".internal_user_id IS NULL",
		alias + ".conversation_kind <> 'internal'",
		"EXISTS (SELECT 1 FROM contacts vc WHERE vc.tenant_id = " + alias + ".tenant_id AND vc.id = " + alias + ".contact_id AND vc.kind NOT IN ('internal', 'spam'))",
	}
	if len(v.QueueIDs) > 0 {
		parts = append(parts, alias+".queue_id = ANY("+args.Add(v.QueueIDs)+"::uuid[])")
	}
	if len(v.ChannelIDs) > 0 {
		parts = append(parts, alias+".channel_connection_id = ANY("+args.Add(v.ChannelIDs)+"::uuid[])")
	}
	return strings.Join(parts, " AND ")
}

// Group returns the predicate for wa_groups aliased `alias` (ADR-0051): the tenant, only ENABLED groups (a disabled group is invisible to the
// external API exactly as it is to a Workspace user without group.manage history access — there is no "pending" state to leak), and, when the
// administrator limited the installation's channel allow-list, only groups on one of those WhatsApp lines.
func (v Visibility) Group(alias string, args *Args) string {
	t := args.Add(v.TenantID)
	parts := []string{alias + ".tenant_id = " + t, alias + ".enabled"}
	if len(v.ChannelIDs) > 0 {
		parts = append(parts, alias+".channel_connection_id = ANY("+args.Add(v.ChannelIDs)+"::uuid[])")
	}
	return strings.Join(parts, " AND ")
}
