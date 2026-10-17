package adapters_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	attendanceapplication "github.com/omnira/omnira/internal/attendance/application"
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	"github.com/omnira/omnira/internal/servicectx/adapters"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
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
	att := s.attendance(true).WithSessionNotice(adapters.FinalizeNotice(s.env.App, notice, s.svc))
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

// Owner decision 2026-10-17: every attendance ends with a protocol (it is how it is traced); only telling the customer the NUMBER is optional.
func (s *stack) finalizeNotify(att *attendanceapplication.Service, user, conv uuid.UUID, reason attendancedomain.CloseReason, notify *bool) error {
	return s.try(user, s.env.TenantA, func(ctx context.Context) error {
		_, err := att.Finalize(ctx, attendancedomain.FinalizeInput{ConversationID: conv, Reason: reason, Summary: "Cliente tinha o link fora; voltou.", Note: "ok", NotifyProtocol: notify})
		return err
	})
}

func TestFinalizingAnAttendanceNobodyAssumedGivesItAProtocolAndTellsTheCustomerOnce(t *testing.T) {
	s := newStack(t)
	notice := s.announce()
	conv, _ := s.conversation()
	s.withLine(conv, "waha", "unofficial")
	s.assign(conv, s.agent) // held, but never opened a protocol (e.g. assigned by a rule)
	att := s.attendance(true).WithSessionNotice(adapters.FinalizeNotice(s.env.App, notice, s.svc))
	if err := s.finalizeNotify(att, s.agent, conv, attendancedomain.ReasonResolved, nil); err != nil {
		t.Fatal(err)
	}
	rows := s.sessionsOf(conv)
	if len(rows) != 1 || rows[0].status != "closed" || rows[0].closedSource != "finalize" {
		t.Fatalf("an attendance that never had a protocol gets one when it ends, and it is closed: %+v", rows)
	}
	got := s.told(conv)
	if len(got) != 1 || !strings.Contains(got[0], "Seu atendimento foi encerrado") || !strings.Contains(got[0], rows[0].protocol) {
		t.Fatalf("the customer is told the number that was closed, once: %q (protocol %s)", got, rows[0].protocol)
	}
	// a replay opens no second protocol and tells nobody again
	if err := s.finalizeNotify(att, s.agent, conv, attendancedomain.ReasonResolved, nil); err != nil {
		t.Fatal(err)
	}
	if rows := s.sessionsOf(conv); len(rows) != 1 || len(s.told(conv)) != 1 {
		t.Fatalf("a replay: %+v %q", rows, s.told(conv))
	}
}

