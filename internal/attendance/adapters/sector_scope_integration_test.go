package adapters_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ADR-0043 S3: the memory of a contact (earlier attendances, open follow-ups, the search over earlier messages) must not carry conversations outside the person's queues.
func TestContactHistoryHidesWhatTheSectorScopeHides(t *testing.T) {
	s := newStack(t)
	// the attendant works in queue QA; the contact also talked, earlier, in queue QB which the attendant does not belong to
	qa, qb := uuid.New(), uuid.New()
	s.exec(`INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$3,'QA','manual'),($2,$3,'QB','manual')`, qa, qb, s.env.TenantA)
	s.exec(`INSERT INTO queue_members(tenant_id,queue_id,user_id,active) VALUES($1,$2,$3,true)`, s.env.TenantA, qa, s.agent)
	current, contact := s.conversation(&s.agent)
	s.exec(`UPDATE conversations SET queue_id=$2 WHERE id=$1`, current, qa)
	visible, _ := s.env.SeedConversation(t, s.env.TenantA, "none")
	hidden, _ := s.env.SeedConversation(t, s.env.TenantA, "none")
	for conv, q := range map[uuid.UUID]uuid.UUID{visible: qa, hidden: qb} {
		s.exec(`UPDATE conversations SET contact_id=$2, queue_id=$3, status='closed', closed_at=now() WHERE id=$1`, conv, contact, q)
	}
	for conv, label := range map[uuid.UUID]string{visible: "visivel", hidden: "oculto"} {
		closure := uuid.New()
		s.exec(`INSERT INTO conversation_closures(id,tenant_id,conversation_id,contact_id,source,reason,summary) VALUES($1,$2,$3,$4,'agent','resolved',$5)`, closure, s.env.TenantA, conv, contact, "resumo "+label)
		s.exec(`INSERT INTO follow_up_items(tenant_id,contact_id,conversation_id,closure_id,kind,text) VALUES($1,$2,$3,$4,'pending',$5)`, s.env.TenantA, contact, conv, closure, "pendencia "+label)
		s.message(conv, "inbound", "a fatura "+label+" veio errada", 24*time.Hour)
	}
	mux := s.mux()
	history := func() string {
		code, body := s.callRaw(mux, s.agent, http.MethodGet, "/contacts/"+contact.String()+"/attendance-history", "")
		if code != 200 {
			t.Fatalf("attendance-history = %d %s", code, body)
		}
		return body
	}
	search := func() string {
		code, body := s.callRaw(mux, s.agent, http.MethodGet, "/inbox/conversations/"+current.String()+"/history-search?q=fatura", "")
		if code != 200 {
			t.Fatalf("history-search = %d %s", code, body)
		}
		return body
	}
	// scope OFF (the default): everything, as before
	if h := history(); !strings.Contains(h, "resumo oculto") || !strings.Contains(h, "pendencia oculto") || !strings.Contains(search(), "oculto") {
		t.Fatalf("scope off must change nothing: %s", history())
	}
	s.exec(`INSERT INTO tenant_entitlements(tenant_id,capability,enabled) VALUES($1,'sector_scope',true)`, s.env.TenantA)
	h := history()
	if strings.Contains(h, "oculto") || !strings.Contains(h, "resumo visivel") || !strings.Contains(h, "pendencia visivel") {
		t.Errorf("scope on: the history shows what is outside the queues, or lost what is inside: %s", h)
	}
	if sr := search(); strings.Contains(sr, "oculto") || !strings.Contains(sr, "visivel") {
		t.Errorf("scope on: the message search crosses queues: %s", sr)
	}
	// the attendance-context of the current conversation is the same memory
	code, ctxBody := s.callRaw(mux, s.agent, http.MethodGet, "/inbox/conversations/"+current.String()+"/attendance-context", "")
	if code != 200 || strings.Contains(ctxBody, "oculto") {
		t.Errorf("attendance-context = %d %s", code, ctxBody)
	}
}

func (s *stack) callRaw(mux *http.ServeMux, user uuid.UUID, method, path, raw string) (int, string) {
	s.t.Helper()
	req := httptest.NewRequest(method, fmt.Sprintf("/api/v1/tenants/%s%s", s.env.TenantA, path), bytes.NewReader([]byte(raw)))
	if user != uuid.Nil {
		req.Header.Set("X-Test-User", user.String())
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}
