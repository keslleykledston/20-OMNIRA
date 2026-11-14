package adapters

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	mediaadapters "github.com/omnira/omnira/internal/media/adapters"
	mediaports "github.com/omnira/omnira/internal/media/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// ADR-0015 addendum 2: group image storage + the SAME fetch/antivirus state machine 1:1 media already uses
// (internal/media/application.Processor's ports.Repository), run a second time against wa_group_message_media.

// A group message with a non-empty media_ref gets a pending wa_group_message_media row automatically
// (migration 000200's trigger) — tenant-scoped, never crossing into message_media.
func TestGroupMessageMediaRowIsEnqueuedByTheIngestionTriggerAndIsTenantScoped(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, connB := e.tenant()
	gidA := e.group(tenantA, connA, jid1, "Oficial", true)
	e.group(tenantB, connB, jid1, "Do B", true)

	intake := NewIntake(e.app, channeladapters.NewPostgresWebhookEventStore(e.app))
	m := groupMsg(connA, jid1, "img-1", "", time.Now().UTC())
	m.Media = &channeldomain.InboundMedia{Kind: channeldomain.MediaKindImage, MediaRef: "/api/files/abc.jpg", MimeType: "image/jpeg"}
	if _, _, err := intake.ProcessGroupMessage(e.ctx, connA, "kimg1", "message.any", "dimg1", m); err != nil {
		t.Fatal(err)
	}
	var gmid uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT id FROM wa_group_messages WHERE tenant_id=$1 AND group_id=$2 AND provider_message_id='img-1'`, tenantA, gidA).Scan(&gmid); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM wa_group_message_media WHERE tenant_id=$1 AND group_message_id=$2 AND status='pending'`, tenantA, gmid); n != 1 {
		t.Fatalf("pending media row = %d, want 1", n)
	}
	// Tenant B never gets a row from A's message.
	if n := e.count(`SELECT count(*) FROM wa_group_message_media WHERE tenant_id=$1`, tenantB); n != 0 {
		t.Fatalf("B got a media row it should never see: %d", n)
	}
	// A text-only message (no media_ref) never enqueues anything (the trigger's WHEN clause).
	m2 := groupMsg(connA, jid1, "txt-1", "oi", time.Now().UTC())
	if _, _, err := intake.ProcessGroupMessage(e.ctx, connA, "ktxt1", "message.any", "dtxt1", m2); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM wa_group_message_media WHERE tenant_id=$1`, tenantA); n != 1 {
		t.Fatalf("a text-only message must never get a media row: total = %d", n)
	}
}

// Claim/MarkQuarantined/MarkClean mirror the 1:1 state machine exactly, reading media_ref/mime_type/
// channel_connection_id from wa_group_messages (never messages), tenant-scoped.
func TestGroupMediaRepositoryFetchAntivirusLifecycleAndTenantIsolation(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, connB := e.tenant()
	gidA := e.group(tenantA, connA, jid1, "Oficial", true)
	gidB := e.group(tenantB, connB, jid1, "Do B", true)
	mA := e.groupMediaMessage(tenantA, gidA, "/api/files/a.jpg", "image/jpeg")
	mB := e.groupMediaMessage(tenantB, gidB, "/api/files/b.jpg", "image/jpeg")

	repo := NewGroupMediaRepository(e.app)
	claimed, err := repo.Claim(e.ctx, 10, 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed = %d, want 2", len(claimed))
	}
	byID := map[uuid.UUID]mediaports.Work{}
	for _, w := range claimed {
		byID[w.TenantID] = w
	}
	wa, ok := byID[tenantA]
	if !ok || wa.MessageID != mA || wa.MediaRef != "/api/files/a.jpg" || wa.DeclaredMime != "image/jpeg" || wa.ConnectionID != connA.ID {
		t.Fatalf("A's claimed work = %+v", wa)
	}

	// A second Claim immediately after must not reclaim (the lease), until MarkQuarantined/MarkClean advance it.
	if again, err := repo.Claim(e.ctx, 10, 90*time.Second); err != nil || len(again) != 0 {
		t.Fatalf("re-claim under lease: %d items, err=%v", len(again), err)
	}

	if err := repo.MarkQuarantined(e.ctx, wa, mediaports.Quarantined{Kind: "image", Mime: "image/jpeg", SizeBytes: 1234, SHA256: "deadbeef"}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM wa_group_message_media WHERE tenant_id=$1 AND status='quarantined' AND sha256='deadbeef'`, tenantA); n != 1 {
		t.Fatalf("quarantined row = %d", n)
	}
	if err := repo.MarkClean(e.ctx, wa); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM wa_group_message_media WHERE tenant_id=$1 AND status='clean'`, tenantA); n != 1 {
		t.Fatalf("clean row = %d", n)
	}

	// B's work, independently, goes to a terminal state and is audited under its OWN table name.
	wb, ok := byID[tenantB]
	if !ok || wb.MessageID != mB {
		t.Fatalf("B's work not claimed correctly: %+v", wb)
	}
	if err := repo.MarkTerminal(e.ctx, wb, mediaports.StatusInfected, "eicar"); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM wa_group_message_media WHERE tenant_id=$1 AND status='infected' AND reason='eicar'`, tenantB); n != 1 {
		t.Fatalf("B infected row = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_type='wa_group_message_media' AND action='group_media.infected'`, tenantB); n != 1 {
		t.Fatalf("B audit row = %d", n)
	}
	// A's media is never touched by B's terminal write.
	if n := e.count(`SELECT count(*) FROM wa_group_message_media WHERE tenant_id=$1 AND status='clean'`, tenantA); n != 1 {
		t.Fatalf("A's row changed by B's write: %d clean", n)
	}
}

