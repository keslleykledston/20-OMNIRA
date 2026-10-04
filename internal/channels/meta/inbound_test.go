package meta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/meta"
)

const inboundBody = `{"entry":[{"changes":[{"value":{
 "metadata":{"phone_number_id":"pn1"},
 "messages":[
  {"from":"5511999990000","id":"wamid.1","timestamp":"1700000000","type":"text","text":{"body":"oi"}},
  {"from":"5511999990000","id":"wamid.2","timestamp":"1700000001","type":"image","image":{"id":"m1","mime_type":"image/jpeg"}},
  {"from":"5511999990000","id":"wamid.3","timestamp":"1700000002","type":"reaction"}],
 "statuses":[
  {"id":"wamid.out","status":"failed","timestamp":"1700000003","errors":[{"code":131047,"title":"window"}]},
  {"id":"wamid.out","status":"delivered","timestamp":"1700000004"},
  {"id":"wamid.out","status":"weird","timestamp":"1700000005"}]}}]}]}`

func TestParseEvents(t *testing.T) {
	conn := domain.ChannelConnection{ID: uuid.New()}
	evs, err := meta.ParseEvents(conn, []byte(inboundBody))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 4 { // reaction and unknown status skipped
		t.Fatalf("events=%d, want 4", len(evs))
	}
	if m := evs[0].Message; m.ProviderMessageID != "wamid.1" || m.Text != "oi" || m.FromE164 != "+5511999990000" || m.Timestamp.Unix() != 1700000000 {
		t.Fatalf("text msg: %+v", m)
	}
	if m := evs[1].Message; m.Media == nil || m.Media.Kind != domain.MediaKindImage || m.Media.MediaRef != "m1" {
		t.Fatalf("media msg: %+v", m)
	}
	if s := evs[2].Status; s.State != domain.DeliveryStateFailed || !strings.Contains(s.Reason, "131047") || evs[2].Key != "wamid.out:failed" {
		t.Fatalf("failed status: %+v key=%s", s, evs[2].Key)
	}
	if evs[3].Key != "wamid.out:delivered" {
		t.Fatalf("status keys must be per-state, got %s", evs[3].Key)
	}
}

func TestParseEventsMalformed(t *testing.T) {
	if _, err := meta.ParseEvents(domain.ChannelConnection{}, []byte("{")); err == nil {
		t.Fatal("want error")
	}
}

type fakeIntake struct {
	seen   map[string]bool
	err    error
	tenant uuid.UUID
}

func (f *fakeIntake) ProcessWebhook(_ context.Context, c domain.ChannelConnection, key, _, _ string, _ *domain.InboundMessage, _ *domain.DeliveryStatusUpdate) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.tenant = c.TenantID
	dup := f.seen[key]
	f.seen[key] = true
	return dup, nil
}

func post(h http.Handler, body string) int {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/v1/whatsapp/meta", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signature([]byte(body), "secret"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestHandlerIntakeUsesConnectionTenantAndDedupes(t *testing.T) {
	tenant := uuid.New()
	conn := &domain.ChannelConnection{ID: uuid.New(), TenantID: tenant, Provider: domain.ProviderMetaCloud, ProviderKind: domain.ProviderKindOfficial, Status: domain.ConnectionStatusActive}
	intake := &fakeIntake{seen: map[string]bool{}}
	h := meta.Handler{AppSecret: "secret", Resolver: &resolver{conn: conn}, Intake: intake}
	if code := post(h, inboundBody); code != 200 {
		t.Fatalf("code=%d", code)
	}
	if intake.tenant != tenant {
		t.Fatal("intake must receive resolved connection (tenant from connection, never payload)")
	}
	if len(intake.seen) != 4 {
		t.Fatalf("keys=%v", intake.seen)
	}
	if code := post(h, inboundBody); code != 200 { // redelivery
		t.Fatalf("redelivery code=%d", code)
	}
}

func TestHandlerIntakeFailureReturns503(t *testing.T) {
	conn := &domain.ChannelConnection{ID: uuid.New(), TenantID: uuid.New(), Provider: domain.ProviderMetaCloud, ProviderKind: domain.ProviderKindOfficial, Status: domain.ConnectionStatusActive}
	h := meta.Handler{AppSecret: "secret", Resolver: &resolver{conn: conn}, Intake: &fakeIntake{err: context.DeadlineExceeded}}
	if code := post(h, inboundBody); code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d", code)
	}
}

func TestHandlerRejectsUnsignedBeforeIntake(t *testing.T) {
	intake := &fakeIntake{seen: map[string]bool{}}
	h := meta.Handler{AppSecret: "secret", Resolver: &resolver{conn: &domain.ChannelConnection{}}, Intake: intake}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(inboundBody))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 || len(intake.seen) != 0 {
		t.Fatalf("code=%d seen=%v", rec.Code, intake.seen)
	}
}

func TestParseEventsKeepsTheRepliedMessageAndTheSenderID(t *testing.T) {
	body := `{"entry":[{"changes":[{"value":{"messages":[
	  {"from":"5511999990000","id":"wamid.r1","timestamp":"1700000000","type":"text","text":{"body":"sim"},"context":{"from":"5511888880000","id":"wamid.ORIGINAL"}},
	  {"from":"5511999990000","id":"wamid.r2","timestamp":"1700000001","type":"text","text":{"body":"não"}},
	  {"from":"5511999990000","id":"wamid.r3","timestamp":"1700000002","type":"text","text":{"body":"x"},"context":{"id":"<b>bad id</b>"}}
	]}}]}]}`
	evs, err := meta.ParseEvents(domain.ChannelConnection{ID: uuid.New()}, []byte(body))
	if err != nil || len(evs) != 3 {
		t.Fatalf("events = %d %v", len(evs), err)
	}
	if m := evs[0].Message; m.ReplyToExternalID != "wamid.ORIGINAL" || m.ParticipantID != "5511999990000" {
		t.Fatalf("reply message = %+v", m)
	}
	if m := evs[1].Message; m.ReplyToExternalID != "" || m.ParticipantID != "5511999990000" {
		t.Fatalf("plain message = %+v", m)
	}
	if m := evs[2].Message; m.ReplyToExternalID != "" {
		t.Fatalf("a malformed context id must be dropped, got %q", m.ReplyToExternalID)
	}
}
