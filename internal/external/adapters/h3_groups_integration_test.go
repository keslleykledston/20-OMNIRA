package adapters_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/external/adapters"
	"github.com/omnira/omnira/internal/external/domain"
)

// ADR-0051: the external API's read-only surface for WhatsApp groups. Groups live in their own model (ADR-0015), never mixed into
// conversations/messages/contacts — these tests exercise that it is wired the same way as the rest of the external API: scope-gated,
// tenant-scoped, channel-allowlist-narrowed, and never an enumeration oracle across tenants.

// groupsAPI mounts the group routes exactly as RegisterExternalHandlers does (the contract test guards that registration).
func (e *env) groupsAPI() {
	mw := adapters.NewMiddleware(adapters.NewPostgresLookup(e.app))
	h := adapters.NewReadHandler(e.app, nil)
	e.ext.Handle("GET /api/v1/external/groups", mw.Authenticated(adapters.RequireScope(domain.ScopeGroupReadExternal, http.HandlerFunc(h.ListGroups))))
	e.ext.Handle("GET /api/v1/external/groups/{id}/messages", mw.Authenticated(adapters.RequireScope(domain.ScopeGroupReadExternal, http.HandlerFunc(h.ListGroupMessages))))
	e.ext.Handle("GET /api/v1/external/groups/{id}/participants", mw.Authenticated(adapters.RequireScope(domain.ScopeGroupReadExternal, http.HandlerFunc(h.ListGroupParticipants))))
}

func (e *env) group(tenant, connID uuid.UUID, name string, enabled bool) uuid.UUID {
	e.n++
	id := uuid.New()
	e.exec(`INSERT INTO wa_groups(id,tenant_id,channel_connection_id,provider_group_id,name,enabled) VALUES($1,$2,$3,$4,$5,$6)`,
		id, tenant, connID, fmt.Sprintf("%015d@g.us", 100000000000000+e.n), name, enabled)
	return id
}

func (e *env) groupMessage(tenant, groupID uuid.UUID, authorName, body string, ago time.Duration) uuid.UUID {
	e.n++
	id := uuid.New()
	e.exec(`INSERT INTO wa_group_messages(id,tenant_id,group_id,provider_message_id,author_jid,author_name,from_me,message_type,body,sent_at)
	        VALUES($1,$2,$3,$4,$5,$6,false,'text',$7,$8)`,
		id, tenant, groupID, fmt.Sprintf("wamid-%d", e.n), fmt.Sprintf("%d@lid", 1000+e.n), authorName, body, time.Now().Add(-ago))
	return id
}

func (e *env) channelParticipant(tenant, connID uuid.UUID, displayName string) uuid.UUID {
	e.n++
	id := uuid.New()
	e.exec(`INSERT INTO channel_participants(id,tenant_id,channel_connection_id,provider,external_participant_id,display_name)
	        VALUES($1,$2,$3,'waha',$4,$5)`, id, tenant, connID, fmt.Sprintf("%d@lid", 2000+e.n), displayName)
	return id
}

func (e *env) groupMessageFrom(tenant, groupID, participantID uuid.UUID, authorName, body string, ago time.Duration) uuid.UUID {
	e.n++
	id := uuid.New()
	e.exec(`INSERT INTO wa_group_messages(id,tenant_id,group_id,provider_message_id,author_jid,author_name,from_me,message_type,body,sent_at,sender_channel_participant_id)
	        VALUES($1,$2,$3,$4,'',$5,false,'text',$6,$7,$8)`,
		id, tenant, groupID, fmt.Sprintf("wamid-%d", e.n), authorName, body, time.Now().Add(-ago), participantID)
	return id
}