// EnqueueVision only ever creates a job for a tenant that opted into BOTH the external AI itself AND its own
// "also read group images" decision (tenant_ai_integrations.enabled AND .group_media_enabled) — enabling 1:1
// vision alone must never silently also start reading a tenant's group images.
func TestGroupMediaRepositoryEnqueueVisionRespectsTheTenantAIOptInGate(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, connB := e.tenant() // enabled=true but group_media_enabled=false: 1:1 vision opted in, groups not
	gidA := e.group(tenantA, connA, jid1, "Oficial", true)
	gidB := e.group(tenantB, connB, jid1, "Do B", true)
	mA := e.groupMediaMessage(tenantA, gidA, "/api/files/a.jpg", "image/jpeg")
	mB := e.groupMediaMessage(tenantB, gidB, "/api/files/b.jpg", "image/jpeg")
	e.exec(`UPDATE wa_group_message_media SET status='clean', mime='image/jpeg' WHERE group_message_id IN ($1,$2)`, mA, mB)
	e.exec(`INSERT INTO tenant_ai_integrations(tenant_id, provider, enabled, secret_ciphertext, consent_at, group_media_enabled) VALUES($1,'gemini',true,'\x00','now'::timestamptz,true)`, tenantA)
	e.exec(`INSERT INTO tenant_ai_integrations(tenant_id, provider, enabled, secret_ciphertext, consent_at, group_media_enabled) VALUES($1,'gemini',true,'\x00','now'::timestamptz,false)`, tenantB)

	repo := NewGroupMediaRepository(e.app)
	n, err := repo.EnqueueVision(e.ctx, time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("enqueued = %d, want 1 (only the tenant with group_media_enabled)", n)
	}
	if got := e.count(`SELECT count(*) FROM wa_group_message_analysis WHERE tenant_id=$1 AND kind='description'`, tenantA); got != 1 {
		t.Fatalf("A's analysis row = %d", got)
	}
	if got := e.count(`SELECT count(*) FROM wa_group_message_analysis WHERE tenant_id=$1`, tenantB); got != 0 {
		t.Fatalf("B opted into 1:1 vision but never into group_media_enabled, yet got a row: %d", got)
	}
	// Idempotent: a second call creates nothing new.
	if n2, err := repo.EnqueueVision(e.ctx, time.Now().Add(-time.Hour), 10); err != nil || n2 != 0 {
		t.Fatalf("second enqueue: n=%d err=%v", n2, err)
	}
}

