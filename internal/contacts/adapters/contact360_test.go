package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// CONTACT.360-A tests. Real Postgres, runtime role omnira_app under RLS, a
// real tenant session per call (see callAsTenant). Tenant A is the caller;
// Tenant B owns the data that must never show up.

func seedMemberWithRole(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID, status, roleKey string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	userID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, external_subject, email, status) VALUES ($1,$2,$3,'active')`,
		userID, userID.String(), userID.String()+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var roleID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL LIMIT 1`, roleKey).Scan(&roleID); err != nil {
		t.Fatalf("seed role %s: %v", roleKey, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,$5)`,
		uuid.New(), tenantID, userID, roleID, status); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	return userID
}

func seedChannel(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID, channel string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO channel_connections (id, tenant_id, channel, provider, provider_kind, external_number_id, status)
		 VALUES ($1,$2,$3,'waha','unofficial',$4,'active')`,
		id, tenantID, channel, id.String()); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return id
}

func seedConversation(t *testing.T, pool *pgxpool.Pool, tenantID, contactID uuid.UUID, channelID *uuid.UUID, status string, updatedAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO conversations (id, tenant_id, contact_id, channel_connection_id, status, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$6)`,
		id, tenantID, contactID, channelID, status, updatedAt); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	return id
}

func seedMessage(t *testing.T, pool *pgxpool.Pool, tenantID, conversationID uuid.UUID, direction, messageType, body string, at time.Time) {
	t.Helper()
	status := "received"
	if direction == "outbound" {
		status = "sent"
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO messages (id, tenant_id, conversation_id, direction, message_type, body, status, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		uuid.New(), tenantID, conversationID, direction, messageType, body, status, at); err != nil {
		t.Fatalf("seed message: %v", err)
	}
}

func seedTicket(t *testing.T, pool *pgxpool.Pool, tenantID, conversationID uuid.UUID, status, subject string, updatedAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO tickets (id, tenant_id, conversation_id, status, priority, subject, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,'medium',$5,$6,$6)`,
		id, tenantID, conversationID, status, subject, updatedAt); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	return id
}

func callContact(t *testing.T, app *pgxpool.Pool, tenantID, userID uuid.UUID, target, contactID string, fn func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	return callAsTenant(t, app, tenantID, userID, target, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("contact_id", contactID)
		fn(w, r)
	})
}

func parseTime(t *testing.T, v any) time.Time {
	t.Helper()
	s, _ := v.(string)
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse time %v: %v", v, err)
	}
	return ts
}

func TestContactCarriesDerivedFacts(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA := seedTenant(t, seed, "A")
	userA := seedMember(t, seed, tenantA, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)

	busy := seedContact(t, seed, tenantA, "Ocupada", "+5511900001001", now)
	quiet := seedContact(t, seed, tenantA, "Quieta", "+5511900001002", now.Add(-time.Minute))

	wa, mail := seedChannel(t, seed, tenantA, "whatsapp"), seedChannel(t, seed, tenantA, "email")
	convWA := seedConversation(t, seed, tenantA, busy, &wa, "open", now.Add(-3*time.Hour))
	convMail := seedConversation(t, seed, tenantA, busy, &mail, "open", now.Add(-2*time.Hour))
	convOld := seedConversation(t, seed, tenantA, busy, nil, "closed", now.Add(-48*time.Hour))
	seedMessage(t, seed, tenantA, convOld, "inbound", "text", "antiga", now.Add(-48*time.Hour))
	seedMessage(t, seed, tenantA, convWA, "inbound", "text", "oi", now.Add(-3*time.Hour))
	latest := now.Add(-10 * time.Minute)
	seedMessage(t, seed, tenantA, convMail, "outbound", "text", "ultima", latest)

	h := NewContactsAPIHandler(app)

	check := func(label string, item map[string]any, wantLast *time.Time, wantChannels []string, wantOpen float64) {
		t.Helper()
		if wantLast == nil {
			if item["last_interaction_at"] != nil {
				t.Fatalf("%s: last_interaction_at = %v, want null", label, item["last_interaction_at"])
			}
		} else if got := parseTime(t, item["last_interaction_at"]); !got.Equal(*wantLast) {
			t.Fatalf("%s: last_interaction_at = %v, want %v", label, got, *wantLast)
		}
		raw, ok := item["channels"].([]any)
		if !ok {
			t.Fatalf("%s: channels must be a JSON array (never null), got %#v", label, item["channels"])
		}
		got := make([]string, len(raw))
		for i, c := range raw {
			got[i], _ = c.(string)
		}
		if strings.Join(got, ",") != strings.Join(wantChannels, ",") {
			t.Fatalf("%s: channels = %v, want %v", label, got, wantChannels)
		}
		if item["open_conversation_count"] != wantOpen {
			t.Fatalf("%s: open_conversation_count = %v, want %v", label, item["open_conversation_count"], wantOpen)
		}
	}

	rec := callAsTenant(t, app, tenantA, userA, "/api/v1/tenants/"+tenantA.String()+"/contacts", h.ListContacts)
	byName := map[string]map[string]any{}
	for _, it := range decodePage(t, rec) {
		byName[it["display_name"].(string)] = it
	}
	check("list/busy", byName["Ocupada"], &latest, []string{"email", "whatsapp"}, 2)
	check("list/quiet", byName["Quieta"], nil, []string{}, 0)

	rec = callContact(t, app, tenantA, userA, "/c", busy.String(), h.GetContact)
	var one map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("get busy = %d %v %s", rec.Code, err, rec.Body.String())
	}
	check("get/busy", one, &latest, []string{"email", "whatsapp"}, 2)
	rec = callContact(t, app, tenantA, userA, "/c", quiet.String(), h.GetContact)
	one = map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &one)
	check("get/quiet", one, nil, []string{}, 0)
}

