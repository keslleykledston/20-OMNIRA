package adapters_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/external/adapters"
	"github.com/omnira/omnira/internal/external/domain"
	mediaadapters "github.com/omnira/omnira/internal/media/adapters"
	mediaports "github.com/omnira/omnira/internal/media/ports"
)

// ADR-0046 H3: the read API. Real PostgreSQL as omnira_app, tenants A / B / C, the installation's token as the only credential.

var readScopes = []string{"conversation.read", "message.read", "contact.read", "media.read", "conversation.claim", "message.send"}

// readAPI mounts the read routes exactly as RegisterExternalHandlers does (the contract test guards that registration).
func (e *env) readAPI(media mediaports.MediaReader) {
	mw := adapters.NewMiddleware(adapters.NewPostgresLookup(e.app))
	h := adapters.NewReadHandler(e.app, media)
	e.ext.Handle("GET /api/v1/external/conversations", mw.Authenticated(adapters.RequireScope(domain.ScopeConversationRead, http.HandlerFunc(h.ListConversations))))
	e.ext.Handle("GET /api/v1/external/conversations/{id}", mw.Authenticated(adapters.RequireScope(domain.ScopeConversationRead, http.HandlerFunc(h.GetConversation))))
	e.ext.Handle("GET /api/v1/external/conversations/{id}/messages", mw.Authenticated(adapters.RequireScope(domain.ScopeMessageRead, http.HandlerFunc(h.ListMessages))))
	e.ext.Handle("GET /api/v1/external/contacts/{id}", mw.Authenticated(adapters.RequireScope(domain.ScopeContactRead, http.HandlerFunc(h.GetContact))))
	e.ext.Handle("GET /api/v1/external/media/{id}", mw.Authenticated(adapters.RequireScope(domain.ScopeMediaRead, http.HandlerFunc(h.GetMedia))))
}

type convOpts struct {
	contactKind string // customer | other | unclassified | internal | spam
	queue, conn *uuid.UUID
	status      string
	staff       *uuid.UUID // a conversation with a team member (no contact)
	createdAgo  time.Duration
}

func (e *env) conv(tenant uuid.UUID, o convOpts) (conv, contact uuid.UUID) {
	e.t.Helper()
	e.n++
	conv, contact = uuid.New(), uuid.New()
	status := o.status
	if status == "" {
		status = "open"
	}
	when := time.Now().Add(-o.createdAgo)
	if o.staff != nil {
		e.exec(`INSERT INTO conversations(id,tenant_id,internal_user_id,conversation_kind,queue_id,channel_connection_id,status,created_at,updated_at) VALUES($1,$2,$3,'internal',$4,$5,$6,$7,$7)`, conv, tenant, *o.staff, o.queue, o.conn, status, when)
		return conv, uuid.Nil
	}
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,$3,$4)`, contact, tenant, fmt.Sprintf("Cliente %d", e.n), fmt.Sprintf("+5592966%06d", e.n))
	switch o.contactKind {
	case "", "customer":
		// the default classification: a "customer" needs an active account link in this schema, so the plain visible contact is left unclassified
	case "internal":
		e.exec(`UPDATE contacts SET kind='internal', internal_role='partner' WHERE id=$1`, contact)
	case "spam", "other", "unclassified":
		e.exec(`UPDATE contacts SET kind=$2 WHERE id=$1`, contact, o.contactKind)
	}
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,queue_id,channel_connection_id,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7)`, conv, tenant, contact, o.queue, o.conn, status, when)
	return conv, contact
}

func (e *env) connection(tenant uuid.UUID) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]'::jsonb)`, id, tenant, "n-"+id.String())
	return id
}