// ClaimAnalysis/SaveAnalysis roundtrip: the vision step's text lands on wa_group_message_analysis, never
// message_media_analysis.
func TestGroupMediaRepositoryClaimAndSaveAnalysisRoundtrip(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	gidA := e.group(tenantA, connA, jid1, "Oficial", true)
	mA := e.groupMediaMessage(tenantA, gidA, "/api/files/a.jpg", "image/jpeg")
	e.exec(`UPDATE wa_group_message_media SET status='clean', mime='image/jpeg' WHERE group_message_id=$1`, mA)
	e.exec(`INSERT INTO wa_group_message_analysis(tenant_id, group_message_id, kind, engine) VALUES($1,$2,'description','gemini')`, tenantA, mA)

	repo := NewGroupMediaRepository(e.app)
	claimed, err := repo.ClaimAnalysis(e.ctx, "description", 10, 3*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].MessageID != mA || claimed[0].Mime != "image/jpeg" {
		t.Fatalf("claimed = %+v", claimed)
	}
	if err := repo.SaveAnalysis(e.ctx, claimed[0], mediaports.Analysis{Text: "um roteador com a luz vermelha piscando", Language: "pt"}); err != nil {
		t.Fatal(err)
	}
	var body, status string
	if err := e.seed.QueryRow(e.ctx, `SELECT status, body FROM wa_group_message_analysis WHERE tenant_id=$1 AND group_message_id=$2`, tenantA, mA).Scan(&status, &body); err != nil {
		t.Fatal(err)
	}
	if status != "done" || body != "um roteador com a luz vermelha piscando" {
		t.Fatalf("status=%s body=%q", status, body)
	}
}

// GroupMediaReader.Open serves the clean file (runs under the CALLER's tenant session, so RLS decides what
// exists), reports pending/quarantined/terminal states without ever opening a file, and is tenant-isolated.
func TestGroupMediaReaderOpenReportsLifecycleAndIsTenantScoped(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, _ := e.tenant()
	adminA := e.member(tenantA, "tenant_admin", "active")
	gidA := e.group(tenantA, connA, jid1, "Oficial", true)
	mA := e.groupMediaMessage(tenantA, gidA, "/api/files/a.jpg", "image/jpeg")

	dir := t.TempDir()
	store, err := mediaadapters.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewGroupMediaReader(e.app, store)

	// pending: no file opened, status reported.
	if sess := sessionCall(t, e.app, tenantA, adminA, func(ctx context.Context) error {
		media, err := reader.Open(ctx, tenantA, mA)
		if err != nil {
			t.Fatal(err)
		}
		if media.Status != "pending" || media.File != nil {
			t.Fatalf("pending media = %+v", media)
		}
		return nil
	}); sess != nil {
		t.Fatal(sess)
	}

	// clean: the actual bytes come back.
	var mediaRowID uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT id FROM wa_group_message_media WHERE tenant_id=$1 AND group_message_id=$2`, tenantA, mA).Scan(&mediaRowID); err != nil {
		t.Fatal(err)
	}
	w := mediaports.Work{ID: mediaRowID, TenantID: tenantA}
	if err := store.PutQuarantine(w, []byte("fake-jpeg-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := store.Promote(w); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE wa_group_message_media SET status='clean', mime='image/jpeg', sha256='abc123' WHERE id=$1`, mediaRowID)
	if sess := sessionCall(t, e.app, tenantA, adminA, func(ctx context.Context) error {
		media, err := reader.Open(ctx, tenantA, mA)
		if err != nil {
			t.Fatal(err)
		}
		if media.Status != "clean" || media.File == nil {
			t.Fatalf("clean media = %+v", media)
		}
		defer media.File.Close()
		return nil
	}); sess != nil {
		t.Fatal(sess)
	}

	// Tenant B: an unknown message id (A's) behaves exactly like one that does not exist.
	adminB := e.member(tenantB, "tenant_admin", "active")
	if sess := sessionCall(t, e.app, tenantB, adminB, func(ctx context.Context) error {
		_, err := reader.Open(ctx, tenantB, mA)
		if err != mediaports.ErrMediaNotFound {
			t.Fatalf("B opening A's media: err=%v, want ErrMediaNotFound", err)
		}
		return nil
	}); sess != nil {
		t.Fatal(sess)
	}
}