// Another tenant's conversations and messages must never feed a contact's
// derived facts, even if a row of tenant B somehow pointed at tenant A's
// contact: every subquery is scoped by tenant on both sides.
func TestDerivedFactsNeverIncludeAnotherTenant(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA, tenantB := seedTenant(t, seed, "A"), seedTenant(t, seed, "B")
	userA := seedMember(t, seed, tenantA, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)

	contactA := seedContact(t, seed, tenantA, "Alice A", "+5511900002001", now)
	contactB := seedContact(t, seed, tenantB, "Bruno B", "+5511900002002", now)
	chB := seedChannel(t, seed, tenantB, "whatsapp")
	convB := seedConversation(t, seed, tenantB, contactB, &chB, "open", now)
	seedMessage(t, seed, tenantB, convB, "inbound", "text", "segredo de B", now)

	// Adversarial row: a tenant-B conversation that names tenant A's contact.
	if _, err := seed.Exec(context.Background(),
		`INSERT INTO conversations (id, tenant_id, contact_id, status) VALUES ($1,$2,$3,'open')`,
		uuid.New(), tenantB, contactA); err != nil {
		t.Logf("database already forbids a cross-tenant conversation->contact reference (%v); aggregate scoping not reachable by seed", err)
	}

	h := NewContactsAPIHandler(app)
	rec := callContact(t, app, tenantA, userA, "/c", contactA.String(), h.GetContact)
	var item map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("get = %d %v", rec.Code, err)
	}
	if item["last_interaction_at"] != nil || item["open_conversation_count"] != float64(0) {
		t.Fatalf("tenant A contact picked up tenant B data: %v", item)
	}
	if ch, _ := item["channels"].([]any); len(ch) != 0 {
		t.Fatalf("tenant A contact picked up tenant B channels: %v", ch)
	}
}