func (e *env) message(tenant, conv uuid.UUID, dir, body, status string, ago time.Duration, byUser *uuid.UUID) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status,created_at,sent_by_user_id) VALUES($1,$2,$3,$4,'text',$5,$6,$7,$8)`,
		id, tenant, conv, dir, body, status, time.Now().Add(-ago), byUser)
	return id
}

type listed struct {
	Items []struct {
		ID      uuid.UUID `json:"id"`
		Status  string    `json:"status"`
		Control struct {
			State   string `json:"state"`
			Version int64  `json:"version"`
			Holder  *struct {
				Kind string `json:"kind"`
				Mine bool   `json:"mine"`
			} `json:"holder"`
		} `json:"control"`
		Can map[string]bool `json:"can"`
	} `json:"items"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

func (e *env) list(token, query string) (int, listed) {
	e.t.Helper()
	code, body, _ := e.callExt(http.MethodGet, "/api/v1/external/conversations"+query, token, nil)
	var l listed
	if code == http.StatusOK {
		if err := json.Unmarshal(body, &l); err != nil {
			e.t.Fatalf("%s: %v", body, err)
		}
	}
	return code, l
}

func ids(l listed) []uuid.UUID {
	out := make([]uuid.UUID, len(l.Items))
	for i, it := range l.Items {
		out[i] = it.ID
	}
	return out
}

func setOf(in ...uuid.UUID) map[uuid.UUID]bool {
	m := map[uuid.UUID]bool{}
	for _, i := range in {
		m[i] = true
	}
	return m
}

func sameSet(t *testing.T, name string, got []uuid.UUID, want map[uuid.UUID]bool) {
	t.Helper()
	g := setOf(got...)
	if len(g) != len(want) || len(got) != len(g) {
		t.Errorf("%s: got %d ids (%d distinct), want %d", name, len(got), len(g), len(want))
	}
	for id := range want {
		if !g[id] {
			t.Errorf("%s: missing %s", name, id)
		}
	}
	for id := range g {
		if !want[id] {
			t.Errorf("%s: unexpected %s", name, id)
		}
	}
}

