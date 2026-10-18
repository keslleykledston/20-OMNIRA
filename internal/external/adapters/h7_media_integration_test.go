package adapters_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/external/adapters"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapp "github.com/omnira/omnira/internal/messages/application"
	messagesports "github.com/omnira/omnira/internal/messages/ports"
)

// ADR-0048 x ADR-0024 on a real PostgreSQL: an application uploads a file to a conversation it holds and sends it. The file goes through the operator's pipeline;
// the record belongs to the installation; the message is queued only while the installation still holds the conversation at the version it presents.

type memFiles struct {
	mu sync.Mutex
	m  map[uuid.UUID][]byte
}

func (f *memFiles) Put(_, id uuid.UUID, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[uuid.UUID][]byte{}
	}
	f.m[id] = data
	return nil
}
func (f *memFiles) Remove(_, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, id)
	return nil
}

// eicar is the standard antivirus test string; the fake scanner flags it the way ClamAV does.
type fakeScanner struct{}

func (fakeScanner) Scan(_ context.Context, data []byte) (messagesports.VirusVerdict, error) {
	if bytes.Contains(data, []byte("EICAR-STANDARD-ANTIVIRUS-TEST-FILE")) {
		return messagesports.VirusVerdict{Infected: true, Signature: "Eicar-Test-Signature"}, nil
	}
	return messagesports.VirusVerdict{}, nil
}

func tinyPNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := 0; x < 4; x++ {
		for y := 0; y < 4; y++ {
			img.Set(x, y, color.RGBA{R: shade, G: 80, B: 160, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (e *env) mediaAPI() {
	store := messagesadapters.NewPostgresOutboundStore(e.app)
	sender := messagesapp.NewSender(store, nil)
	att := messagesapp.NewAttachments(sender, store, &memFiles{}, fakeScanner{}).WithExternal(store, store)
	e.writeAPI(func(d *adapters.WriteDeps) { d.Media = att })
}

type uploadResp struct {
	Code int
	Raw  []byte
	Doc  struct {
		ID        uuid.UUID `json:"attachment_id"`
		Kind      string    `json:"kind"`
		Mime      string    `json:"mime"`
		SizeBytes int64     `json:"size_bytes"`
		Code      string    `json:"code"`
	}
}

func (e *env) upload(token string, conv uuid.UUID, name string, data []byte) uploadResp {
	e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", name)
	_, _ = part.Write(data)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/external/conversations/"+conv.String()+"/attachments", &body)
	e.seq++
	req.RemoteAddr = "198.51.100." + strconv.Itoa(e.seq%250+1) + ":" + strconv.Itoa(4000+e.seq/250)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ext.ServeHTTP(rec, req)
	r := uploadResp{Code: rec.Code, Raw: rec.Body.Bytes()}
	_ = json.Unmarshal(r.Raw, &r.Doc)
	return r
}

func TestAnApplicationUploadsAFileAndSendsItAsTheHolder(t *testing.T) {
	e := newEnv(t)
	e.mediaAPI()
	tn, _, app, conv, _ := e.writeEnv()
	tok := app.Credential.Token

	// not the holder yet: no file can be parked on the conversation
	if r := e.upload(tok, conv, "foto.png", tinyPNG(t, 10)); r.Code != 409 || r.Doc.Code != "not_holder" {
		t.Fatalf("upload before claiming = %d %s", r.Code, r.Raw)
	}
	v := e.cmd(tok, conv, "claim", "key-claim-0001", "", nil).Doc.Control.Version
	up := e.upload(tok, conv, "foto.png", tinyPNG(t, 10))
	if up.Code != 201 || up.Doc.Kind != "image" || up.Doc.Mime != "image/png" || up.Doc.ID == uuid.Nil {
		t.Fatalf("upload = %d %s", up.Code, up.Raw)
	}
	// the record belongs to the installation and to no person
	if n := e.count(`SELECT count(*) FROM message_outbound_media WHERE id=$1 AND uploaded_by IS NULL AND uploaded_by_installation_id IS NOT NULL AND message_id IS NULL`, up.Doc.ID); n != 1 {
		t.Fatalf("owner of the upload: %d rows", n)
	}
	// a send needs exactly one of text, template, or an attachment
	if r := e.cmd(tok, conv, "messages", "key-msg-00001", match(v), map[string]any{"attachment_id": up.Doc.ID, "template": map[string]any{"id": uuid.New()}}); r.Code != 422 {
		t.Errorf("attachment with a template = %d", r.Code)
	}
	// the version must be the current one
	if r := e.cmd(tok, conv, "messages", "key-msg-00002", match(v+5), map[string]any{"attachment_id": up.Doc.ID, "text": "Segue a foto"}); r.Code != 412 {
		t.Errorf("stale version = %d %s", r.Code, r.Raw)
	}
	if n := e.count(`SELECT count(*) FROM message_outbound_media WHERE id=$1 AND message_id IS NULL`, up.Doc.ID); n != 1 {
		t.Fatal("a refused send leaves the file unsent")
	}
	sent := e.cmd(tok, conv, "messages", "key-msg-00003", match(v), map[string]any{"attachment_id": up.Doc.ID, "text": "Segue a foto"})
	if sent.Code != 202 || sent.Doc.MessageID == nil || sent.Doc.Delivery != "queued" {
		t.Fatalf("send = %d %s", sent.Code, sent.Raw)
	}
	var typ, body string
	var inst, user *uuid.UUID
	var key *string
	if err := e.seed.QueryRow(e.ctx, `SELECT message_type, body, sent_by_installation_id, sent_by_user_id, idempotency_key FROM messages WHERE id=$1`, *sent.Doc.MessageID).Scan(&typ, &body, &inst, &user, &key); err != nil {
		t.Fatal(err)
	}
	if typ != "image" || body != "Segue a foto" || inst == nil || user != nil || key != nil {
		t.Fatalf("message: type=%s body=%q inst=%v user=%v key=%v", typ, body, inst, user, key)
	}
	if n := e.count(`SELECT count(*) FROM message_outbound_media WHERE id=$1 AND message_id=$2`, up.Doc.ID, *sent.Doc.MessageID); n != 1 {
		t.Fatal("the file is linked to its message")
	}
	if n := e.count(`SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND aggregate_id=$2 AND event_type='job.channel.send_text.v1'`, tn, sent.Doc.MessageID.String()); n != 1 {
		t.Fatal("the delivery job is queued once")
	}
	// the same command again answers the same, and queues nothing more
	again := e.cmd(tok, conv, "messages", "key-msg-00003", match(v), map[string]any{"attachment_id": up.Doc.ID, "text": "Segue a foto"})
	if again.Code != 202 || again.Header.Get("Idempotent-Replay") != "true" || *again.Doc.MessageID != *sent.Doc.MessageID {
		t.Fatalf("replay = %d %s", again.Code, again.Raw)
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, conv); n != 1 {
		t.Fatalf("outbound messages = %d", n)
	}
	// the file is spent: another send of it, under another key, is refused
	if r := e.cmd(tok, conv, "messages", "key-msg-00004", match(v), map[string]any{"attachment_id": up.Doc.ID}); r.Code != 422 || r.Doc.Code != "attachment_unavailable" {
		t.Errorf("sending a spent file = %d %s", r.Code, r.Raw)
	}
}

func TestAnApplicationFileIsNeverAnotherInstallationsOrCompanys(t *testing.T) {
	e := newEnv(t)
	e.mediaAPI()
	tn, admin, app, conv, _ := e.writeEnv()
	tok := app.Credential.Token
	v := e.cmd(tok, conv, "claim", "key-claim-0001", "", nil).Doc.Control.Version
	up := e.upload(tok, conv, "foto.png", tinyPNG(t, 20))
	if up.Code != 201 {
		t.Fatalf("upload = %d %s", up.Code, up.Raw)
	}
	// a second installation of the same company holds ANOTHER conversation and tries to send the first one's file
	other := e.install(admin, tn, "CRM 2", writeScopes...)
	conn2 := e.connection(tn)
	conv2, _ := e.conv(tn, convOpts{conn: &conn2})
	e.message(tn, conv2, "inbound", "oi", "received", 0, nil)
	v2 := e.cmd(other.Credential.Token, conv2, "claim", "key-claim-0002", "", nil).Doc.Control.Version
	if r := e.cmd(other.Credential.Token, conv2, "messages", "key-msg-00001", match(v2), map[string]any{"attachment_id": up.Doc.ID}); r.Code != 422 || r.Doc.Code != "attachment_unavailable" {
		t.Errorf("another installation sending the file = %d %s", r.Code, r.Raw)
	}
	// the owner is what matters, not only the conversation: the first installation lets go, the second takes the SAME conversation and cannot send the first one's file
	e.cmd(tok, conv, "release", "key-rel-00001", match(v), nil)
	claimed := e.cmd(other.Credential.Token, conv, "claim", "key-claim-0009", "", nil)
	if claimed.Code != 200 {
		t.Fatalf("second installation claims the same conversation = %d %s", claimed.Code, claimed.Raw)
	}
	if r := e.cmd(other.Credential.Token, conv, "messages", "key-msg-00009", match(claimed.Doc.Control.Version), map[string]any{"attachment_id": up.Doc.ID}); r.Code != 422 || r.Doc.Code != "attachment_unavailable" {
		t.Errorf("another installation sending the file on the same conversation = %d %s", r.Code, r.Raw)
	}
	e.cmd(other.Credential.Token, conv, "release", "key-rel-00009", match(claimed.Doc.Control.Version), nil)
	v = e.cmd(tok, conv, "claim", "key-claim-0010", "", nil).Doc.Control.Version
	// ... and the same installation cannot move its own file to another conversation it holds
	connB := e.connection(tn)
	convB, _ := e.conv(tn, convOpts{conn: &connB})
	e.message(tn, convB, "inbound", "oi", "received", 0, nil)
	vB := e.cmd(tok, convB, "claim", "key-claim-0004", "", nil).Doc.Control.Version
	if r := e.cmd(tok, convB, "messages", "key-msg-00002", match(vB), map[string]any{"attachment_id": up.Doc.ID}); r.Code != 422 || r.Doc.Code != "attachment_unavailable" {
		t.Errorf("a file from another conversation = %d %s", r.Code, r.Raw)
	}
	// another company cannot even see the conversation, and an unknown attachment id is the same answer as someone else's
	b := e.tenant()
	adminB := e.member(b, "tenant_admin")
	appB := e.install(adminB, b, "CRM B", writeScopes...)
	if r := e.upload(appB.Credential.Token, conv, "foto.png", tinyPNG(t, 30)); r.Code != 404 {
		t.Errorf("another company's upload = %d %s", r.Code, r.Raw)
	}
	if r := e.cmd(tok, conv, "messages", "key-msg-00003", match(v), map[string]any{"attachment_id": uuid.New()}); r.Code != 422 || r.Doc.Code != "attachment_unavailable" {
		t.Errorf("unknown attachment = %d %s", r.Code, r.Raw)
	}
	// a person of the company cannot use the application's file either: the operator path does not know it
	if n := e.count(`SELECT count(*) FROM message_outbound_media WHERE id=$1 AND uploaded_by IS NOT NULL`, up.Doc.ID); n != 0 {
		t.Fatal("an application's file has no person as owner")
	}
}

func TestAnApplicationUploadIsHeldToTheOperatorsRules(t *testing.T) {
	e := newEnv(t)
	e.mediaAPI()
	tn, _, app, conv, _ := e.writeEnv()
	tok := app.Credential.Token
	e.cmd(tok, conv, "claim", "key-claim-0001", "", nil)
	// the antivirus blocks it, and nothing is recorded
	if r := e.upload(tok, conv, "eicar.txt", []byte("X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*")); r.Code != 422 {
		t.Errorf("infected file = %d %s", r.Code, r.Raw)
	}
	// not an image at all
	if r := e.upload(tok, conv, "x.exe", []byte("MZ\x90\x00 not allowed")); r.Code != 422 || r.Doc.Code != "file_rejected" {
		t.Errorf("a file of a kind that is not sent = %d %s", r.Code, r.Raw)
	}
	if n := e.count(`SELECT count(*) FROM message_outbound_media WHERE tenant_id=$1`, tn); n != 0 {
		t.Fatalf("refused files leave no record: %d", n)
	}
	// at most 5 pending files per conversation and installation
	for i := 0; i < 5; i++ {
		if r := e.upload(tok, conv, "f.png", tinyPNG(t, uint8(50+i))); r.Code != 201 {
			t.Fatalf("upload %d = %d %s", i, r.Code, r.Raw)
		}
	}
	if r := e.upload(tok, conv, "f.png", tinyPNG(t, 99)); r.Code != 409 || r.Doc.Code != "too_many_attachments" {
		t.Errorf("sixth pending file = %d %s", r.Code, r.Raw)
	}
	// a token without message.send cannot upload
	admin := e.member(tn, "tenant_admin")
	readOnly := e.install(admin, tn, "reader", "conversation.read", "conversation.claim")
	if r := e.upload(readOnly.Credential.Token, conv, "f.png", tinyPNG(t, 7)); r.Code != 403 {
		t.Errorf("without message.send = %d", r.Code)
	}
	// once it lets go of the conversation the application cannot park more files
	e.cmd(tok, conv, "release", "key-rel-00001", match(e.version(conv)), nil)
	if r := e.upload(tok, conv, "f.png", tinyPNG(t, 8)); r.Code != 409 || r.Doc.Code != "not_holder" {
		t.Errorf("upload after release = %d %s", r.Code, r.Raw)
	}
	// ... and a file already uploaded cannot be sent by someone who does not hold the conversation any more
	var pending uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT id FROM message_outbound_media WHERE conversation_id=$1 LIMIT 1`, conv).Scan(&pending)
	if r := e.cmd(tok, conv, "messages", "key-msg-00001", match(e.version(conv)), map[string]any{"attachment_id": pending}); r.Code != 409 || r.Doc.Code != "not_holder" {
		t.Errorf("send after release = %d %s", r.Code, r.Raw)
	}
}

func TestAnApplicationCannotUploadWhereOutboundMediaIsOff(t *testing.T) {
	e := newEnv(t)
	e.writeAPI() // no Media
	_, _, app, conv, _ := e.writeEnv()
	e.cmd(app.Credential.Token, conv, "claim", "key-claim-0001", "", nil)
	if r := e.upload(app.Credential.Token, conv, "f.png", tinyPNG(t, 1)); r.Code != http.StatusNotImplemented {
		t.Errorf("media off = %d %s", r.Code, strings.TrimSpace(string(r.Raw)))
	}
	v := e.version(conv)
	if r := e.cmd(app.Credential.Token, conv, "messages", "key-msg-00001", match(v), map[string]any{"attachment_id": uuid.New()}); r.Code != 422 || r.Doc.Code != "media_unavailable" {
		t.Errorf("send of a file with media off = %d %s", r.Code, r.Raw)
	}
}

// version is the control version the conversation has now.
func (e *env) version(conv uuid.UUID) int64 {
	e.t.Helper()
	var v int64
	if err := e.seed.QueryRow(e.ctx, `SELECT control_version FROM conversations WHERE id=$1`, conv).Scan(&v); err != nil {
		e.t.Fatal(err)
	}
	return v
}