func TestContactConversationsFieldsOrderAndPreview(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA := seedTenant(t, seed, "A")
	userA := seedMember(t, seed, tenantA, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	contact := seedContact(t, seed, tenantA, "Cliente", "+5511900003001", now)
	ch := seedChannel(t, seed, tenantA, "whatsapp")

	older := seedConversation(t, seed, tenantA, contact, nil, "closed", now.Add(-5*time.Hour))
	newer := seedConversation(t, seed, tenantA, contact, &ch, "open", now.Add(-1*time.Hour))
	seedMessage(t, seed, tenantA, older, "inbound", "text", "primeira", now.Add(-5*time.Hour))
	long := strings.Repeat("é", 200)
	seedMessage(t, seed, tenantA, newer, "inbound", "text", "olá", now.Add(-90*time.Minute))
	seedMessage(t, seed, tenantA, newer, "outbound", "text", long, now.Add(-70*time.Minute))
	last := now.Add(-61 * time.Minute)
	seedMessage(t, seed, tenantA, newer, "inbound", "image", "", last)

	h := NewContactsAPIHandler(app)
	rec := callContact(t, app, tenantA, userA, "/c", contact.String(), h.ListContactConversations)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	items := decodePage(t, rec)
	if len(items) != 2 {
		t.Fatalf("want 2 conversations, got %d", len(items))
	}
	if items[0]["id"] != newer.String() || items[1]["id"] != older.String() {
		t.Fatalf("not ordered by updated_at desc: %v", items)
	}
	if items[0]["channel"] != "whatsapp" || items[0]["provider"] != "waha" {
		t.Fatalf("channel/provider = %v/%v", items[0]["channel"], items[0]["provider"])
	}
	if items[1]["channel"] != nil || items[1]["provider"] != nil {
		t.Fatalf("conversation without a channel must report null, got %v/%v", items[1]["channel"], items[1]["provider"])
	}
	if items[0]["message_count"] != float64(3) || items[1]["message_count"] != float64(1) {
		t.Fatalf("message_count = %v/%v", items[0]["message_count"], items[1]["message_count"])
	}
	lm, _ := items[0]["last_message"].(map[string]any)
	if lm["message_type"] != "image" || lm["direction"] != "inbound" || lm["body_preview"] != "" {
		t.Fatalf("last_message must be the newest one (empty-caption image), got %v", lm)
	}
	if !parseTime(t, lm["created_at"]).Equal(last) {
		t.Fatalf("last_message.created_at = %v, want %v", lm["created_at"], last)
	}
	if _, leaked := items[0]["tenant_id"]; leaked {
		t.Fatalf("tenant_id must not be exposed")
	}

	// Truncation: make the long outbound the newest message and re-read.
	seedMessage(t, seed, tenantA, newer, "outbound", "text", long, now)
	rec = callContact(t, app, tenantA, userA, "/c", contact.String(), h.ListContactConversations)
	lm, _ = decodePage(t, rec)[0]["last_message"].(map[string]any)
	preview, _ := lm["body_preview"].(string)
	if n := utf8.RuneCountInString(preview); n != bodyPreviewMaxRunes+1 || !strings.HasSuffix(preview, "…") {
		t.Fatalf("preview should be %d runes + ellipsis, got %d runes", bodyPreviewMaxRunes, n)
	}
}

func TestContactConversationsPaginationIsStable(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA := seedTenant(t, seed, "A")
	userA := seedMember(t, seed, tenantA, "active")
	shared := time.Now().UTC().Truncate(time.Microsecond)
	contact := seedContact(t, seed, tenantA, "Paginada", "+5511900004001", shared)
	for i := 0; i < 5; i++ {
		seedConversation(t, seed, tenantA, contact, nil, "closed", shared)
	}

	h := NewContactsAPIHandler(app)
	seen := map[string]bool{}
	target := "/c?limit=2"
	for page := 0; page < 5; page++ {
		rec := callContact(t, app, tenantA, userA, target, contact.String(), h.ListContactConversations)
		var body struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"next_cursor"`
			HasMore    bool             `json:"has_more"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("page %d = %d %v", page, rec.Code, err)
		}
		for _, it := range body.Items {
			id, _ := it["id"].(string)
			if seen[id] {
				t.Fatalf("conversation %s repeated across pages", id)
			}
			seen[id] = true
		}
		if !body.HasMore {
			break
		}
		target = "/c?limit=2&cursor=" + body.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("paging returned %d of 5 conversations", len(seen))
	}
}