func TestTheProtocolIsNotOptionalButTellingTheNumberIs(t *testing.T) {
	s := newStack(t)
	notice := s.announce()
	att := s.attendance(true).WithSessionNotice(adapters.FinalizeNotice(s.env.App, notice, s.svc))
	no := false
	// (a) opened at the claim, finalized with notify_protocol=false: the protocol closes, the customer is told only the opening
	conv, _ := s.conversation()
	s.withLine(conv, "waha", "unofficial")
	s.assign(conv, s.agent)
	sess := s.mustOpen(s.agent, conv)
	if err := s.finalizeNotify(att, s.agent, conv, attendancedomain.ReasonResolved, &no); err != nil {
		t.Fatal(err)
	}
	if rows := s.sessionsOf(conv); len(rows) != 1 || rows[0].status != "closed" || rows[0].protocol != sess.Protocol {
		t.Fatalf("(a) the protocol is not optional: %+v", rows)
	}
	if got := s.told(conv); len(got) != 1 || strings.Contains(got[0], "foi encerrado") {
		t.Fatalf("(a) only the opening was told: %q", got)
	}
	// (b) never assumed, notify_protocol=false: the protocol is still created and closed, the customer is told nothing
	conv2, _ := s.conversation()
	s.withLine(conv2, "waha", "unofficial")
	s.assign(conv2, s.agent)
	if err := s.finalizeNotify(att, s.agent, conv2, attendancedomain.ReasonResolved, &no); err != nil {
		t.Fatal(err)
	}
	if rows := s.sessionsOf(conv2); len(rows) != 1 || rows[0].status != "closed" {
		t.Fatalf("(b) every attendance ends with a protocol, told or not: %+v", rows)
	}
	if got := s.told(conv2); len(got) != 0 {
		t.Fatalf("(b) not told: %q", got)
	}
	// (c) spam: the protocol is kept for tracing, but nobody is told an "attendance" ended
	conv3, _ := s.conversation()
	s.withLine(conv3, "waha", "unofficial")
	s.assign(conv3, s.agent)
	if err := s.finalizeNotify(att, s.agent, conv3, attendancedomain.ReasonSpam, nil); err != nil {
		t.Fatal(err)
	}
	if rows := s.sessionsOf(conv3); len(rows) != 1 || rows[0].status == "open" {
		t.Fatalf("(c) spam still ends with a protocol: %+v", rows)
	}
	if got := s.told(conv3); len(got) != 0 {
		t.Fatalf("(c) a spam contact is told nothing: %q", got)
	}
	// (c2) spam on an attendance that DID open a protocol: the opening was told, the ending is not
	conv3b, _ := s.conversation()
	s.withLine(conv3b, "waha", "unofficial")
	s.assign(conv3b, s.agent)
	s.mustOpen(s.agent, conv3b)
	if err := s.finalizeNotify(att, s.agent, conv3b, attendancedomain.ReasonDuplicate, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.told(conv3b); len(got) != 1 || strings.Contains(got[0], "foi encerrado") {
		t.Fatalf("(c2) a duplicate with an active protocol is told no ending: %q", got)
	}
	if rows := s.sessionsOf(conv3b); len(rows) != 1 || rows[0].status == "open" {
		t.Fatalf("(c2) the protocol still ends: %+v", rows)
	}
	// (d) the company's own switch OFF beats the person's wish: nothing is sent, the protocol still ends
	s.switchNotice(s.env.TenantA, false)
	conv4, _ := s.conversation()
	s.withLine(conv4, "waha", "unofficial")
	s.assign(conv4, s.agent)
	if err := s.finalizeNotify(att, s.agent, conv4, attendancedomain.ReasonResolved, nil); err != nil {
		t.Fatal(err)
	}
	if rows := s.sessionsOf(conv4); len(rows) != 1 || rows[0].status != "closed" || len(s.told(conv4)) != 0 {
		t.Fatalf("(d) switch off: %+v %q", rows, s.told(conv4))
	}
}

// panickingAnnouncer stands for a bug in the courtesy: it must cost the customer a notice, never the protocol.
type panickingAnnouncer struct{}

func (panickingAnnouncer) Opened(context.Context, ports.Announcement) error { panic("boom") }
func (panickingAnnouncer) Closed(context.Context, ports.Announcement) error { panic("boom") }

func TestAPanicInTheNoticeCostsTheNoticeNotTheProtocol(t *testing.T) {
	s := newStack(t)
	s.svc.WithAnnouncer(panickingAnnouncer{})
	conv, _ := s.conversation()
	s.assign(conv, s.agent)
	sess := s.mustOpen(s.agent, conv) // does not panic, does not roll back
	if rows := s.sessionsOf(conv); len(rows) != 1 || rows[0].status != "open" {
		t.Fatalf("the protocol stands after a panic in the notice: %+v", rows)
	}
	if _, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResInformationProvided}, sess.Version); err != nil {
		t.Fatalf("closing after a panic in the notice: %v", err)
	}
	if rows := s.sessionsOf(conv); len(rows) != 1 || rows[0].status != "closed" {
		t.Fatalf("the protocol closed: %+v", rows)
	}
}

func TestReopeningAndClosingAgainRepeatsNoNotice(t *testing.T) {
	s := newStack(t)
	s.announce()
	conv, _ := s.conversation()
	s.withLine(conv, "waha", "unofficial")
	s.assign(conv, s.agent)
	sess := s.mustOpen(s.agent, conv)
	closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResInformationProvided}, sess.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.told(conv); len(got) != 2 {
		t.Fatalf("opened and closed once each: %q", got)
	}
	reopened, err := s.reopen(s.svc, s.agent, sess.ID, closed.Version)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResInformationProvided}, reopened.Version); err != nil {
		t.Fatal(err)
	}
	if got := s.told(conv); len(got) != 2 {
		t.Fatalf("the same protocol closing twice is told once: %q", got)
	}
}
