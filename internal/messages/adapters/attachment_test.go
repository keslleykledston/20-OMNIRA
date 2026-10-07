package adapters_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	mediaadapters "github.com/omnira/omnira/internal/media/adapters"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/messages/ports"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	"github.com/omnira/omnira/internal/worker/delivery"
)

type fakeScanner struct {
	down  atomic.Bool
	calls atomic.Int32
}

func (f *fakeScanner) Scan(_ context.Context, data []byte) (ports.VirusVerdict, error) {
	f.calls.Add(1)
	if f.down.Load() {
		return ports.VirusVerdict{}, errors.New("clamd unreachable")
	}
	if bytes.Contains(data, []byte("EICAR-STANDARD-ANTIVIRUS-TEST-FILE")) {
		return ports.VirusVerdict{Infected: true, Signature: "Eicar-Test-Signature"}, nil
	}
	return ports.VirusVerdict{}, nil
}

type attEnv struct {
	*env
	mux     *http.ServeMux
	scanner *fakeScanner
	dir     string
	files   *mediaadapters.OutboundFiles
}

func newAttEnv(t *testing.T) *attEnv {
	e := newEnv(t)
	dir := t.TempDir()
	files, err := mediaadapters.NewOutboundFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	scanner := &fakeScanner{}
	store := messagesadapters.NewPostgresOutboundStore(e.app)
	sender := messagesapplication.NewSender(store, channeladapters.NewPostgresPermissionChecker(e.app))
	h := messagesadapters.NewSendHandler(sender).WithAttachments(messagesapplication.NewAttachments(sender, store, files, scanner), store)
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(e.app), tenancyadapters.NewPostgresTenantRepository(e.app))
	mw := tenancyadapters.AuthorizationMiddleware(e.app, authz)
	mux := http.NewServeMux()
	base := "/api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}"
	mux.Handle("POST "+base+"/messages", mw(http.HandlerFunc(h.Send)))
	mux.Handle("POST "+base+"/attachments", mw(http.HandlerFunc(h.Upload)))
	mux.Handle("DELETE "+base+"/attachments/{attachment_id}", mw(http.HandlerFunc(h.RemoveAttachment)))
	return &attEnv{env: e, mux: mux, scanner: scanner, dir: dir, files: files}
}

func pngFile(t *testing.T) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 6, 6))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegWithEXIF(t *testing.T) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	payload := []byte("Exif\x00\x00GPSLatitude=-23.55;Make=SecretPhone")
	seg := make([]byte, 2)
	binary.BigEndian.PutUint16(seg, uint16(len(payload)+2))
	out := append([]byte{0xff, 0xd8, 0xff, 0xe1}, seg...)
	out = append(out, payload...)
	return append(out, b.Bytes()[2:]...)
}

func (a *attEnv) upload(user, tenant, conv uuid.UUID, name, mime string, data []byte) res {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	if mime != "" {
		h.Set("Content-Type", mime)
	}
	part, _ := mw.CreatePart(h)
	_, _ = part.Write(data)
	_ = mw.Close()
	return a.rawUpload(user, tenant, conv, mw.FormDataContentType(), &body)
}

func (a *attEnv) rawUpload(user, tenant, conv uuid.UUID, contentType string, body *bytes.Buffer) res {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tenant.String()+"/inbox/conversations/"+conv.String()+"/attachments", body)
	req.Header.Set("Content-Type", contentType)
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req)
	r := res{code: rec.Code, body: rec.Body.String()}
	_ = jsonUnmarshal(rec.Body.Bytes(), &r.m)
	return r
}

func (a *attEnv) sendReq(user, tenant, conv uuid.UUID, key, body string) res {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tenant.String()+"/inbox/conversations/"+conv.String()+"/messages", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req)
	r := res{code: rec.Code, body: rec.Body.String(), replayed: rec.Header().Get("Idempotent-Replayed") == "true"}
	_ = jsonUnmarshal(rec.Body.Bytes(), &r.m)
	return r
}

func (a *attEnv) del(user, tenant, conv uuid.UUID, id string) int {
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tenants/"+tenant.String()+"/inbox/conversations/"+conv.String()+"/attachments/"+id, nil)
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req)
	return rec.Code
}

