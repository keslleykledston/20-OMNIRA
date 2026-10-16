package adapters_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	"github.com/omnira/omnira/internal/servicectx/adapters"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

// ADR-0049 addendum on a real PostgreSQL: the customer is told, in the conversation, when the protocol opens and when it ends. It is a courtesy: the session never
// depends on it, and a refusal (no line, closed window, not the holder) only means "not sent".

// withLine gives the conversation an active text line of the company (WAHA has no 24 h window; the official Meta line has one).
func (s *stack) withLine(conv uuid.UUID, provider, kind string) {
	s.t.Helper()
	id := uuid.New()
	s.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp',$3,$4,$5,'active','["text"]')`,
		id, s.env.TenantA, provider, kind, id.String())
	s.exec(`UPDATE conversations SET channel_connection_id=$2 WHERE id=$1`, conv, id)
}

// told returns the notices the customer was queued, oldest first.
func (s *stack) told(conv uuid.UUID) (out []string) {
	s.t.Helper()
	rows, err := s.env.Seed.Query(context.Background(), `SELECT body FROM messages WHERE conversation_id=$1 AND direction='outbound' ORDER BY created_at, id`, conv)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

// switchNotice records the company's own decision on the capability (what the Hub's "Capacidades" does).
func (s *stack) switchNotice(tenant uuid.UUID, on bool) {
	s.t.Helper()
	s.exec(`INSERT INTO tenant_entitlements(tenant_id, capability, enabled) VALUES($1,'service_protocol_notice',$2)
	        ON CONFLICT (tenant_id, capability) DO UPDATE SET enabled = EXCLUDED.enabled`, tenant, on)
}

// announce installs the notice and turns the company's switch ON (it starts off).
func (s *stack) announce() *adapters.Notice {
	n := adapters.NewNotice(s.env.App)
	s.svc.WithAnnouncer(n)
	s.switchNotice(s.env.TenantA, true)
	return n
}

func TestTheCustomerIsToldWhenTheProtocolOpensAndEnds(t *testing.T) {
	s := newStack(t)
	s.announce()
	conv, _ := s.conversation()
	s.withLine(conv, "waha", "unofficial")
	s.assign(conv, s.agent)
	sess := s.mustOpen(s.agent, conv)
	got := s.told(conv)
	if len(got) != 1 || !strings.Contains(got[0], "Seu atendimento foi registrado") || !strings.Contains(got[0], "Protocolo de atendimento: "+sess.Protocol) || !strings.HasPrefix(got[0], "Prezado Ana,") {
		t.Fatalf("the opening notice: %q", got)
	}
	// opening what is already open tells nobody again
	if _, err := s.open(s.agent, domain.OpenInput{ConversationID: &conv}); err != nil {
		t.Fatal(err)
	}
	if got = s.told(conv); len(got) != 1 {
		t.Fatalf("still one notice: %q", got)
	}
	if _, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResInformationProvided}, sess.Version); err != nil {
		t.Fatal(err)
	}
	got = s.told(conv)
	if len(got) != 2 || !strings.Contains(got[1], "Seu atendimento foi encerrado") || !strings.Contains(got[1], "Protocolo de atendimento: "+sess.Protocol) {
		t.Fatalf("the closing notice: %q", got)
	}
	if n := s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND idempotency_key IN ($2,$3)`, conv, "service-opened-"+sess.ID.String(), "service-closed-"+sess.ID.String()); n != 2 {
		t.Fatalf("one message per protocol and kind, under its key: %d", n)
	}
}

func TestNothingIsSentUntilTheCompanyTurnsItsSwitchOn(t *testing.T) {
	s := newStack(t)
	talk := func() uuid.UUID {
		conv, _ := s.conversation()
		s.withLine(conv, "waha", "unofficial")
		s.assign(conv, s.agent)
		s.mustOpen(s.agent, conv)
		return conv
	}
	talk() // no announcer installed at all
	s.svc.WithAnnouncer(adapters.NewNotice(s.env.App))
	if got := s.told(talk()); len(got) != 0 {
		t.Fatalf("the switch starts OFF: %q", got)
	}
	s.switchNotice(s.env.TenantA, false)
	if got := s.told(talk()); len(got) != 0 {
		t.Fatalf("switched off: %q", got)
	}
	s.switchNotice(s.env.TenantA, true)
	if got := s.told(talk()); len(got) != 1 {
		t.Fatalf("switched on: %q", got)
	}
	// another company's decision is not this one's
	s.switchNotice(s.env.TenantA, false)
	s.switchNotice(s.env.TenantB, true)
	if got := s.told(talk()); len(got) != 0 {
		t.Fatalf("only the company that turned it on is messaging: %q", got)
	}
}

func TestTheProtocolOpensEvenWhenTheCustomerCannotBeTold(t *testing.T) {
	s := newStack(t)
	s.announce()
	// no line at all
	noLine, _ := s.conversation()
	s.assign(noLine, s.agent)
	if got := s.mustOpen(s.agent, noLine); got.Protocol == "" {
		t.Fatal("opened")
	}
	// the official line and no message from the customer in the last 24 h: free text is refused
	closedWindow, _ := s.conversation()
	s.withLine(closedWindow, "meta_cloud", "official")
	s.assign(closedWindow, s.agent)
	s.mustOpen(s.agent, closedWindow)
	// the conversation belongs to someone else
	other, _ := s.conversation()
	s.withLine(other, "waha", "unofficial")
	s.assign(other, s.limited)
	s.mustOpen(s.agent, other)
	for name, c := range map[string]uuid.UUID{"no line": noLine, "closed window": closedWindow, "not the holder": other} {
		if got := s.told(c); len(got) != 0 {
			t.Fatalf("%s: nothing is queued: %q", name, got)
		}
		if rows := s.sessionsOf(c); len(rows) != 1 || rows[0].status != "open" {
			t.Fatalf("%s: the protocol stands: %+v", name, rows)
		}
	}
}

func TestAssumingAConversationTellsTheCustomerItsProtocol(t *testing.T) {
	s := newStack(t)
	s.announce()
	s.agentProfile(s.agent)
	conv, _ := s.conversation()
	s.withLine(conv, "waha", "unofficial")
	if res, err := s.claim(s.assigner(s.installedHook()), s.agent, conv); err != nil || !res.Changed {
		t.Fatalf("claim: %v %+v", err, res)
	}
	rows := s.sessionsOf(conv)
	got := s.told(conv)
	if len(rows) != 1 || len(got) != 1 || !strings.Contains(got[0], "Protocolo de atendimento: "+rows[0].protocol) {
		t.Fatalf("the claim opened the protocol and told the customer: %+v %q", rows, got)
	}
}

func TestFinalizingTellsTheCustomerTheProtocolEndsBeforeTheConversationCloses(t *testing.T) {
	s := newStack(t)
	notice := s.announce()
	conv, _ := s.conversation()
	s.withLine(conv, "waha", "unofficial")
	s.assign(conv, s.agent)
	sess := s.mustOpen(s.agent, conv)
	att := s.attendance(true).WithSessionNotice(adapters.FinalizeNotice(s.env.App, notice))
	if err := s.finalize(att, s.agent, conv, attendancedomain.ReasonResolved); err != nil {
		t.Fatal(err)
	}
	got := s.told(conv)
	if len(got) != 2 || !strings.Contains(got[1], "Seu atendimento foi encerrado") || !strings.Contains(got[1], sess.Protocol) {
		t.Fatalf("opening then closing notice: %q", got)
	}
	if rows := s.sessionsOf(conv); len(rows) != 1 || rows[0].status != "closed" {
		t.Fatalf("the finalization closed the protocol: %+v", rows)
	}
	if n := s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='closed'`, conv); n != 1 {
		t.Fatal("the conversation is closed")
	}
	// finalizing again (a replay) tells nobody twice
	if err := s.finalize(att, s.agent, conv, attendancedomain.ReasonResolved); err != nil {
		t.Fatal(err)
	}
	if got = s.told(conv); len(got) != 2 {
		t.Fatalf("a replay sends nothing more: %q", got)
	}
}

func TestFinalizingAConversationWithoutAProtocolTellsNobody(t *testing.T) {
	s := newStack(t)
	notice := s.announce()
	conv, _ := s.conversation()
	s.withLine(conv, "waha", "unofficial")
	s.assign(conv, s.agent)
	att := s.attendance(true).WithSessionNotice(adapters.FinalizeNotice(s.env.App, notice))
	if err := s.finalize(att, s.agent, conv, attendancedomain.ReasonResolved); err != nil {
		t.Fatal(err)
	}
	if got := s.told(conv); len(got) != 0 {
		t.Fatalf("no active protocol was shown to the customer: %q", got)
	}
}