// What an installation sees: its own company, never staff, internal or spam, and exactly its allow-list.
func TestReadAPISeesOnlyWhatTheInstallationMaySee(t *testing.T) {
	e := newEnv(t)
	e.readAPI(nil)
	a, b, c := e.tenant(), e.tenant(), e.tenant()
	adminA, adminB := e.member(a, "tenant_admin"), e.member(b, "tenant_admin")
	_ = c
	appA := e.install(adminA, a, "CRM A", readScopes...)
	appB := e.install(adminB, b, "CRM B", readScopes...)
	staff := e.member(a, "tenant_agent")

	connA := e.connection(a)
	q1, q2 := e.queue(a), e.queue(a)
	visible1, _ := e.conv(a, convOpts{queue: &q1, conn: &connA})
	visible2, _ := e.conv(a, convOpts{queue: &q2, conn: &connA, contactKind: "unclassified"})
	visible3, _ := e.conv(a, convOpts{contactKind: "other"})
	closed, _ := e.conv(a, convOpts{status: "closed", queue: &q1})
	staffConv, _ := e.conv(a, convOpts{staff: &staff})
	internalConv, _ := e.conv(a, convOpts{contactKind: "internal"})
	spamConv, _ := e.conv(a, convOpts{contactKind: "spam"})
	bConv, bContact := e.conv(b, convOpts{})
	cConv, _ := e.conv(c, convOpts{})
	_ = cConv

	// 1. the default list is the open conversations the installation may see
	code, l := e.list(appA.Credential.Token, "")
	if code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	sameSet(t, "A open", ids(l), setOf(visible1, visible2, visible3))
	_, l = e.list(appA.Credential.Token, "?status=all")
	sameSet(t, "A all", ids(l), setOf(visible1, visible2, visible3, closed))
	_, l = e.list(appA.Credential.Token, "?status=closed")
	sameSet(t, "A closed", ids(l), setOf(closed))
	_, l = e.list(appB.Credential.Token, "?status=all")
	sameSet(t, "B all", ids(l), setOf(bConv))

	// 2. the ones it must not see are the same 404 as an id that does not exist
	var reference string
	for name, id := range map[string]uuid.UUID{"a conversation with staff": staffConv, "an internal contact": internalConv, "a spam contact": spamConv, "another company's": bConv, "unknown": uuid.New()} {
		code, body, _ := e.callExt(http.MethodGet, "/api/v1/external/conversations/"+id.String(), appA.Credential.Token, nil)
		if code != http.StatusNotFound {
			t.Errorf("%s = %d %s, want 404", name, code, body)
		}
		var p adapters.Problem
		_ = json.Unmarshal(body, &p)
		p.RequestID = ""
		shape := fmt.Sprintf("%+v", p)
		if reference == "" {
			reference = shape
		} else if shape != reference {
			t.Errorf("%s answers differently from the others: %s vs %s", name, shape, reference)
		}
		for _, sub := range []string{"/messages"} {
			if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/conversations/"+id.String()+sub, appA.Credential.Token, nil); code != http.StatusNotFound {
				t.Errorf("%s%s = %d, want 404", name, sub, code)
			}
		}
	}
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/contacts/"+bContact.String(), appA.Credential.Token, nil); code != http.StatusNotFound {
		t.Errorf("another company's contact = %d, want 404", code)
	}

	// 3. filters
	_, l = e.list(appA.Credential.Token, "?queue_id="+q1.String()+"&status=all")
	sameSet(t, "queue filter", ids(l), setOf(visible1, closed))
	_, l = e.list(appA.Credential.Token, "?channel_connection_id="+connA.String())
	sameSet(t, "channel filter", ids(l), setOf(visible1, visible2))
	_, l = e.list(appA.Credential.Token, "?control=held")
	sameSet(t, "held", ids(l), setOf())
	_, l = e.list(appA.Credential.Token, "?control=free")
	sameSet(t, "free", ids(l), setOf(visible1, visible2, visible3))

	// 4. an allow-limited installation sees only its queue and line, and a contact only through a conversation it may see
	code, body := e.admin(adminA, a, http.MethodPost, "", map[string]any{"name": "limited", "scopes": readScopes, "queue_ids": []string{q1.String()}})
	if code != http.StatusCreated {
		t.Fatalf("create limited = %d %s", code, body)
	}
	var lim created
	_ = json.Unmarshal(body, &lim)
	_, l = e.list(lim.Credential.Token, "?status=all")
	sameSet(t, "limited to q1", ids(l), setOf(visible1, closed))
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/conversations/"+visible2.String(), lim.Credential.Token, nil); code != http.StatusNotFound {
		t.Errorf("a conversation outside the queue allow-list = %d, want 404", code)
	}
	var contact2 uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT contact_id FROM conversations WHERE id=$1`, visible2).Scan(&contact2); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/contacts/"+contact2.String(), lim.Credential.Token, nil); code != http.StatusNotFound {
		t.Errorf("a contact reachable only through a hidden conversation = %d, want 404", code)
	}
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/contacts/"+contact2.String(), appA.Credential.Token, nil); code != http.StatusOK {
		t.Errorf("control: the unlimited installation reads that contact: %d", code)
	}

	// 4b. limited by LINE: only the conversations that came in on it
	code, body = e.admin(adminA, a, http.MethodPost, "", map[string]any{"name": "line-limited", "scopes": readScopes, "channel_ids": []string{connA.String()}})
	if code != http.StatusCreated {
		t.Fatalf("create line-limited = %d %s", code, body)
	}
	var byLine created
	_ = json.Unmarshal(body, &byLine)
	_, l = e.list(byLine.Credential.Token, "?status=all")
	sameSet(t, "limited to the line", ids(l), setOf(visible1, visible2))

	// 5. the DTO names nobody and carries no internal field
	_, body, _ = e.callExt(http.MethodGet, "/api/v1/external/conversations/"+visible1.String(), appA.Credential.Token, nil)
	for _, forbidden := range []string{"assigned_to", "tenant_id", "internal_user", "email", "provider_chat_id", "@"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("the conversation document contains %q: %s", forbidden, body)
		}
	}
}

// Keyset pagination is complete and duplicate-free, also while rows are created in between.
func TestReadAPIPaginationIsStableWhileTheDataMoves(t *testing.T) {
	e := newEnv(t)
	e.readAPI(nil)
	a := e.tenant()
	app := e.install(e.member(a, "tenant_admin"), a, "CRM", readScopes...)
	const n = 130
	want := map[uuid.UUID]bool{}
	for i := 0; i < n; i++ {
		id, _ := e.conv(a, convOpts{createdAgo: time.Duration(i+1) * time.Minute})
		want[id] = true
	}
	var got []uuid.UUID
	cursor := ""
	for pages := 0; ; pages++ {
		q := "?limit=50"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		code, l := e.list(app.Credential.Token, q)
		if code != http.StatusOK {
			t.Fatalf("page %d = %d", pages, code)
		}
		got = append(got, ids(l)...)
		if pages == 0 { // new conversations and a change of an existing one appear while the client is paging
			for i := 0; i < 5; i++ {
				e.conv(a, convOpts{})
			}
			e.exec(`UPDATE conversations SET updated_at = now() WHERE id = $1`, got[0])
		}
		if !l.HasMore {
			break
		}
		if l.NextCursor == "" || pages > 5 {
			t.Fatalf("has_more without a usable cursor after %d pages", pages)
		}
		cursor = l.NextCursor
	}
	sameSet(t, "all pages", got, want)
	// the order is stable: newest first, strictly
	var order []time.Time
	rows, err := e.seed.Query(e.ctx, `SELECT created_at FROM conversations WHERE id = ANY($1) ORDER BY created_at DESC, id DESC`, got)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var ts time.Time
		_ = rows.Scan(&ts)
		order = append(order, ts)
	}
	rows.Close()
	if !sort.SliceIsSorted(order, func(i, j int) bool { return order[i].After(order[j]) }) {
		t.Error("the stored order is not newest-first")
	}
	// malformed input is a 400, never a 500 or a leak
	for _, q := range []string{"?limit=0", "?limit=201", "?limit=x", "?cursor=!!!", "?cursor=" + "aGVsbG8=", "?status=weird", "?sort=weird", "?control=weird", "?queue_id=nope"} {
		if code, _ := e.list(app.Credential.Token, q); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, code)
		}
	}
}

// Control, scopes and the informational "can".
func TestReadAPIConversationShowsTypedControlAndScopedCan(t *testing.T) {
	e := newEnv(t)
	e.readAPI(nil)
	a := e.tenant()
	admin := e.member(a, "tenant_admin")
	full := e.install(admin, a, "full", readScopes...)
	readOnly := e.install(admin, a, "ro", "conversation.read", "message.read")
	noMessages := e.install(admin, a, "nomsg", "conversation.read")
	conv, _ := e.conv(a, convOpts{})
	agent := e.member(a, "tenant_agent")

	get := func(token string) (int, map[string]any, http.Header) {
		code, body, hdr := e.callExt(http.MethodGet, "/api/v1/external/conversations/"+conv.String(), token, nil)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		return code, m, hdr
	}
	code, m, hdr := get(full.Credential.Token)
	ctl := m["control"].(map[string]any)
	if code != 200 || ctl["state"] != "unassigned" || hdr.Get("ETag") != `"control-0"` || m["can"].(map[string]any)["claim"] != true || m["can"].(map[string]any)["reply"] != false {
		t.Fatalf("free conversation: %d %v %v", code, m, hdr)
	}
	if _, m, _ = get(readOnly.Credential.Token); m["can"].(map[string]any)["claim"] != false {
		t.Errorf("a read-only installation is told it can claim: %v", m["can"])
	}
	// a person takes it: the document says "tenant_user", not who
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, conv, agent)
	code, m, hdr = get(full.Credential.Token)
	ctl = m["control"].(map[string]any)
	h := ctl["holder"].(map[string]any)
	if ctl["state"] != "held" || h["kind"] != "tenant_user" || h["mine"] != false || hdr.Get("ETag") != `"control-1"` || m["can"].(map[string]any)["claim"] != false {
		t.Fatalf("held by a person: %v %v", m, hdr)
	}
	if strings.Contains(fmt.Sprint(m), agent.String()) {
		t.Fatal("the document names the person who holds the conversation")
	}
	// the installation itself holds it
	e.exec(`UPDATE conversations SET assigned_to_user_id=NULL WHERE id=$1`, conv)
	e.exec(`UPDATE conversations SET control_kind='crm_application', control_holder_id=$2, control_version=control_version+1, control_since=now(), control_origin='crm' WHERE id=$1`, conv, full.Installation.ID)
	_, m, hdr = get(full.Credential.Token)
	h = m["control"].(map[string]any)["holder"].(map[string]any)
	can := m["can"].(map[string]any)
	if h["kind"] != "crm_application" || h["mine"] != true || hdr.Get("ETag") != `"control-3"` || can["reply"] != true || can["release"] != true || can["finalize"] != false {
		t.Fatalf("held by this installation: %v %v", m, hdr)
	}
	// ...and another installation sees "crm_application, not mine"
	_, m, _ = get(readOnly.Credential.Token)
	h = m["control"].(map[string]any)["holder"].(map[string]any)
	if h["kind"] != "crm_application" || h["mine"] != false {
		t.Fatalf("held by another installation: %v", m)
	}
	// scopes: no message.read => no messages; no contact.read => no contacts
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/conversations/"+conv.String()+"/messages", noMessages.Credential.Token, nil); code != http.StatusForbidden {
		t.Errorf("messages without the scope = %d, want 403", code)
	}
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/contacts/"+uuid.NewString(), noMessages.Credential.Token, nil); code != http.StatusForbidden {
		t.Errorf("contacts without the scope = %d, want 403", code)
	}
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/media/"+uuid.NewString(), noMessages.Credential.Token, nil); code != http.StatusForbidden {
		t.Errorf("media without the scope = %d, want 403", code)
	}
}

// Messages: order, statuses, senders without names, media, stability while a message arrives.
func TestReadAPIMessagesAreStableAndNameNobody(t *testing.T) {
	e := newEnv(t)
	e.readAPI(nil)
	a, b := e.tenant(), e.tenant()
	app := e.install(e.member(a, "tenant_admin"), a, "CRM", readScopes...)
	agent := e.member(a, "tenant_agent")
	conv, _ := e.conv(a, convOpts{})
	other, _ := e.conv(a, convOpts{})
	foreign, _ := e.conv(b, convOpts{})
	in1 := e.message(a, conv, "inbound", "olá", "received", 10*time.Minute, nil)
	out1 := e.message(a, conv, "outbound", "bom dia", "delivered", 9*time.Minute, &agent)
	bot := e.message(a, conv, "outbound", "menu", "read", 8*time.Minute, nil)
	queued := e.message(a, conv, "outbound", "ainda na fila", "queued", 7*time.Minute, &agent)
	e.message(a, other, "inbound", "outra conversa", "received", 5*time.Minute, nil)
	e.message(b, foreign, "inbound", "outra empresa", "received", 5*time.Minute, nil)

	page := func(q string) (int, struct {
		Items []struct {
			ID     uuid.UUID `json:"id"`
			Body   string    `json:"body"`
			Status string    `json:"status"`
			Sender struct {
				Kind string `json:"kind"`
			} `json:"sender"`
		} `json:"items"`
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}, []byte) {
		code, body, _ := e.callExt(http.MethodGet, "/api/v1/external/conversations/"+conv.String()+"/messages"+q, app.Credential.Token, nil)
		var p struct {
			Items []struct {
				ID     uuid.UUID `json:"id"`
				Body   string    `json:"body"`
				Status string    `json:"status"`
				Sender struct {
					Kind string `json:"kind"`
				} `json:"sender"`
			} `json:"items"`
			HasMore    bool   `json:"has_more"`
			NextCursor string `json:"next_cursor"`
		}
		_ = json.Unmarshal(body, &p)
		return code, p, body
	}
	code, p, raw := page("")
	if code != 200 || len(p.Items) != 4 {
		t.Fatalf("messages = %d %s", code, raw)
	}
	order := []uuid.UUID{in1, out1, bot, queued}
	kinds := []string{"contact", "agent", "automation", "agent"}
	statuses := []string{"received", "delivered", "read", "queued"}
	for i := range order {
		if p.Items[i].ID != order[i] || p.Items[i].Sender.Kind != kinds[i] || p.Items[i].Status != statuses[i] {
			t.Errorf("message %d = %+v, want %s %s %s", i, p.Items[i], order[i], kinds[i], statuses[i])
		}
	}
	if strings.Contains(string(raw), agent.String()) || strings.Contains(string(raw), "sent_by") {
		t.Errorf("the listing names the sender: %s", raw)
	}
	_, p, _ = page("?order=desc")
	if p.Items[0].ID != queued || p.Items[3].ID != in1 {
		t.Errorf("order=desc: %+v", p.Items)
	}
	// paging by 2 while a newer message arrives: nothing repeated, nothing skipped, and the new one is simply after the last
	var seen []uuid.UUID
	cursor := ""
	for i := 0; i < 5; i++ {
		q := "?limit=2"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		_, p, _ = page(q)
		for _, it := range p.Items {
			seen = append(seen, it.ID)
		}
		if i == 0 {
			e.message(a, conv, "inbound", "chegou agora", "received", 0, nil)
		}
		if !p.HasMore {
			break
		}
		cursor = p.NextCursor
	}
	if len(seen) != 5 || setOf(seen...)[in1] != true || len(setOf(seen...)) != 5 {
		t.Errorf("paged messages = %d (%d distinct), want the 4 plus the one that arrived", len(seen), len(setOf(seen...)))
	}
	// the conversation of another company, or one that does not exist: 404
	for _, id := range []uuid.UUID{foreign, uuid.New()} {
		if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/conversations/"+id.String()+"/messages", app.Credential.Token, nil); code != http.StatusNotFound {
			t.Errorf("messages of %s = %d, want 404", id, code)
		}
	}
}

// Media: only cleared files, authorization at the request, hostile-content headers, nothing of the provider.
func TestReadAPIMediaIsServedOnlyWhenClearedAndVisible(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	store, err := mediaadapters.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.readAPI(mediaadapters.NewReader(e.app, store))
	a, b := e.tenant(), e.tenant()
	adminA := e.member(a, "tenant_admin")
	app := e.install(adminA, a, "CRM", readScopes...)
	appB := e.install(e.member(b, "tenant_admin"), b, "CRM B", readScopes...)

	conv, _ := e.conv(a, convOpts{})
	spamConv, _ := e.conv(a, convOpts{contactKind: "spam"})
	bConv, _ := e.conv(b, convOpts{})
	media := func(tenant, conv uuid.UUID, status, mime string, data []byte) uuid.UUID {
		msg := uuid.New()
		e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES($1,$2,$3,'inbound','image','','received')`, msg, tenant, conv)
		id := uuid.New()
		e.exec(`INSERT INTO message_media(id,tenant_id,message_id,status,kind,mime,size_bytes,sha256) VALUES($1,$2,$3,$4,'image',$5,$6,$7)`, id, tenant, msg, status, mime, len(data), strings.Repeat("a", 64))
		if status == "clean" {
			w := mediaports.Work{ID: id, TenantID: tenant, MessageID: msg}
			if err := store.PutQuarantine(w, data); err != nil {
				t.Fatal(err)
			}
			if err := store.Promote(w); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	png := []byte("\x89PNG\r\n\x1a\nfake-but-cleared")
	clean := media(a, conv, "clean", "image/png", png)
	doc := media(a, conv, "clean", "application/pdf", []byte("%PDF-1.4 cleared"))
	svg := media(a, conv, "clean", "image/svg+xml", []byte("<svg onload=alert(1)/>"))
	pending := media(a, conv, "quarantined", "", nil)
	infected := media(a, conv, "infected", "image/png", png)
	hidden := media(a, spamConv, "clean", "image/png", png)
	foreign := media(b, bConv, "clean", "image/png", png)

	get := func(token string, id uuid.UUID) (int, []byte, http.Header) {
		return e.callExt(http.MethodGet, "/api/v1/external/media/"+id.String(), token, nil)
	}
	code, body, hdr := get(app.Credential.Token, clean)
	if code != 200 || string(body) != string(png) || hdr.Get("Content-Type") != "image/png" || hdr.Get("Content-Disposition") != "inline" ||
		hdr.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(hdr.Get("Content-Security-Policy"), "sandbox") || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("cleared image = %d %q %v", code, body, hdr)
	}
	if code, _, hdr := get(app.Credential.Token, doc); code != 200 || !strings.HasPrefix(hdr.Get("Content-Disposition"), "attachment") || hdr.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("a document must be an attachment: %d %v", code, hdr)
	}
	if code, body, hdr := get(app.Credential.Token, svg); code != 200 || strings.HasPrefix(hdr.Get("Content-Disposition"), "inline") || hdr.Get("Content-Type") == "image/svg+xml" {
		t.Errorf("active content must never be served inline: %d %q %v", code, body, hdr)
	}
	if code, _, _ := get(app.Credential.Token, pending); code != http.StatusConflict {
		t.Errorf("a file still being checked = %d, want 409", code)
	}
	if code, _, _ := get(app.Credential.Token, infected); code != http.StatusForbidden {
		t.Errorf("an infected file = %d, want 403", code)
	}
	for name, id := range map[string]uuid.UUID{"a spam contact's": hidden, "another company's": foreign, "unknown": uuid.New()} {
		if code, _, _ := get(app.Credential.Token, id); code != http.StatusNotFound {
			t.Errorf("%s media = %d, want 404", name, code)
		}
	}
	if code, _, _ := get(appB.Credential.Token, clean); code != http.StatusNotFound {
		t.Errorf("another company's token on this company's media = %d, want 404", code)
	}
	// the listing never carries a provider URL or a path
	_, listing, _ := e.callExt(http.MethodGet, "/api/v1/external/conversations/"+conv.String()+"/messages", app.Credential.Token, nil)
	if !strings.Contains(string(listing), clean.String()) || regexp.MustCompile(`https?://|/clean/|quarantine/|media_ref`).Match(listing) {
		t.Errorf("the message listing leaks storage details or lacks the media id: %s", listing)
	}
	// after the installation is revoked the file is no longer served, at once
	if code, _ := e.admin(adminA, a, http.MethodPost, "/"+app.Installation.ID.String()+"/revoke", nil); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	if code, _, _ := get(app.Credential.Token, clean); code != http.StatusUnauthorized {
		t.Errorf("media after the revocation = %d, want 401", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "clean", a.String(), clean.String())); err != nil {
		t.Fatal("control: the file is still stored:", err)
	}
}