func (a *attEnv) fileOnDisk(tenant uuid.UUID, id string) ([]byte, error) {
	return os.ReadFile(filepath.Join(a.dir, "outbound", tenant.String(), id))
}

func TestUploadThenSendQueuesAMediaMessageThatTheWorkerCanLoad(t *testing.T) {
	a := newAttEnv(t)
	up := a.upload(a.agent1, a.tenantA, a.convA, "../../Foto do Equipamento.jpg", "image/jpeg", jpegWithEXIF(t))
	if up.code != 201 {
		t.Fatalf("upload: %d %s", up.code, up.body)
	}
	id := up.m["id"].(string)
	if up.m["kind"] != "image" || up.m["mime"] != "image/jpeg" || up.m["file_name"] != "Foto do Equipamento.jpg" {
		t.Fatalf("record: %v", up.m)
	}
	// the stored (and later sent) file has no EXIF/GPS
	onDisk, err := a.fileOnDisk(a.tenantA, id)
	if err != nil || bytes.Contains(onDisk, []byte("GPSLatitude")) || bytes.Contains(onDisk, []byte("SecretPhone")) {
		t.Fatalf("stored file keeps metadata or is missing: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(onDisk)); err != nil {
		t.Fatalf("stored file is not a valid JPEG: %v", err)
	}

	s := a.sendReq(a.agent1, a.tenantA, a.convA, "media-key-0001", `{"attachment_id":"`+id+`","text":"Segue a foto"}`)
	if s.code != 202 || s.m["body"] != "Segue a foto" {
		t.Fatalf("send: %d %s", s.code, s.body)
	}
	msgID := uuid.MustParse(s.m["id"].(string))
	var typ, mime string
	var size int64
	var jobs int
	_ = a.seed.QueryRow(context.Background(), `SELECT message_type, mime_type, size_bytes FROM messages WHERE id=$1`, msgID).Scan(&typ, &mime, &size)
	_ = a.seed.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='job.channel.send_text.v1'`, msgID.String()).Scan(&jobs)
	if typ != "image" || mime != "image/jpeg" || size != int64(len(onDisk)) || jobs != 1 {
		t.Fatalf("message row: type=%s mime=%s size=%d jobs=%d", typ, mime, size, jobs)
	}

	// replay: same key, same request -> 200 and the SAME message
	r := a.sendReq(a.agent1, a.tenantA, a.convA, "media-key-0001", `{"attachment_id":"`+id+`","text":"Segue a foto"}`)
	if r.code != 200 || !r.replayed || r.m["id"] != s.m["id"] {
		t.Fatalf("replay: %d %s", r.code, r.body)
	}
	// the same file cannot be sent twice under another key
	if again := a.sendReq(a.agent1, a.tenantA, a.convA, "media-key-0002", `{"attachment_id":"`+id+`"}`); again.code != 422 {
		t.Fatalf("second send of one upload: %d %s", again.code, again.body)
	}
	var msgs int
	_ = a.seed.QueryRow(context.Background(), `SELECT count(*) FROM messages WHERE tenant_id=$1 AND conversation_id=$2 AND message_type='image'`, a.tenantA, a.convA).Scan(&msgs)
	if msgs != 1 {
		t.Fatalf("media messages = %d, want 1", msgs)
	}

	// the delivery worker reads the file record from the persisted message
	store := delivery.NewPostgresOutboundStore(a.app)
	var job *delivery.OutboundJob
	if err := store.RunForMessage(context.Background(), msgID, func(ctx context.Context) error {
		var e error
		job, e = store.LockOutbound(ctx, msgID)
		return e
	}); err != nil || job == nil || job.Media == nil {
		t.Fatalf("LockOutbound: %v %+v", err, job)
	}
	if job.Media.TenantID != a.tenantA || job.Media.Kind != "image" || job.Media.Mime != "image/jpeg" || job.Media.Size != int64(len(onDisk)) || job.Text != "Segue a foto" || job.Media.FileName != "Foto do Equipamento.jpg" {
		t.Fatalf("media job: %+v text=%q", job.Media, job.Text)
	}
	if got, err := a.files.ReadVerified(job.Media.TenantID, job.Media.AttachmentID, job.Media.Size, job.Media.SHA256); err != nil || !bytes.Equal(got, onDisk) {
		t.Fatalf("verified read: %v", err)
	}
	if _, err := a.files.ReadVerified(job.Media.TenantID, job.Media.AttachmentID, job.Media.Size, strings.Repeat("0", 64)); err == nil {
		t.Fatal("a file that does not match its recorded hash must not be readable for delivery")
	}
}

func TestHostileAndUnsupportedUploadsAreRefusedBeforeAnythingIsStored(t *testing.T) {
	a := newAttEnv(t)
	zip := append([]byte("PK\x03\x04"), make([]byte, 64)...)
	exe := append([]byte("MZ"), make([]byte, 64)...)
	cases := []struct {
		name, mime string
		data       []byte
		code       int
	}{
		{"archive.zip", "application/zip", zip, 422},
		{"setup.exe", "application/octet-stream", exe, 422},
		{"page.html", "text/html", []byte("<html><script>alert(1)</script></html>"), 422},
		{"doc.pdf", "application/pdf", []byte("%PDF-1.4\n/JavaScript (x)"), 422},
		{"foto.jpg", "image/jpeg", pngFile(t), 422}, // a PNG that claims to be... audio? no: declared image but fine -> accepted below
		{"empty.pdf", "application/pdf", nil, 422},
		{"eicar.txt", "text/plain", []byte("X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"), 422},
	}
	cases[4].code = 201 // a PNG declared as image/jpeg is the same kind: accepted (and stored by its real type)
	for _, tc := range cases {
		r := a.upload(a.agent1, a.tenantA, a.convA, tc.name, tc.mime, tc.data)
		if r.code != tc.code {
			t.Errorf("%s: %d %s, want %d", tc.name, r.code, r.body, tc.code)
		}
	}
	// nothing but the accepted PNG exists
	var rows int
	_ = a.seed.QueryRow(context.Background(), `SELECT count(*) FROM message_outbound_media WHERE tenant_id=$1`, a.tenantA).Scan(&rows)
	entries, _ := os.ReadDir(filepath.Join(a.dir, "outbound", a.tenantA.String()))
	if rows != 1 || len(entries) != 1 {
		t.Fatalf("rows=%d files=%d, want 1 and 1", rows, len(entries))
	}
	// an image that lies about being audio
	if r := a.upload(a.agent1, a.tenantA, a.convA, "x.ogg", "audio/ogg", pngFile(t)); r.code != 422 {
		t.Errorf("declared mismatch: %d", r.code)
	}
	// over the ceiling
	big := append([]byte("%PDF-"), make([]byte, 16<<20+10)...)
	if r := a.upload(a.agent1, a.tenantA, a.convA, "big.pdf", "application/pdf", big); r.code != 413 {
		t.Errorf("oversized: %d %s", r.code, r.body)
	}
	// not multipart at all
	if r := a.rawUpload(a.agent1, a.tenantA, a.convA, "application/json", bytes.NewBufferString(`{"file":"x"}`)); r.code != 400 {
		t.Errorf("not multipart: %d", r.code)
	}
	// no "file" part
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("note", "hello")
	_ = mw.Close()
	if r := a.rawUpload(a.agent1, a.tenantA, a.convA, mw.FormDataContentType(), &body); r.code != 400 {
		t.Errorf("no file part: %d", r.code)
	}
}

func TestAntivirusDownFailsClosedAndLeavesNothingBehind(t *testing.T) {
	a := newAttEnv(t)
	a.scanner.down.Store(true)
	r := a.upload(a.agent1, a.tenantA, a.convA, "boleto.pdf", "application/pdf", []byte("%PDF-1.4\n%%EOF"))
	if r.code != 503 {
		t.Fatalf("scanner down: %d %s", r.code, r.body)
	}
	var rows int
	_ = a.seed.QueryRow(context.Background(), `SELECT count(*) FROM message_outbound_media WHERE tenant_id=$1`, a.tenantA).Scan(&rows)
	if _, err := os.Stat(filepath.Join(a.dir, "outbound", a.tenantA.String())); rows != 0 || err == nil {
		entries, _ := os.ReadDir(filepath.Join(a.dir, "outbound", a.tenantA.String()))
		if rows != 0 || len(entries) != 0 {
			t.Fatalf("rows=%d files=%d after a failed scan", rows, len(entries))
		}
	}
}

func TestWhoMayUploadAndWhoMaySendAnUpload(t *testing.T) {
	a := newAttEnv(t)
	pdf := []byte("%PDF-1.4\n%%EOF")
	// agent2 is not the assignee and cannot manage: no upload to someone else's conversation
	if r := a.upload(a.agent2, a.tenantA, a.convA, "a.pdf", "application/pdf", pdf); r.code != 403 {
		t.Errorf("other agent: %d", r.code)
	}
	// a viewer cannot claim, so cannot attach
	if r := a.upload(a.viewer, a.tenantA, a.convA, "a.pdf", "application/pdf", pdf); r.code != 403 {
		t.Errorf("viewer: %d", r.code)
	}
	// a revoked membership never reaches the handler
	if r := a.upload(a.revoked, a.tenantA, a.convA, "a.pdf", "application/pdf", pdf); r.code != 404 && r.code != 403 {
		t.Errorf("revoked: %d", r.code)
	}
	// unassigned conversation, inactive channel, no channel
	for name, conv := range map[string]uuid.UUID{"unassigned": a.convUnassigned, "inactive channel": a.convInactive, "no channel": a.convNoConn} {
		if r := a.upload(a.agent1, a.tenantA, conv, "a.pdf", "application/pdf", pdf); r.code != 409 {
			t.Errorf("%s: %d %s", name, r.code, r.body)
		}
	}
	// the supervisor (manage) can upload to agent1's conversation, but agent1's upload is agent1's alone
	mine := a.upload(a.agent1, a.tenantA, a.convA, "mine.pdf", "application/pdf", pdf)
	if mine.code != 201 {
		t.Fatalf("upload: %d %s", mine.code, mine.body)
	}
	if r := a.sendReq(a.supervisor, a.tenantA, a.convA, "sup-key-00001", `{"attachment_id":"`+mine.m["id"].(string)+`"}`); r.code != 422 {
		t.Errorf("a supervisor sending ANOTHER operator's upload: %d %s", r.code, r.body)
	}
	if r := a.del(a.agent2, a.tenantA, a.convA, mine.m["id"].(string)); r != 403 && r != 422 {
		t.Errorf("another operator removing it: %d", r)
	}
	// a malformed or foreign attachment id is the same 422 as an expired one
	if r := a.sendReq(a.agent1, a.tenantA, a.convA, "ghost-key-0001", `{"attachment_id":"`+uuid.NewString()+`"}`); r.code != 422 {
		t.Errorf("unknown id: %d", r.code)
	}
	if r := a.sendReq(a.agent1, a.tenantA, a.convA, "bad-key-000001", `{"attachment_id":"not-a-uuid"}`); r.code != 400 {
		t.Errorf("malformed id: %d", r.code)
	}
	// cross-tenant: an operator of tenant A who is also an agent in tenant B cannot use A's upload from B's conversation
	var role uuid.UUID
	_ = a.seed.QueryRow(context.Background(), `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`).Scan(&role)
	a.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, a.tenantB, a.agent1, role)
	a.exec(`UPDATE conversations SET assigned_to_user_id=$1 WHERE id=$2`, a.agent1, a.convB)
	if r := a.sendReq(a.agent1, a.tenantB, a.convB, "xt-key-000001", `{"attachment_id":"`+mine.m["id"].(string)+`"}`); r.code != 422 {
		t.Errorf("tenant A's upload used from tenant B: %d %s", r.code, r.body)
	}
	// and a conversation of ANOTHER tenant in the URL of this one is a 404
	if r := a.upload(a.agent1, a.tenantA, a.convB, "a.pdf", "application/pdf", pdf); r.code != 404 {
		t.Errorf("foreign conversation: %d", r.code)
	}
	// a closed conversation
	a.exec(`UPDATE conversations SET status='closed' WHERE id=$1`, a.convA)
	if r := a.upload(a.agent1, a.tenantA, a.convA, "a.pdf", "application/pdf", pdf); r.code != 409 {
		t.Errorf("closed conversation: %d", r.code)
	}
}

func TestRemovedUploadsCannotBeSentAndTheNumberOfPendingOnesIsBounded(t *testing.T) {
	a := newAttEnv(t)
	pdf := []byte("%PDF-1.4\n%%EOF")
	ids := []string{}
	for i := 0; i < messagesapplication.MaxPendingAttachments; i++ {
		r := a.upload(a.agent1, a.tenantA, a.convA, "f.pdf", "application/pdf", append(pdf, byte(i)))
		if r.code != 201 {
			t.Fatalf("upload %d: %d %s", i, r.code, r.body)
		}
		ids = append(ids, r.m["id"].(string))
	}
	if r := a.upload(a.agent1, a.tenantA, a.convA, "f.pdf", "application/pdf", pdf); r.code != 409 {
		t.Fatalf("the %dth pending upload: %d, want 409", messagesapplication.MaxPendingAttachments+1, r.code)
	}
	if code := a.del(a.agent1, a.tenantA, a.convA, ids[0]); code != 204 {
		t.Fatalf("remove: %d", code)
	}
	if r := a.sendReq(a.agent1, a.tenantA, a.convA, "gone-key-0001", `{"attachment_id":"`+ids[0]+`"}`); r.code != 422 {
		t.Fatalf("a removed upload was sent: %d %s", r.code, r.body)
	}
	if r := a.upload(a.agent1, a.tenantA, a.convA, "f.pdf", "application/pdf", pdf); r.code != 201 {
		t.Fatalf("room again after a removal: %d %s", r.code, r.body)
	}
	// the sweeper deletes the removed upload's file and row, and keeps the others
	sweeper := mediaadapters.NewOutboundSweeper(a.app, a.files, 60*24*time.Hour)
	deleted, purged, err := sweeper.Once(context.Background())
	if err != nil || deleted != 1 || purged != 0 {
		t.Fatalf("sweep: deleted=%d purged=%d err=%v", deleted, purged, err)
	}
	if _, err := a.fileOnDisk(a.tenantA, ids[0]); err == nil {
		t.Fatal("the removed upload's file is still on disk")
	}
	if _, err := a.fileOnDisk(a.tenantA, ids[1]); err != nil {
		t.Fatalf("a pending upload was swept: %v", err)
	}
}

func TestRetentionPurgesSentFilesButKeepsTheRecord(t *testing.T) {
	a := newAttEnv(t)
	up := a.upload(a.agent1, a.tenantA, a.convA, "boleto.pdf", "application/pdf", []byte("%PDF-1.4\n%%EOF"))
	id := up.m["id"].(string)
	if s := a.sendReq(a.agent1, a.tenantA, a.convA, "ret-key-000001", `{"attachment_id":"`+id+`"}`); s.code != 202 {
		t.Fatalf("send: %d %s", s.code, s.body)
	}
	sweeper := mediaadapters.NewOutboundSweeper(a.app, a.files, time.Hour)
	if d, p, err := sweeper.Once(context.Background()); err != nil || d != 0 || p != 0 {
		t.Fatalf("a fresh sent file must stay: deleted=%d purged=%d err=%v", d, p, err)
	}
	a.exec(`UPDATE message_outbound_media SET created_at = now() - interval '2 hours' WHERE id=$1`, id)
	if d, p, err := sweeper.Once(context.Background()); err != nil || d != 0 || p != 1 {
		t.Fatalf("retention: deleted=%d purged=%d err=%v", d, p, err)
	}
	var purged bool
	if err := a.seed.QueryRow(context.Background(), `SELECT file_purged_at IS NOT NULL FROM message_outbound_media WHERE id=$1`, id).Scan(&purged); err != nil || !purged {
		t.Fatalf("the record must stay, marked purged: %v %v", purged, err)
	}
	if _, err := a.fileOnDisk(a.tenantA, id); err == nil {
		t.Fatal("the file must be gone")
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