// The HTTP handler maps the lifecycle to the right status codes and requires group.read (ADR-0015: granted by
// default to tenant_admin/tenant_supervisor, never tenant_agent).
func TestGroupMediaHandlerRequiresPermissionAndMapsLifecycleStatus(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	admin := e.member(tenantA, "tenant_admin", "active")
	agent := e.member(tenantA, "tenant_agent", "active") // no group.read
	gidA := e.group(tenantA, connA, jid1, "Oficial", true)
	mA := e.groupMediaMessage(tenantA, gidA, "/api/files/a.jpg", "image/jpeg")

	dir := t.TempDir()
	store, err := mediaadapters.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{}).WithMediaReader(NewGroupMediaReader(e.app, store))

	if rec := call(t, e.app, tenantA, agent, http.MethodGet, "/", "", map[string]string{"group_id": gidA.String(), "message_id": mA.String()}, h.Media); rec.Code != http.StatusForbidden {
		t.Fatalf("agent without group.read: %d, want 403", rec.Code)
	}

	rec := call(t, e.app, tenantA, admin, http.MethodGet, "/", "", map[string]string{"group_id": gidA.String(), "message_id": mA.String()}, h.Media)
	if rec.Code != http.StatusConflict { // still pending
		t.Fatalf("pending media: %d, want 409", rec.Code)
	}
	if rec.Header().Get("X-Media-Status") != "pending" {
		t.Fatalf("X-Media-Status = %q", rec.Header().Get("X-Media-Status"))
	}

	e.exec(`UPDATE wa_group_message_media SET status='infected', reason='eicar' WHERE group_message_id=$1`, mA)
	rec = call(t, e.app, tenantA, admin, http.MethodGet, "/", "", map[string]string{"group_id": gidA.String(), "message_id": mA.String()}, h.Media)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("infected media: %d, want 403", rec.Code)
	}

	unknown := uuid.New()
	rec = call(t, e.app, tenantA, admin, http.MethodGet, "/", "", map[string]string{"group_id": gidA.String(), "message_id": unknown.String()}, h.Media)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown message: %d, want 404", rec.Code)
	}
}

// groupMediaMessage inserts an enabled group's message with a media_ref/mime_type set directly (the trigger
// creates the pending wa_group_message_media row), returning the message id.
func (e *env) groupMediaMessage(tenant, group uuid.UUID, mediaRef, mime string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO wa_group_messages(id,tenant_id,group_id,provider_message_id,author_name,message_type,body,sent_at,media_ref,mime_type)
	        VALUES($1,$2,$3,$4,'Rafael','image','',now(),$5,$6)`, id, tenant, group, id.String(), mediaRef, mime)
	return id
}

// sessionCall runs fn inside a real member session (not system) — the shape GroupMediaReader.Open expects to
// run under (RLS decides which rows exist by the session user's OWN membership, same as the 1:1 reader).
// tenant is unused by the session itself (RLS keys off current_user_id() against each row's own tenant_id);
// it is kept as a parameter only so call sites read naturally.
func sessionCall(t *testing.T, pool *pgxpool.Pool, tenant, user uuid.UUID, fn func(ctx context.Context) error) error {
	t.Helper()
	return platformdb.WithTenantSession(context.Background(), pool, user, false, fn)
}