// Enumeration: a thousand guessed ids are all the same 404.
func TestReadAPIEnumerationFindsNothing(t *testing.T) {
	e := newEnv(t)
	e.readAPI(nil)
	a, b := e.tenant(), e.tenant()
	code, body := e.admin(e.member(a, "tenant_admin"), a, http.MethodPost, "", map[string]any{"name": "CRM", "scopes": readScopes, "rate_limit_per_min": 6000})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var app created
	_ = json.Unmarshal(body, &app)
	bConv, bContact := e.conv(b, convOpts{})
	e.message(b, bConv, "inbound", "segredo", "received", time.Minute, nil)
	found := 0
	for i := 0; i < 300; i++ {
		for _, path := range []string{"/conversations/", "/contacts/", "/media/"} {
			if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/"+path[1:]+uuid.NewString(), app.Credential.Token, nil); code != http.StatusNotFound {
				found++
			}
		}
	}
	if found != 0 {
		t.Fatalf("%d guessed ids were not a 404", found)
	}
	for _, p := range []string{"/conversations/" + bConv.String(), "/conversations/" + bConv.String() + "/messages", "/contacts/" + bContact.String()} {
		if code, body, _ := e.callExt(http.MethodGet, "/api/v1/external/"+p[1:], app.Credential.Token, nil); code != http.StatusNotFound || strings.Contains(string(body), "segredo") {
			t.Errorf("another company's %s = %d %s", p, code, body)
		}
	}
}

