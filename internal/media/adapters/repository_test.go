package adapters

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/media/application"
	"github.com/omnira/omnira/internal/media/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/testhelpers"
)

// Real Postgres, runtime role under FORCE RLS. Tenant A and Tenant B each receive media; neither may see or
// change the other's rows, and no operator session may write a verdict.

type menv struct {
	t    *testing.T
	ctx  context.Context
	seed *pgxpool.Pool
	app  *pgxpool.Pool
}

func newMEnv(t *testing.T) *menv {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(seed.Close)
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return &menv{t: t, ctx: ctx, seed: seed, app: app}
}

func (e *menv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *menv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.seed.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (e *menv) tenant() (tenant, conversation uuid.UUID) {
	tenant, conversation = uuid.New(), uuid.New()
	contact := uuid.New()
	e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenant, tenant.String())
	e.t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.seed.Exec(bg, `DELETE FROM audit_events WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM messages WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM conversations WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM contacts WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM tenants WHERE id=$1`, tenant)
	})
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164,status) VALUES($1,$2,'C',$3,'active')`, contact, tenant, fmt.Sprintf("+55119%08d", rand.Intn(100000000)))
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, conversation, tenant, contact)
	return tenant, conversation
}

// message inserts a message the way the webhook does; returns its id.
func (e *menv) message(tenant, conversation uuid.UUID, direction, mediaRef string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,media_ref,mime_type,status)
	        VALUES($1,$2,$3,$4,'image','',$5,'image/jpeg','received')`, id, tenant, conversation, direction, mediaRef)
	return id
}

