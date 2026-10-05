package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

func clCall(t *testing.T, pool *pgxpool.Pool, tenant, user uuid.UUID, method, body string, path map[string]string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	err := platformdb.WithTenantSession(context.Background(), pool, user, false, func(sc context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(method, "/", bytes.NewReader([]byte(body))).WithContext(tenancydomain.WithTenantContext(sc, tc))
		for k, v := range path {
			req.SetPathValue(k, v)
		}
		fn(rec, req)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestOpenConversationOnAChosenLineAndTheMetaWindow(t *testing.T) {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx := context.Background()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	a, b := uuid.New(), uuid.New()
	for _, tn := range []uuid.UUID{a, b} {
		if _, err := seed.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1,$2)`, a, b) })
	member := func(tenant uuid.UUID, role string) uuid.UUID {
		u := uuid.New()
		var roleID uuid.UUID
		if _, err := seed.Exec(ctx, `INSERT INTO users (id, external_subject, email, status) VALUES ($1,$2,$3,'active')`, u, u.String(), u.String()+"@example.com"); err != nil {
			t.Fatal(err)
		}
		if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL LIMIT 1`, role).Scan(&roleID); err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Exec(ctx, `INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,'active')`, uuid.New(), tenant, u, roleID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
		return u
	}
	agent, otherAgent := member(a, "tenant_agent"), member(b, "tenant_agent")
	conn := func(tenant uuid.UUID, provider, kind, status string) uuid.UUID {
		id := uuid.New()
		if _, err := seed.Exec(ctx, `INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
			VALUES($1,$2,'whatsapp',$3,$4,$5,$6,'["text"]')`, id, tenant, provider, kind, "cl-"+id.String(), status); err != nil {
			t.Fatal(err)
		}
		return id
	}
	waha, meta := conn(a, "waha", "unofficial", "active"), conn(a, "meta_cloud", "official", "active")
	inactive, foreign := conn(a, "waha", "unofficial", "disconnected"), conn(b, "waha", "unofficial", "active")
	contact := uuid.New()
	for id, phone := range map[uuid.UUID]string{contact: "+5592966660099"} {
		if _, err := seed.Exec(ctx, `INSERT INTO contacts(id,tenant_id,display_name,phone_e164,kind) VALUES($1,$2,'Fulano',$3,'other')`, id, a, phone); err != nil {
			t.Fatal(err)
		}
	}
	h := NewChannelLinesHandler(app, channeladapters.NewPostgresPermissionChecker(app))
	open := func(tenant, user, c, ch uuid.UUID) (int, uuid.UUID) {
		rec := clCall(t, app, tenant, user, http.MethodPost, `{"contact_id":"`+c.String()+`","channel_connection_id":"`+ch.String()+`"}`, nil, h.Open)
		var out struct {
			ConversationID uuid.UUID `json:"conversation_id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.ConversationID
	}

	code, onWaha := open(a, agent, contact, waha)
	if code != http.StatusCreated || onWaha == uuid.Nil {
		t.Fatalf("first open = %d", code)
	}
	if code, again := open(a, agent, contact, waha); code != http.StatusOK || again != onWaha {
		t.Fatalf("second open must return the same conversation: %d %s vs %s", code, again, onWaha)
	}
	code, onMeta := open(a, agent, contact, meta)
	if code != http.StatusCreated || onMeta == onWaha {
		t.Fatalf("the same contact on another line is a separate conversation: %d", code)
	}
	for name, got := range map[string]int{
		"inactive line":     func() int { c, _ := open(a, agent, contact, inactive); return c }(),
		"other tenant line": func() int { c, _ := open(a, agent, contact, foreign); return c }(),
	} {
		if got != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d, want 422", name, got)
		}
	}
	if c, _ := open(b, otherAgent, contact, foreign); c != http.StatusNotFound {
		t.Errorf("another tenant's contact = %d, want 404", c)
	}

	channel := func(conv uuid.UUID) (int, conversationChannel) {
		rec := clCall(t, app, a, agent, http.MethodGet, "", map[string]string{"conversation_id": conv.String()}, h.Channel)
		var out conversationChannel
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if _, w := channel(onWaha); w.WindowRequired || !w.WindowOpen || !w.CanSendText {
		t.Fatalf("waha has no window: %+v", w)
	}
	if _, m := channel(onMeta); !m.WindowRequired || m.WindowOpen {
		t.Fatalf("meta with no inbound message must be closed: %+v", m)
	}
	insert := func(ago time.Duration) {
		if _, err := seed.Exec(ctx, `INSERT INTO messages(id,tenant_id,conversation_id,channel_connection_id,direction,message_type,body,status,created_at,updated_at)
			VALUES($1,$2,$3,$4,'inbound','text','oi','received',$5,$5)`, uuid.New(), a, onMeta, meta, time.Now().Add(-ago)); err != nil {
			t.Fatal(err)
		}
	}
	insert(2 * time.Hour)
	if _, m := channel(onMeta); !m.WindowOpen || m.WindowExpiresAt == nil {
		t.Fatalf("a message 2 h ago keeps the window open: %+v", m)
	}
	if _, err := seed.Exec(ctx, `DELETE FROM messages WHERE conversation_id=$1`, onMeta); err != nil {
		t.Fatal(err)
	}
	insert(26 * time.Hour)
	if _, m := channel(onMeta); m.WindowOpen {
		t.Fatalf("a message 26 h ago closes the window: %+v", m)
	}
	if code, _ := channel(uuid.New()); code != http.StatusNotFound {
		t.Fatalf("unknown conversation = %d", code)
	}
}