func TestContactSubresourcesAreTenantScopedAndIndistinguishable(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA, tenantB := seedTenant(t, seed, "A"), seedTenant(t, seed, "B")
	adminA := seedMember(t, seed, tenantA, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)

	contactB := seedContact(t, seed, tenantB, "Bruno B", "+5511900005001", now)
	convB := seedConversation(t, seed, tenantB, contactB, nil, "open", now)
	seedMessage(t, seed, tenantB, convB, "inbound", "text", "segredo de B", now)
	seedTicket(t, seed, tenantB, convB, "open", "ticket de B", now)

	h := NewContactsAPIHandler(app)
	for name, fn := range map[string]func(http.ResponseWriter, *http.Request){
		"conversations": h.ListContactConversations,
		"tickets":       h.ListContactTickets,
	} {
		rec := callContact(t, app, tenantA, adminA, "/c", contactB.String(), fn)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s of another tenant's contact = %d %q, want 404", name, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "segredo") || strings.Contains(rec.Body.String(), "ticket de B") {
			t.Fatalf("%s leaked tenant B content: %s", name, rec.Body.String())
		}
		missing := uuid.New().String()
		if got := callContact(t, app, tenantA, adminA, "/c", missing, fn).Code; got != http.StatusNotFound {
			t.Fatalf("%s unknown id = %d, want 404 (same as foreign id)", name, got)
		}
		if got := callContact(t, app, tenantA, adminA, "/c", "not-a-uuid", fn).Code; got != http.StatusBadRequest {
			t.Fatalf("%s invalid id = %d, want 400", name, got)
		}
	}
}

func TestContactTicketsRequireTicketReadAndStayOnTheContact(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA, tenantB := seedTenant(t, seed, "A"), seedTenant(t, seed, "B")
	admin := seedMemberWithRole(t, seed, tenantA, "active", "tenant_admin")
	agent := seedMemberWithRole(t, seed, tenantA, "active", "tenant_agent")
	revoked := seedMemberWithRole(t, seed, tenantA, "revoked", "tenant_admin")
	now := time.Now().UTC().Truncate(time.Microsecond)

	mine := seedContact(t, seed, tenantA, "Minha", "+5511900006001", now)
	other := seedContact(t, seed, tenantA, "Outra", "+5511900006002", now)
	convOld := seedConversation(t, seed, tenantA, mine, nil, "closed", now.Add(-2*time.Hour))
	convNew := seedConversation(t, seed, tenantA, mine, nil, "open", now.Add(-1*time.Hour))
	convOther := seedConversation(t, seed, tenantA, other, nil, "open", now)
	old := seedTicket(t, seed, tenantA, convOld, "resolved", "antigo", now.Add(-2*time.Hour))
	cur := seedTicket(t, seed, tenantA, convNew, "open", "atual", now.Add(-1*time.Hour))
	seedTicket(t, seed, tenantA, convOther, "open", "de outra pessoa", now)
	contactB := seedContact(t, seed, tenantB, "Bruno B", "+5511900006003", now)
	convB := seedConversation(t, seed, tenantB, contactB, nil, "open", now)
	seedTicket(t, seed, tenantB, convB, "open", "de outro tenant", now)

	h := NewContactsAPIHandler(app)

	rec := callContact(t, app, tenantA, admin, "/t", mine.String(), h.ListContactTickets)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin = %d %s", rec.Code, rec.Body.String())
	}
	items := decodePage(t, rec)
	if len(items) != 2 || items[0]["id"] != cur.String() || items[1]["id"] != old.String() {
		t.Fatalf("want [atual, antigo] only, got %v", items)
	}
	for _, forbidden := range []string{"de outra pessoa", "de outro tenant"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("tickets of %q leaked into this contact", forbidden)
		}
	}
	if _, leaked := items[0]["tenant_id"]; leaked {
		t.Fatalf("tenant_id must not be exposed")
	}

	// ticket.read comes from the role matrix: tenant_agent lacks it. The answer
	// is the same 403 for an existing id and for one that does not exist.
	for _, id := range []string{mine.String(), uuid.New().String(), contactB.String()} {
		if got := callContact(t, app, tenantA, agent, "/t", id, h.ListContactTickets).Code; got != http.StatusForbidden {
			t.Fatalf("agent without ticket.read on %s = %d, want 403", id, got)
		}
	}
	if got := callContact(t, app, tenantA, revoked, "/t", mine.String(), h.ListContactTickets).Code; got != http.StatusForbidden {
		t.Fatalf("revoked membership on tickets = %d, want 403", got)
	}
	// An agent may still read the conversations (Inbox visibility)...
	if got := callContact(t, app, tenantA, agent, "/c", mine.String(), h.ListContactConversations).Code; got != http.StatusOK {
		t.Fatalf("agent conversations = %d, want 200", got)
	}
	// ...but a revoked member sees nothing: the contact itself is invisible.
	if got := callContact(t, app, tenantA, revoked, "/c", mine.String(), h.ListContactConversations).Code; got != http.StatusNotFound {
		t.Fatalf("revoked membership on conversations = %d, want 404", got)
	}
}