func (e *menv) member(tenant uuid.UUID) uuid.UUID {
	u := uuid.New()
	e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	var roleID uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`).Scan(&roleID); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenant, u, roleID)
	return u
}

func (e *menv) status(id uuid.UUID) string {
	var s string
	if err := e.seed.QueryRow(e.ctx, `SELECT status FROM message_media WHERE message_id=$1`, id).Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

func TestInboundMediaMessageGetsAPendingRowInTheSameTransactionAndOnlyThose(t *testing.T) {
	e := newMEnv(t)
	tenant, conv := e.tenant()
	withMedia := e.message(tenant, conv, "inbound", "http://localhost:3000/api/files/s/a")
	noMedia := e.message(tenant, conv, "inbound", "")
	outbound := e.message(tenant, conv, "outbound", "http://localhost:3000/api/files/s/b")
	if e.status(withMedia) != "pending" {
		t.Fatalf("inbound media must start pending, got %s", e.status(withMedia))
	}
	for name, id := range map[string]uuid.UUID{"text message": noMedia, "outbound message": outbound} {
		if e.count(`SELECT count(*) FROM message_media WHERE message_id=$1`, id) != 0 {
			t.Fatalf("%s must not get a media row", name)
		}
	}
}

func TestTenantsCannotSeeEachOthersMediaAndOperatorsCannotWriteVerdicts(t *testing.T) {
	e := newMEnv(t)
	a, convA := e.tenant()
	b, convB := e.tenant()
	msgA := e.message(a, convA, "inbound", "http://localhost:3000/api/files/s/a")
	msgB := e.message(b, convB, "inbound", "http://localhost:3000/api/files/s/b")
	userA := e.member(a)

	visible := func(user uuid.UUID, tenant uuid.UUID) (n int) {
		_ = platformdb.WithTenantSession(e.ctx, e.app, user, false, func(ctx context.Context) error {
			return platformdb.QuerierFromContext(ctx, e.app).QueryRow(ctx, `SELECT count(*) FROM message_media`).Scan(&n)
		})
		return n
	}
	if n := visible(userA, a); n != 1 {
		t.Fatalf("tenant A member sees %d media rows, want exactly their own 1", n)
	}
	// Cross-tenant read by id, and cross-tenant write, both denied.
	var crossRead int
	_ = platformdb.WithTenantSession(e.ctx, e.app, userA, false, func(ctx context.Context) error {
		return platformdb.QuerierFromContext(ctx, e.app).QueryRow(ctx, `SELECT count(*) FROM message_media WHERE message_id=$1`, msgB).Scan(&crossRead)
	})
	if crossRead != 0 {
		t.Fatal("tenant A read tenant B's media row")
	}
	// An operator tries to mark an infected-in-waiting file clean, and to delete rows: both must affect nothing.
	var updated, deleted int64
	_ = platformdb.WithTenantSession(e.ctx, e.app, userA, false, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, e.app)
		tag, _ := q.Exec(ctx, `UPDATE message_media SET status='clean' WHERE message_id=$1`, msgA)
		updated = tag.RowsAffected()
		tag, _ = q.Exec(ctx, `DELETE FROM message_media WHERE message_id=$1`, msgA)
		deleted = tag.RowsAffected()
		return nil
	})
	if updated != 0 || deleted != 0 {
		t.Fatalf("operator session changed %d / deleted %d rows; verdicts are the worker's alone", updated, deleted)
	}
	if e.status(msgA) != "pending" || e.status(msgB) != "pending" {
		t.Fatal("rows must be untouched")
	}
}

func TestClaimLeasesRowsSoTwoWorkersNeverTakeTheSameOne(t *testing.T) {
	e := newMEnv(t)
	tenant, conv := e.tenant()
	for i := 0; i < 6; i++ {
		e.message(tenant, conv, "inbound", "http://localhost:3000/api/files/s/x")
	}
	repo := NewPostgresRepository(e.app)
	var mu sync.Mutex
	seen := map[uuid.UUID]int{}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := repo.Claim(e.ctx, 3, time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			for _, it := range items {
				seen[it.ID]++
				if it.MediaRef == "" || it.Attempts != 1 {
					t.Errorf("claimed item incomplete: %+v", it)
				}
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("row %s claimed %d times", id, n)
		}
	}
	// Leased rows are not handed out again until the lease ends.
	again, _ := repo.Claim(e.ctx, 10, time.Minute)
	for _, it := range again {
		if seen[it.ID] > 0 {
			t.Fatalf("leased row %s was handed out again", it.ID)
		}
	}
}

func TestVerdictTransitionsAreGuardedAndAudited(t *testing.T) {
	e := newMEnv(t)
	tenant, conv := e.tenant()
	msg := e.message(tenant, conv, "inbound", "http://localhost:3000/api/files/s/x")
	repo := NewPostgresRepository(e.app)
	items, _ := repo.Claim(e.ctx, 5, time.Minute)
	var w ports.Work
	for _, it := range items {
		if it.MessageID == msg {
			w = it
		}
	}
	if w.ID == uuid.Nil {
		t.Fatal("row not claimed")
	}
	// A file cannot be declared clean without first being quarantined.
	if err := repo.MarkClean(e.ctx, w); err == nil {
		t.Fatal("pending -> clean must be refused")
	}
	if err := repo.MarkQuarantined(e.ctx, w, ports.Quarantined{Kind: "image", Mime: "image/png", SizeBytes: 10, SHA256: "ab"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkTerminal(e.ctx, w, ports.StatusInfected, "Win.Test.EICAR_HDB-1"); err != nil {
		t.Fatal(err)
	}
	if e.status(msg) != "infected" {
		t.Fatalf("status = %s", e.status(msg))
	}
	// A final verdict cannot be overwritten, in either direction.
	if err := repo.MarkClean(e.ctx, w); err == nil {
		t.Fatal("infected -> clean must be refused")
	}
	if err := repo.MarkTerminal(e.ctx, w, ports.StatusFailed, "x"); err == nil {
		t.Fatal("infected -> failed must be refused")
	}
	var action, outcome, reason string
	if err := e.seed.QueryRow(e.ctx, `SELECT action, outcome, metadata->>'reason' FROM audit_events WHERE tenant_id=$1 AND resource_type='message_media'`, tenant).Scan(&action, &outcome, &reason); err != nil {
		t.Fatalf("audit event missing: %v", err)
	}
	if action != "media.infected" || outcome != "failure" || reason != "Win.Test.EICAR_HDB-1" {
		t.Fatalf("audit = %s %s %s", action, outcome, reason)
	}
}

func TestEndToEndWithRealDatabaseFilesAndAntivirusProtocol(t *testing.T) {
	e := newMEnv(t)
	tenant, conv := e.tenant()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x00\x00\x00\x00:~\x9bU\x00\x00\x00\nIDATx\x9cc`\x00\x00\x00\x02\x00\x01H\xaf\xa4q\x00\x00\x00\x00IEND\xaeB`\x82")
	eicar := []byte(`X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`)
	zip := append([]byte("PK\x03\x04"), make([]byte, 40)...)

	good := e.message(tenant, conv, "inbound", "http://localhost:3000/api/files/s/good")
	bad := e.message(tenant, conv, "inbound", "http://localhost:3000/api/files/s/eicar")
	arch := e.message(tenant, conv, "inbound", "http://localhost:3000/api/files/s/zip")
	gone := e.message(tenant, conv, "inbound", "http://localhost:3000/api/files/s/gone")
	e.exec(`UPDATE message_media SET created_at = now() - interval '1 hour' WHERE tenant_id=$1`, tenant)

	files := map[string][]byte{"/api/files/s/good": png, "/api/files/s/eicar": eicar, "/api/files/s/zip": zip}
	declared := map[string]string{"/api/files/s/good": "image/png", "/api/files/s/eicar": "text/plain", "/api/files/s/zip": "image/png"}
	fetch := fetcherFunc(func(_ context.Context, ref string) ([]byte, string, error) {
		for path, data := range files {
			if len(ref) >= len(path) && ref[len(ref)-len(path):] == path {
				return data, declared[path], nil
			}
		}
		return nil, "", ports.ErrSourceGone
	})
	// A clamd that really inspects the bytes: EICAR is FOUND, everything else OK.
	scanner := scannerFunc(func(_ context.Context, data []byte) (ports.Verdict, error) {
		if string(data) == string(eicar) {
			return ports.Verdict{Infected: true, Signature: "Win.Test.EICAR_HDB-1"}, nil
		}
		return ports.Verdict{}, nil
	})
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := application.NewProcessor(NewPostgresRepository(e.app), store, fetch, scanner, application.DefaultConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := p.ProcessOnce(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]uuid.UUID{"clean": good, "infected": bad, "rejected": arch, "source_gone": gone}
	for status, id := range want {
		if got := e.status(id); got != status {
			t.Errorf("message %s: status = %s, want %s", status, got, status)
		}
	}
	// Only the clean file is on disk in the served area.
	var cleanID uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT id FROM message_media WHERE message_id=$1`, good).Scan(&cleanID)
	f, err := store.OpenClean(tenant.String(), cleanID.String())
	if err != nil {
		t.Fatalf("clean file not served: %v", err)
	}
	f.Close()
	for _, id := range []uuid.UUID{bad, arch} {
		var mid uuid.UUID
		_ = e.seed.QueryRow(e.ctx, `SELECT id FROM message_media WHERE message_id=$1`, id).Scan(&mid)
		if f, err := store.OpenClean(tenant.String(), mid.String()); err == nil {
			f.Close()
			t.Fatalf("a %s file ended up in the served area", id)
		}
	}
	var sha string
	_ = e.seed.QueryRow(e.ctx, `SELECT sha256 FROM message_media WHERE message_id=$1`, good).Scan(&sha)
	if len(sha) != 64 {
		t.Fatalf("sha256 not recorded: %q", sha)
	}
}