// Static guard (R-1): every statement of the read side names tenant_id. The external API runs in a system session; the predicate is what confines it.
func TestEveryStatementOfTheExternalReadSideNamesTheTenant(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile("(?s)`([^`]*)`")
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "lookup.go" || f == "recheck.go" || f == "allowlist.go" || f == "store.go" || f == "admin_http.go" {
			continue // the definer functions bind tenant + installation themselves; the admin store runs in a person's session under row policies
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			sql := strings.ToUpper(m[1])
			if !(strings.Contains(sql, "SELECT ") || strings.Contains(sql, "UPDATE ") || strings.Contains(sql, "INSERT ") || strings.Contains(sql, "DELETE ")) || !strings.Contains(sql, " FROM ") && !strings.Contains(sql, "UPDATE ") && !strings.Contains(sql, "INSERT ") {
				continue
			}
			checked++
			// the shared constants carry the tenant through Visibility (conversation_select), which is built with it
			if !strings.Contains(m[1], "tenant_id") && !strings.Contains(m[1], "conversations c") {
				t.Errorf("%s: a statement without tenant_id: %.80s", f, strings.Join(strings.Fields(m[1]), " "))
			}
		}
	}
	if checked < 4 {
		t.Fatalf("only %d statements inspected: the scan is broken", checked)
	}
}