// Write side of the tenancy contract for the rows this read model uses: from a
// tenant-A session, tenant B's rows can be neither changed nor created.
func TestTenantASessionCannotWriteTenantBRows(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA, tenantB := seedTenant(t, seed, "A"), seedTenant(t, seed, "B")
	userA := seedMember(t, seed, tenantA, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	contactB := seedContact(t, seed, tenantB, "Bruno B", "+5511900007001", now)
	convB := seedConversation(t, seed, tenantB, contactB, nil, "open", now)

	err := platformdb.WithTenantSession(context.Background(), app, userA, false, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, app)
		tag, execErr := q.Exec(ctx, `UPDATE contacts SET display_name='hack' WHERE id=$1`, contactB)
		if execErr != nil {
			t.Fatalf("update contact: %v", execErr)
		}
		if tag.RowsAffected() != 0 {
			t.Fatalf("tenant A session updated %d tenant-B contact row(s)", tag.RowsAffected())
		}
		tag, execErr = q.Exec(ctx, `UPDATE conversations SET status='closed' WHERE id=$1`, convB)
		if execErr != nil {
			t.Fatalf("update conversation: %v", execErr)
		}
		if tag.RowsAffected() != 0 {
			t.Fatalf("tenant A session updated %d tenant-B conversation row(s)", tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	err = platformdb.WithTenantSession(context.Background(), app, userA, false, func(ctx context.Context) error {
		_, insErr := platformdb.QuerierFromContext(ctx, app).Exec(ctx,
			`INSERT INTO messages (id, tenant_id, conversation_id, direction, message_type, body, status)
			 VALUES ($1,$2,$3,'inbound','text','injetada','received')`, uuid.New(), tenantB, convB)
		if insErr == nil {
			t.Fatalf("tenant A session inserted a message into tenant B")
		}
		return insErr
	})
	if err == nil {
		t.Fatalf("expected the cross-tenant insert to be rejected")
	}
	var name string
	if scanErr := seed.QueryRow(context.Background(), `SELECT display_name FROM contacts WHERE id=$1`, contactB).Scan(&name); scanErr != nil || name != "Bruno B" {
		t.Fatalf("tenant B contact changed: %q %v", name, scanErr)
	}
}

// Placeholder tickets (the implicit empty one every conversation gets) are not
// shown in Contact 360; a ticket linked to the ERP is, even with no local subject.
func TestContactTicketsHidePlaceholders(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA := seedTenant(t, seed, "A")
	admin := seedMemberWithRole(t, seed, tenantA, "active", "tenant_admin")
	now := time.Now().UTC().Truncate(time.Microsecond)
	contact := seedContact(t, seed, tenantA, "Cliente", "+5511900008001", now)

	convPlaceholder := seedConversation(t, seed, tenantA, contact, nil, "closed", now.Add(-3*time.Hour))
	convReal := seedConversation(t, seed, tenantA, contact, nil, "closed", now.Add(-2*time.Hour))
	convLinked := seedConversation(t, seed, tenantA, contact, nil, "closed", now.Add(-1*time.Hour))
	seedTicket(t, seed, tenantA, convPlaceholder, "open", "", now.Add(-3*time.Hour))
	real := seedTicket(t, seed, tenantA, convReal, "open", "Problema com pagamento", now.Add(-2*time.Hour))
	linked := uuid.New()
	if _, err := seed.Exec(context.Background(),
		`INSERT INTO tickets (id, tenant_id, conversation_id, status, priority, subject, provider, external_ticket_id, created_at, updated_at)
		 VALUES ($1,$2,$3,'open','medium','','k3g','EXT-7',$4,$4)`, linked, tenantA, convLinked, now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("seed linked ticket: %v", err)
	}

	h := NewContactsAPIHandler(app)
	rec := callContact(t, app, tenantA, admin, "/t", contact.String(), h.ListContactTickets)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	items := decodePage(t, rec)
	if len(items) != 2 || items[0]["id"] != linked.String() || items[1]["id"] != real.String() {
		t.Fatalf("want [ERP-linked, real] and no placeholder, got %v", items)
	}
}