type fetcherFunc func(context.Context, string) ([]byte, string, error)

func (f fetcherFunc) Fetch(ctx context.Context, ref string) ([]byte, string, error) {
	return f(ctx, ref)
}

type scannerFunc func(context.Context, []byte) (ports.Verdict, error)

func (f scannerFunc) Scan(ctx context.Context, b []byte) (ports.Verdict, error) { return f(ctx, b) }

// ---- derived text (ADR-0016 M2) ----

// audioCleared walks one inbound media row through the real repository to "clean" with the given kind.
func (e *menv) cleared(tenant, conv uuid.UUID, kind, mime string) (msg uuid.UUID, w ports.Work) {
	e.t.Helper()
	msg = e.message(tenant, conv, "inbound", "http://localhost:3000/api/files/s/"+uuid.NewString())
	repo := NewPostgresRepository(e.app)
	items, err := repo.Claim(e.ctx, 50, time.Minute)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, it := range items {
		if it.MessageID == msg {
			w = it
		}
	}
	if w.ID == uuid.Nil {
		e.t.Fatal("row not claimed")
	}
	if err := repo.MarkQuarantined(e.ctx, w, ports.Quarantined{Kind: kind, Mime: mime, SizeBytes: 10, SHA256: "ab"}); err != nil {
		e.t.Fatal(err)
	}
	if err := repo.MarkClean(e.ctx, w); err != nil {
		e.t.Fatal(err)
	}
	return msg, w
}

