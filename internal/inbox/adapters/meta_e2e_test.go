package adapters

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	"github.com/omnira/omnira/internal/channels/meta"
	inboxapp "github.com/omnira/omnira/internal/inbox/application"
)

func TestMetaWebhookEndToEndPersistsDedupesAndIsolates(t *testing.T) {
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
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

	tenantA, tenantB := uuid.New(), uuid.New()
	phoneA, phoneB := "e2e-a-"+uuid.NewString(), "e2e-b-"+uuid.NewString()
	for _, tn := range []uuid.UUID{tenantA, tenantB} {
		if _, err := seed.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Exec(ctx, `INSERT INTO queues(id,tenant_id,name,mode,is_default) VALUES($1,$2,'Default','round_robin',true)`, uuid.New(), tn); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1,$2)`, tenantA, tenantB)
	})
	for tn, phone := range map[uuid.UUID]string{tenantA: phoneA, tenantB: phoneB} {
		if _, err := seed.Exec(ctx, `INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status) VALUES($1,$2,'whatsapp','meta_cloud','official',$3,'active')`, uuid.New(), tn, phone); err != nil {
			t.Fatal(err)
		}
	}

	store := NewPostgresInboundStore(app)
	svc := inboxapp.NewInboundService(store, store, store, TicketStore{store}, store)
	events := channeladapters.NewPostgresWebhookEventStore(app)
	h := meta.Handler{
		AppSecret: "secret",
		Resolver:  channeladapters.NewMetaWebhookConnectionResolver(app, channeladapters.NewPostgresChannelConnectionRepository(app)),
		Intake:    NewWebhookIntake(app, events, svc),
	}
	post := func(body string) int {
		mac := hmac.New(sha256.New, []byte("secret"))
		mac.Write([]byte(body))
		req := httptest.NewRequest(http.MethodPost, "/webhooks/v1/whatsapp/meta", strings.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	// Payload carries a forged tenant_id; it must be ignored.
	body := func(phone, wamid string) string {
		return `{"tenant_id":"` + tenantB.String() + `","entry":[{"changes":[{"value":{"metadata":{"phone_number_id":"` + phone + `"},"messages":[{"from":"5511988887777","id":"` + wamid + `","timestamp":"1700000000","type":"text","text":{"body":"olá"}}]}}]}]}`
	}
	wamid := "wamid." + uuid.NewString()
	for i := 0; i < 2; i++ { // second is a Meta redelivery
		if code := post(body(phoneA, wamid)); code != 200 {
			t.Fatalf("delivery %d code=%d", i, code)
		}
	}
	count := func(tn uuid.UUID) (n int) {
		if err := seed.QueryRow(ctx, `SELECT count(*) FROM messages WHERE tenant_id=$1`, tn).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}
	if a, b := count(tenantA), count(tenantB); a != 1 || b != 0 {
		t.Fatalf("messages A=%d B=%d, want 1/0 (dedupe + tenant from connection only)", a, b)
	}
	var conv int
	if err := seed.QueryRow(ctx, `SELECT count(*) FROM conversations WHERE tenant_id=$1`, tenantA).Scan(&conv); err != nil || conv != 1 {
		t.Fatalf("conversations=%d err=%v", conv, err)
	}
	if code := post(body(phoneB, "wamid."+uuid.NewString())); code != 200 {
		t.Fatalf("B code=%d", code)
	}
	if a, b := count(tenantA), count(tenantB); a != 1 || b != 1 {
		t.Fatalf("after B: A=%d B=%d", a, b)
	}
	if code := post(body("unknown-"+uuid.NewString(), "wamid.z")); code != http.StatusNotFound {
		t.Fatalf("unknown phone code=%d", code)
	}
}