// OMNIRA_API_PENDENCIAS_CRM.md §3.1/§3.2, ADR-0051: list enabled groups, narrowed by channel allowlist, scoped by group.read.external,
// never crossing tenants, never listing a disabled group.
func TestReadAPIListGroupsIsEnabledOnlyAllowlistedAndTenantScoped(t *testing.T) {
	e := newEnv(t)
	e.groupsAPI()
	a, b := e.tenant(), e.tenant()
	adminA, adminB := e.member(a, "tenant_admin"), e.member(b, "tenant_admin")
	connA1, connA2 := e.connection(a), e.connection(a)
	connB := e.connection(b)

	gVisible := e.group(a, connA1, "Equipe A1", true)
	gOtherConn := e.group(a, connA2, "Equipe A2", true)
	e.group(a, connA1, "Desabilitado", false) // enabled=false: excluded
	gB := e.group(b, connB, "Equipe B", true)

	full := e.install(adminA, a, "CRM full", append(append([]string{}, readScopes...), "group.read.external")...)
	code, body := e.admin(adminA, a, http.MethodPost, "", map[string]any{"name": "limited", "scopes": append(append([]string{}, readScopes...), "group.read.external"), "channel_ids": []string{connA1.String()}})
	if code != http.StatusCreated {
		t.Fatalf("create limited = %d %s", code, body)
	}
	var limited created
	_ = json.Unmarshal(body, &limited)
	other := e.install(adminB, b, "CRM B", append(append([]string{}, readScopes...), "group.read.external")...)
	noScope := e.install(adminA, a, "CRM no group scope", readScopes...)

	groups := func(token string) (int, []uuid.UUID) {
		code, raw, _ := e.callExt(http.MethodGet, "/api/v1/external/groups", token, nil)
		var doc struct {
			Items []struct {
				ID uuid.UUID `json:"id"`
			} `json:"items"`
		}
		_ = json.Unmarshal(raw, &doc)
		var out []uuid.UUID
		for _, it := range doc.Items {
			out = append(out, it.ID)
		}
		return code, out
	}

	code, got := groups(full.Credential.Token)
	if code != http.StatusOK {
		t.Fatalf("groups = %d", code)
	}
	sameSet(t, "A sees enabled groups across its own connections", got, setOf(gVisible, gOtherConn))
	if _, got := groups(limited.Credential.Token); len(got) != 1 || got[0] != gVisible {
		t.Errorf("the channel allow-list narrows the list: %v", got)
	}
	if _, got := groups(other.Credential.Token); len(got) != 1 || got[0] != gB {
		t.Errorf("another tenant sees only its own: %v", got)
	}
	if code, _ := groups(noScope.Credential.Token); code != http.StatusForbidden {
		t.Errorf("without group.read.external = %d, want 403", code)
	}
}

// ADR-0051 §2/§5: a group's message history and its participants never leak a phone number or WhatsApp JID, and a group that is not
// visible to the installation (wrong tenant, disabled, outside the channel allowlist) answers the same 404 as one that never existed.
func TestReadAPIGroupMessagesAndParticipantsAreNameOnlyAndNeverCrossTenants(t *testing.T) {
	e := newEnv(t)
	e.groupsAPI()
	a, b := e.tenant(), e.tenant()
	adminA := e.member(a, "tenant_admin")
	connA := e.connection(a)
	connB := e.connection(b)

	g := e.group(a, connA, "Equipe A", true)
	disabled := e.group(a, connA, "Desabilitado", false)
	gB := e.group(b, connB, "Equipe B", true)

	alice := e.channelParticipant(a, connA, "Alice")
	e.groupMessageFrom(a, g, alice, "Alice", "oi equipe", 2*time.Minute)
	e.groupMessage(a, g, "Bob", "tudo certo", time.Minute)

	app := e.install(adminA, a, "CRM", append(append([]string{}, readScopes...), "group.read.external")...)

	code, raw, _ := e.callExt(http.MethodGet, "/api/v1/external/groups/"+g.String()+"/messages", app.Credential.Token, nil)
	if code != http.StatusOK {
		t.Fatalf("messages = %d %s", code, raw)
	}
	var page struct {
		Items []struct {
			AuthorName string `json:"author_name"`
			Body       string `json:"body"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("messages items = %d, want 2", len(page.Items))
	}
	for _, it := range page.Items {
		if it.AuthorName == "" {
			t.Errorf("author_name must never be empty for a stored author: %+v", it)
		}
	}
	if raw := string(raw); strings.Contains(raw, "@lid") || strings.Contains(raw, "author_jid") {
		t.Errorf("a phone-shaped JID or author_jid leaked into the message feed: %s", raw)
	}

	code, raw, _ = e.callExt(http.MethodGet, "/api/v1/external/groups/"+g.String()+"/participants", app.Credential.Token, nil)
	if code != http.StatusOK {
		t.Fatalf("participants = %d %s", code, raw)
	}
	var participants struct {
		Items []struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &participants); err != nil {
		t.Fatal(err)
	}
	if len(participants.Items) != 1 || participants.Items[0].ID != alice || participants.Items[0].Name != "Alice" {
		t.Errorf("participants = %+v, want exactly Alice", participants.Items)
	}
	if strings.Contains(string(raw), "@lid") {
		t.Errorf("a WhatsApp JID leaked into the participants list: %s", raw)
	}

	// no enumeration oracle: disabled, cross-tenant and non-existent all answer the same 404.
	for _, id := range []uuid.UUID{disabled, gB, uuid.New()} {
		if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/groups/"+id.String()+"/messages", app.Credential.Token, nil); code != http.StatusNotFound {
			t.Errorf("messages of %s = %d, want 404", id, code)
		}
		if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/groups/"+id.String()+"/participants", app.Credential.Token, nil); code != http.StatusNotFound {
			t.Errorf("participants of %s = %d, want 404", id, code)
		}
	}
}