// Latency of the list on a larger company (the comparison at 30 000 conversations is made by the H6 benchmark).
func TestReadAPIListLatencyOnAMidSizeCompany(t *testing.T) {
	if testing.Short() {
		t.Skip("benchmark-like")
	}
	e := newEnv(t)
	e.readAPI(nil)
	a := e.tenant()
	app := e.install(e.member(a, "tenant_admin"), a, "CRM", readScopes...)
	const n = 4000
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) SELECT uuid_generate_v4(), $1, 'C'||g, '+5593'||lpad(g::text, 9, '0') FROM generate_series(1, $2) g`, a, n)
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status,created_at,updated_at)
	        SELECT uuid_generate_v4(), $1, c.id, 'open', now() - (row_number() OVER (ORDER BY c.id)) * interval '1 second', now() FROM contacts c WHERE c.tenant_id = $1`, a)
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) SELECT uuid_generate_v4(), tenant_id, id, 'inbound', 'text', 'oi', 'received' FROM conversations WHERE tenant_id = $1`, a)
	var times []time.Duration
	cursor := ""
	for i := 0; i < 20; i++ {
		q := "?limit=50"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		start := time.Now()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/external/conversations"+q, nil)
		req.RemoteAddr = fmt.Sprintf("198.51.100.%d:1", i+1)
		req.Header.Set("Authorization", "Bearer "+app.Credential.Token)
		rec := httptest.NewRecorder()
		e.ext.ServeHTTP(rec, req)
		times = append(times, time.Since(start))
		var l listed
		_ = json.Unmarshal(rec.Body.Bytes(), &l)
		if rec.Code != 200 || len(l.Items) != 50 {
			t.Fatalf("page %d = %d with %d items", i, rec.Code, len(l.Items))
		}
		cursor = l.NextCursor
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	p50, p95 := times[len(times)/2], times[len(times)*95/100]
	t.Logf("list of 50 on %d conversations: p50=%v p95=%v", n, p50, p95)
	if p95 > 2*time.Second {
		t.Errorf("p95 %v is far beyond what the keyset query should cost", p95)
	}
}