func (e *menv) analysisCount(msg uuid.UUID) int {
	return e.count(`SELECT count(*) FROM message_media_analysis WHERE message_id=$1`, msg)
}

func TestOnlyClearedAudioGetsATranscriptJobAndNeverTwice(t *testing.T) {
	e := newMEnv(t)
	tenant, conv := e.tenant()
	audioMsg, audio := e.cleared(tenant, conv, "audio", "audio/ogg")
	imageMsg, _ := e.cleared(tenant, conv, "image", "image/png")
	if e.analysisCount(audioMsg) != 1 {
		t.Fatalf("cleared audio must get exactly one transcript job, got %d", e.analysisCount(audioMsg))
	}
	if e.analysisCount(imageMsg) != 0 {
		t.Fatal("images are not transcribed")
	}
	// An infected audio never reaches the engine.
	infMsg := e.message(tenant, conv, "inbound", "http://localhost:3000/api/files/s/inf")
	repo := NewPostgresRepository(e.app)
	items, _ := repo.Claim(e.ctx, 50, time.Minute)
	for _, it := range items {
		if it.MessageID == infMsg {
			_ = repo.MarkQuarantined(e.ctx, it, ports.Quarantined{Kind: "audio", Mime: "audio/ogg", SizeBytes: 1, SHA256: "x"})
			_ = repo.MarkTerminal(e.ctx, it, ports.StatusInfected, "Eicar")
		}
	}
	if e.analysisCount(infMsg) != 0 {
		t.Fatal("an infected audio must never be queued for transcription")
	}
	_ = audio
}

