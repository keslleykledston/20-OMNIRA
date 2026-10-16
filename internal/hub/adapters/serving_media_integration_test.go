package adapters_test

// ADR-0024 x ADR-0040 on a real PostgreSQL, with the application's own role: a Hub agent sends files to the customer of a conversation it holds, through the Hub's
// own routes ("Todas") and through the instance routes of a person attending the company through the Hub. What must hold:
//   - the file goes through the SAME checks as a member's (quotas, kind, metadata, antivirus, the company's outbound_attachments switch) and nothing is stored when
//     one refuses;
//   - the conversation must be the agent's, the line live and the delegation proven (reply capability, key conversation.reply), also inside the transaction;
//   - an upload is the uploader's: another agent can never send it;
//   - the audit names the hub; the answers never carry paths.

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	"github.com/omnira/omnira/internal/entitlements"
	"github.com/omnira/omnira/internal/hub/adapters"
	mediaadapters "github.com/omnira/omnira/internal/media/adapters"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/messages/ports"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

type mediaScanner struct{ down atomic.Bool }

func (m *mediaScanner) Scan(_ context.Context, data []byte) (ports.VirusVerdict, error) {
	if m.down.Load() {
		return ports.VirusVerdict{}, errors.New("clamd unreachable")
	}
	if bytes.Contains(data, []byte("EICAR-STANDARD-ANTIVIRUS-TEST-FILE")) {
		return ports.VirusVerdict{Infected: true, Signature: "Eicar-Test-Signature"}, nil
	}
	return ports.VirusVerdict{}, nil
}

type mediaAPI struct {
	w       *world
	h       http.Handler
	scanner *mediaScanner
}

// mediaAPI wires the real handlers the way the server does: the attachments service built with the delegated sender and the company switch, the Hub's own routes with
// the body read before the session, and the instance routes behind Delegable.
func (w *world) mediaAPI(t *testing.T) *mediaAPI {
	t.Helper()
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	files, err := mediaadapters.NewOutboundFiles(t.TempDir())
	w.must(err)
	scanner := &mediaScanner{}
	store := messagesadapters.NewPostgresOutboundStore(w.app)
	perms := channeladapters.NewPostgresPermissionChecker(w.app)
	sender := messagesapplication.NewSender(store, perms).WithMediaProviders(func(string) bool { return true }).WithEntitlements(entitlements.NewChecker(w.app).Gate)
	att := messagesapplication.NewAttachments(sender, store, files, scanner).WithDelegated(messagesapplication.NewDelegatedSender(store))
	send := messagesadapters.NewSendHandler(messagesapplication.NewSender(store, perms)).WithAttachments(att, store)

	hubH := adapters.NewHTTPHandler(w.app).WithAttachments(att)
	dw := adapters.NewDelegatedWrites(w.app).WithAttachments(att)
	userSession := tenancyadapters.UserSessionMiddleware(w.app)
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(w.app), tenancyadapters.NewPostgresTenantRepository(w.app))
	tenantSession := tenancyadapters.AuthorizationMiddleware(w.app, authz)
	who := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if u, err := uuid.Parse(r.Header.Get("X-Test-User")); err == nil {
				r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: u}))
			}
			next.ServeHTTP(rw, r)
		})
	}
	attachmentsOn := entitlements.NewChecker(w.app).Require(entitlements.OutboundAttachments)
	mux := http.NewServeMux()
	// the Hub's own routes
	mux.Handle("POST /api/v1/hubs/{hub_id}/inbox/{item_id}/attachments", who(send.BufferUpload(userSession(http.HandlerFunc(hubH.UploadAttachment)))))
	mux.Handle("DELETE /api/v1/hubs/{hub_id}/inbox/{item_id}/attachments/{attachment_id}", who(userSession(http.HandlerFunc(hubH.RemoveAttachment))))
	mux.Handle("POST /api/v1/hubs/{hub_id}/inbox/{item_id}/messages", who(userSession(http.HandlerFunc(hubH.ReplyItem))))
	mux.Handle("GET /api/v1/hubs/{hub_id}/inbox/{item_id}/channel", who(userSession(http.HandlerFunc(hubH.ChannelItem))))
	// the instance routes of a person attending the company through the Hub
	base := "/api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}"
	mux.Handle("POST "+base+"/attachments", tenancyadapters.Delegable("conversation.reply", who(send.BufferUpload(tenantSession(attachmentsOn(dw.Upload(http.HandlerFunc(send.Upload))))))))
	mux.Handle("DELETE "+base+"/attachments/{attachment_id}", tenancyadapters.Delegable("conversation.reply", who(tenantSession(attachmentsOn(dw.RemoveAttachment(http.HandlerFunc(send.RemoveAttachment)))))))
	mux.Handle("POST "+base+"/messages", tenancyadapters.Delegable("conversation.reply", who(tenantSession(dw.Reply(http.HandlerFunc(send.Send))))))
	mux.Handle("GET "+base+"/channel", tenancyadapters.Delegable("conversation.read", who(tenantSession(dw.Channel(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(http.StatusNoContent) }))))))
	return &mediaAPI{w: w, h: mux, scanner: scanner}
}

func smallPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 6, 6))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (a *mediaAPI) do(req *http.Request, user uuid.UUID, acting string) (int, string) {
	req.Header.Set("X-Test-User", user.String())
	if acting != "" {
		req.Header.Set("X-Omnira-Acting-As", acting)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func uploadReq(t *testing.T, url, name, mime string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	h.Set("Content-Type", mime)
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(data)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, url, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func jsonReq(method, url, body string, headers ...string) *http.Request {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	return req
}

func idOf(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `"id":"`)
	if i < 0 {
		t.Fatalf("no id in %s", body)
	}
	rest := body[i+len(`"id":"`):]
	return rest[:strings.Index(rest, `"`)]
}

func TestAHubAgentSendsAFileThroughTheHubsOwnRoutes(t *testing.T) {
	w := newWorld(t)
	api := w.mediaAPI(t)
	w.connect("A")
	agent := w.hubAgent("agent")
	other := w.hubAgent("other")
	w.grantReply(agent, "A")
	w.grantReply(other, "A")
	conv, item, tenant := w.conv["A"], w.itemID("A"), w.tenant["A"]
	base := "/api/v1/hubs/" + w.hub.String() + "/inbox/" + item.String()
	q := "?expected_tenant_id=" + tenant.String()
	hold := func(u *uuid.UUID) { w.exec(`UPDATE conversations SET assigned_to_user_id = $2 WHERE id = $1`, conv, u) }
	files := func() int {
		return w.count(`SELECT count(*) FROM message_outbound_media WHERE conversation_id = $1`, conv)
	}

	t.Run("a conversation nobody holds, or someone else's, takes no file; nothing is stored", func(t *testing.T) {
		hold(nil)
		if code, b := api.do(uploadReq(t, base+"/attachments"+q, "a.png", "image/png", smallPNG(t)), agent, ""); code != 409 {
			t.Errorf("unassigned: %d %s, want 409", code, b)
		}
		hold(&other)
		if code, b := api.do(uploadReq(t, base+"/attachments"+q, "a.png", "image/png", smallPNG(t)), agent, ""); code != 409 {
			t.Errorf("held by another agent: %d %s, want 409", code, b)
		}
		if files() != 0 {
			t.Error("a refused upload stored a record")
		}
	})

	hold(&agent)

	t.Run("the holder uploads and sends it: a media message is queued in the agent's name, the audit names the hub, and the answer carries no path", func(t *testing.T) {
		code, b := api.do(uploadReq(t, base+"/attachments"+q, "foto.png", "image/png", smallPNG(t)), agent, "")
		if code != 201 || !strings.Contains(b, `"kind":"image"`) {
			t.Fatalf("upload: %d %s", code, b)
		}
		if strings.Contains(b, "/") && strings.Contains(b, "outbound") {
			t.Errorf("the answer leaks a path: %s", b)
		}
		attID := idOf(t, b)
		code, b = api.do(jsonReq("POST", base+"/messages", `{"expected_tenant_id":"`+tenant.String()+`","text":"segue a foto","attachment_id":"`+attID+`"}`, "Idempotency-Key", "media-key-0001"), agent, "")
		if code != 202 {
			t.Fatalf("send: %d %s", code, b)
		}
		if w.count(`SELECT count(*) FROM message_outbound_media m JOIN messages x ON x.id = m.message_id WHERE m.id = $1 AND x.direction = 'outbound' AND x.sent_by_user_id = $2 AND x.body = 'segue a foto'`, attID, agent) != 1 {
			t.Error("the file is not attached to an outbound message of the agent")
		}
		if w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor_id = $2 AND action = 'hub.attachment.uploaded'`, tenant, agent) != 1 ||
			w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor_id = $2 AND action = 'hub.message.sent' AND metadata->>'attachment_id' = $3`, tenant, agent, attID) != 1 {
			t.Error("the upload and the send are not audited naming the hub")
		}
		// the same key again is a replay, not a second message
		if code, _ := api.do(jsonReq("POST", base+"/messages", `{"expected_tenant_id":"`+tenant.String()+`","text":"segue a foto","attachment_id":"`+attID+`"}`, "Idempotency-Key", "media-key-0001"), agent, ""); code != 200 {
			t.Errorf("replay: %d, want 200", code)
		}
		if w.count(`SELECT count(*) FROM messages WHERE conversation_id = $1 AND direction = 'outbound' AND body = 'segue a foto'`, conv) != 1 {
			t.Error("a replay queued a second message")
		}
	})

	t.Run("an upload is the uploader's: another agent can never send it", func(t *testing.T) {
		code, b := api.do(uploadReq(t, base+"/attachments"+q, "mine.png", "image/png", smallPNG(t)), agent, "")
		if code != 201 {
			t.Fatalf("upload: %d %s", code, b)
		}
		attID := idOf(t, b)
		hold(&other)
		if code, b := api.do(jsonReq("POST", base+"/messages", `{"expected_tenant_id":"`+tenant.String()+`","text":"","attachment_id":"`+attID+`"}`, "Idempotency-Key", "media-key-0002"), other, ""); code != 422 {
			t.Errorf("another agent sends the file: %d %s, want 422", code, b)
		}
		hold(&agent)
	})

	t.Run("an infected file, an antivirus that is down and a file that is not a file are refused and store nothing", func(t *testing.T) {
		before := files()
		eicar := []byte(`X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`)
		if code, _ := api.do(uploadReq(t, base+"/attachments"+q, "e.txt", "text/plain", eicar), agent, ""); code != 422 {
			t.Errorf("eicar: %d, want 422", code)
		}
		api.scanner.down.Store(true)
		if code, _ := api.do(uploadReq(t, base+"/attachments"+q, "a.png", "image/png", smallPNG(t)), agent, ""); code != 503 {
			t.Errorf("antivirus down: %d, want 503", code)
		}
		api.scanner.down.Store(false)
		if code, _ := api.do(uploadReq(t, base+"/attachments"+q, "x.exe", "application/octet-stream", []byte("MZ\x90\x00not a document")), agent, ""); code != 422 {
			t.Errorf("an executable: %d, want 422", code)
		}
		if files() != before {
			t.Errorf("refused uploads stored %d records", files()-before)
		}
	})

	t.Run("the company's outbound_attachments switch decides: off, the Hub's write path refuses too", func(t *testing.T) {
		w.exec(`INSERT INTO tenant_entitlements(tenant_id, capability, enabled) VALUES ($1, 'outbound_attachments', false) ON CONFLICT (tenant_id, capability) DO UPDATE SET enabled = false`, tenant)
		if code, b := api.do(uploadReq(t, base+"/attachments"+q, "a.png", "image/png", smallPNG(t)), agent, ""); code != 403 {
			t.Errorf("switch off: %d %s, want 403", code, b)
		}
		if code, b := api.do(httptest.NewRequest("GET", base+"/channel"+q, nil), agent, ""); code != 200 || !strings.Contains(b, `"can_send_media":false`) {
			t.Errorf("channel with the switch off: %d %s", code, b)
		}
		w.exec(`DELETE FROM tenant_entitlements WHERE tenant_id = $1 AND capability = 'outbound_attachments'`, tenant)
		if code, b := api.do(httptest.NewRequest("GET", base+"/channel"+q, nil), agent, ""); code != 200 || !strings.Contains(b, `"can_send_media":true`) || !strings.Contains(b, `"can_send_text":true`) {
			t.Errorf("channel with the switch on: %d %s", code, b)
		}
	})

	t.Run("a read-only grant and a wrong company on screen are refused before anything is touched", func(t *testing.T) {
		reader := w.hubAgent("reader")
		w.grant(reader, "A") // read-only
		if code, _ := api.do(uploadReq(t, base+"/attachments"+q, "a.png", "image/png", smallPNG(t)), reader, ""); code != 403 {
			t.Errorf("read-only grant: %d, want 403", code)
		}
		if code, _ := api.do(uploadReq(t, base+"/attachments?expected_tenant_id="+w.tenant["B"].String(), "a.png", "image/png", smallPNG(t)), agent, ""); code != 409 {
			t.Errorf("wrong company on screen: %d, want 409", code)
		}
	})

	t.Run("an unsent upload can be taken out, and only by its owner", func(t *testing.T) {
		code, b := api.do(uploadReq(t, base+"/attachments"+q, "gone.png", "image/png", smallPNG(t)), agent, "")
		if code != 201 {
			t.Fatalf("upload: %d %s", code, b)
		}
		attID := idOf(t, b)
		hold(&other)
		if code, _ := api.do(httptest.NewRequest("DELETE", base+"/attachments/"+attID+q, nil), other, ""); code != 422 {
			t.Errorf("another agent removes it: %d, want 422", code)
		}
		hold(&agent)
		if code, _ := api.do(httptest.NewRequest("DELETE", base+"/attachments/"+attID+q, nil), agent, ""); code != 204 {
			t.Errorf("the owner removes it: %d, want 204", code)
		}
	})
}

func TestAHubAgentSendsAFileThroughTheInstanceRoutes(t *testing.T) {
	w := newWorld(t)
	api := w.mediaAPI(t)
	w.connect("A")
	hub := "hub:" + w.hub.String()
	agent := w.hubAgent("agent")
	g := w.grantReply(agent, "A")
	w.ceiling("A", "conversation.read", "conversation.claim", "conversation.reply")
	w.grantKeys(g, "conversation.read", "conversation.claim", "conversation.reply")
	conv := w.conv["A"]
	w.exec(`UPDATE conversations SET assigned_to_user_id = $2 WHERE id = $1`, conv, agent)
	base := "/api/v1/tenants/" + w.tenant["A"].String() + "/inbox/conversations/" + conv.String()

	code, b := api.do(uploadReq(t, base+"/attachments", "foto.png", "image/png", smallPNG(t)), agent, hub)
	if code != 201 {
		t.Fatalf("upload through the instance route: %d %s", code, b)
	}
	attID := idOf(t, b)
	if code, b := api.do(jsonReq("POST", base+"/messages", `{"text":"olha","attachment_id":"`+attID+`"}`, "Idempotency-Key", "media-key-1001"), agent, hub); code != 202 {
		t.Fatalf("send through the instance route: %d %s", code, b)
	}
	if w.count(`SELECT count(*) FROM message_outbound_media m JOIN messages x ON x.id = m.message_id WHERE m.id = $1 AND x.sent_by_user_id = $2`, attID, agent) != 1 {
		t.Error("the file is not attached to the agent's message")
	}
	if code, b := api.do(httptest.NewRequest("GET", base+"/channel", nil), agent, hub); code != 200 || !strings.Contains(b, `"can_send_media":true`) {
		t.Errorf("channel for a delegate: %d %s", code, b)
	}

	t.Run("without the key conversation.reply the route answers 403; an instance with no grant is a 404; outside the delegated context a non-member is nobody", func(t *testing.T) {
		w.grantKeys(g, "conversation.read", "conversation.claim")
		if code, _ := api.do(uploadReq(t, base+"/attachments", "a.png", "image/png", smallPNG(t)), agent, hub); code != 403 {
			t.Errorf("no reply key: %d, want 403", code)
		}
		w.grantKeys(g, "conversation.read", "conversation.claim", "conversation.reply")
		if code, _ := api.do(uploadReq(t, "/api/v1/tenants/"+w.tenant["B"].String()+"/inbox/conversations/"+w.conv["B"].String()+"/attachments", "a.png", "image/png", smallPNG(t)), agent, hub); code != 404 {
			t.Errorf("another instance: %d, want 404", code)
		}
		if code, _ := api.do(uploadReq(t, base+"/attachments", "a.png", "image/png", smallPNG(t)), agent, ""); code != 403 && code != 404 {
			t.Errorf("not acting for the hub: %d, want 403/404", code)
		}
	})

	t.Run("a grant revoked between the upload and the send stops the send", func(t *testing.T) {
		code, b := api.do(uploadReq(t, base+"/attachments", "b.png", "image/png", smallPNG(t)), agent, hub)
		if code != 201 {
			t.Fatalf("upload: %d %s", code, b)
		}
		attID := idOf(t, b)
		w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g)
		if code, _ := api.do(jsonReq("POST", base+"/messages", `{"text":"","attachment_id":"`+attID+`"}`, "Idempotency-Key", "media-key-1002"), agent, hub); code == 202 {
			t.Error("a revoked grant still sent a file")
		}
	})
}