func TestTranscriptLifecycleAndTheTextOutlivesTheFile(t *testing.T) {
	e := newMEnv(t)
	tenant, conv := e.tenant()
	msg, media := e.cleared(tenant, conv, "audio", "audio/ogg")
	repo := NewPostgresRepository(e.app)

	jobs, err := repo.ClaimAnalysis(e.ctx, "transcript", 5, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var job ports.AnalysisWork
	for _, j := range jobs {
		if j.MessageID == msg {
			job = j
		}
	}
	if job.ID == uuid.Nil || job.MediaID != media.ID || job.Mime != "audio/ogg" || job.Attempts != 1 {
		t.Fatalf("claimed = %+v", job)
	}
	// Leased: not handed out again.
	again, _ := repo.ClaimAnalysis(e.ctx, "transcript", 5, time.Minute)
	for _, j := range again {
		if j.ID == job.ID {
			t.Fatal("a leased job was handed out again")
		}
	}
	if err := repo.SaveAnalysis(e.ctx, job, ports.Analysis{Text: "Preciso trocar o roteador amanhã", Language: "pt", Model: "turbo", Suspicious: true, Reason: "truncated_at_10min"}); err != nil {
		t.Fatal(err)
	}
	var status, body, lang string
	var susp bool
	if err := e.seed.QueryRow(e.ctx, `SELECT status, body, language, suspicious FROM message_media_analysis WHERE message_id=$1`, msg).Scan(&status, &body, &lang, &susp); err != nil {
		t.Fatal(err)
	}
	if status != "done" || body != "Preciso trocar o roteador amanhã" || lang != "pt" || !susp {
		t.Fatalf("stored = %s %q %s %v", status, body, lang, susp)
	}
	// A result cannot be written twice (no silent overwrite of an existing transcript).
	if err := repo.SaveAnalysis(e.ctx, job, ports.Analysis{Text: "outra coisa"}); err == nil {
		t.Fatal("saving over a finished transcript must be refused")
	}
	// Retention removes the FILE; the text stays on the conversation.
	if err := repo.MarkPurged(e.ctx, media); err != nil {
		t.Fatal(err)
	}
	if got := e.count(`SELECT count(*) FROM message_media_analysis WHERE message_id=$1 AND status='done' AND body<>''`, msg); got != 1 {
		t.Fatal("the transcript must survive the purge of the file")
	}
	// Portuguese full-text search finds it by a stem ("roteadores" -> roteador), the base of M5.
	if got := e.count(`SELECT count(*) FROM message_media_analysis WHERE tenant_id=$1 AND status='done' AND tsv @@ plainto_tsquery('portuguese','roteadores')`, tenant); got != 1 {
		t.Fatalf("full-text search found %d, want 1", got)
	}
	// Deleting the message removes its derived text.
	e.exec(`DELETE FROM messages WHERE id=$1`, msg)
	if e.analysisCount(msg) != 0 {
		t.Fatal("derived text must go with the message")
	}
}

func TestDerivedTextIsReadableByMembersOnlyOfTheirTenantAndWritableByNoOperator(t *testing.T) {
	e := newMEnv(t)
	a, convA := e.tenant()
	b, convB := e.tenant()
	msgA, _ := e.cleared(a, convA, "audio", "audio/ogg")
	_, _ = e.cleared(b, convB, "audio", "audio/ogg")
	userA := e.member(a)
	e.exec(`UPDATE message_media_analysis SET status='done', body='texto do tenant A' WHERE message_id=$1`, msgA)

	var seen, forged, deleted int64
	var mine int
	_ = platformdb.WithTenantSession(e.ctx, e.app, userA, false, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, e.app)
		_ = q.QueryRow(ctx, `SELECT count(*) FROM message_media_analysis`).Scan(&mine)
		seen = int64(mine)
		tag, _ := q.Exec(ctx, `UPDATE message_media_analysis SET body='planted by an operator' WHERE message_id=$1`, msgA)
		forged = tag.RowsAffected()
		tag, _ = q.Exec(ctx, `DELETE FROM message_media_analysis WHERE message_id=$1`, msgA)
		deleted = tag.RowsAffected()
		return nil
	})
	if seen != 1 {
		t.Fatalf("member sees %d analysis rows, want exactly the 1 of their own tenant", seen)
	}
	if forged != 0 || deleted != 0 {
		t.Fatalf("an operator session changed %d / deleted %d rows; derived text is written by the worker only", forged, deleted)
	}
	// And cannot insert one for a message of theirs either.
	var insertErr error
	_ = platformdb.WithTenantSession(e.ctx, e.app, userA, false, func(ctx context.Context) error {
		_, insertErr = platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, `INSERT INTO message_media_analysis(tenant_id,message_id,kind,status,body) VALUES($1,$2,'description','done','fake')`, a, msgA)
		return nil
	})
	if insertErr == nil {
		t.Fatal("an operator session must not be able to insert derived text")
	}
}
