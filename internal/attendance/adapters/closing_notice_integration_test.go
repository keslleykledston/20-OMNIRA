package adapters_test

// ADR-0040 phase 08: the closing notice for the customer is queued BY THE SERVER inside the finalize transaction (member path). On a real PostgreSQL with the
// application's role, through the production wiring of the notice (httpserver.ClosingNoticeSenders). What must hold: the message and its delivery job commit with
// the closing and never without it; a refusal of the courtesy (closed 24 h window, no line, someone who may not write) never stops the finalize; a replay never
// messages twice; the audit names the state, never the text.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/attendance/application"
	"github.com/omnira/omnira/internal/attendance/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/httpserver"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func (s *stack) withNotice() {
	member, agent := httpserver.ClosingNoticeSenders(s.env.App)
	s.svc.WithClosingNotice(member, agent)
}

func (s *stack) channel(conv uuid.UUID, provider string) {
	s.t.Helper()
	conn := uuid.New()
	kind := "unofficial"
	if provider == "meta_cloud" {
		kind = "official"
	}
	s.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
	        VALUES($1,$2,'whatsapp',$3,$4,$5,'active','["text"]')`, conn, s.env.TenantA, provider, kind, conn.String())
	s.exec(`UPDATE conversations SET channel_connection_id=$2 WHERE id=$1`, conv, conn)
}

func (s *stack) queuedNotices(conv uuid.UUID) int {
	return s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound' AND idempotency_key=$2`, conv, "farewell-"+conv.String())
}

func TestTheClosingNoticeIsQueuedWithTheClosingAndNeverWithoutIt(t *testing.T) {
	s := newStack(t)
	s.withNotice()
	const text = "Atendimento encerrado, obrigado pelo contato!"

	finalize := func(user uuid.UUID, conv uuid.UUID, farewell string) (res application.FinalizeResult, err error) {
		in := finalizeInput(conv)
		in.Farewell = farewell
		s.as(user, s.env.TenantA, func(ctx context.Context) { res, err = s.svc.Finalize(ctx, in) })
		return
	}

	t.Run("the holder asks for the notice: one outbound message, its delivery job, and the closing, together", func(t *testing.T) {
		conv, _ := s.conversation(&s.agent)
		s.channel(conv, "waha")
		res, err := finalize(s.agent, conv, text)
		if err != nil || !res.Changed || res.Farewell != domain.FarewellQueued {
			t.Fatalf("finalize: %+v err=%v", res, err)
		}
		if n := s.queuedNotices(conv); n != 1 {
			t.Fatalf("queued notices = %d, want 1", n)
		}
		if got := s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound' AND body=$2 AND status='queued' AND sent_by_user_id=$3`, conv, text, s.agent); got != 1 {
			t.Error("the notice must be the person's own queued outbound text")
		}
		if got := s.count(`SELECT count(*) FROM outbox_events o JOIN messages m ON m.id::text = o.aggregate_id WHERE m.conversation_id=$1 AND o.event_type='job.channel.send_text.v1'`, conv); got != 1 {
			t.Errorf("delivery jobs = %d, want 1 (the worker delivers it)", got)
		}
		if st := s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='closed'`, conv); st != 1 {
			t.Error("the conversation is not closed")
		}
		var meta string
		if err := s.env.Seed.QueryRow(context.Background(), `SELECT metadata::text FROM audit_events WHERE tenant_id=$1 AND action='conversation.closed' AND resource_id=$2`, s.env.TenantA, conv).Scan(&meta); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(meta, `"farewell": "queued"`) && !strings.Contains(meta, `"farewell":"queued"`) {
			t.Errorf("the audit names the state of the notice: %s", meta)
		}
		if strings.Contains(meta, "obrigado") {
			t.Errorf("the text of the notice reached the audit: %s", meta)
		}
	})

	t.Run("a replay of the finalize never messages the customer twice", func(t *testing.T) {
		conv, _ := s.conversation(&s.agent)
		s.channel(conv, "waha")
		if _, err := finalize(s.agent, conv, text); err != nil {
			t.Fatal(err)
		}
		res, err := finalize(s.agent, conv, text)
		if err != nil || res.Changed || res.Farewell != domain.FarewellNotRequested && res.Farewell != "" {
			t.Fatalf("replay: %+v err=%v", res, err)
		}
		if n := s.queuedNotices(conv); n != 1 {
			t.Fatalf("notices after a replay = %d, want 1", n)
		}
	})

	t.Run("no notice asked: nothing is queued and the state says so", func(t *testing.T) {
		conv, _ := s.conversation(&s.agent)
		s.channel(conv, "waha")
		res, err := finalize(s.agent, conv, "")
		if err != nil || res.Farewell != domain.FarewellNotRequested {
			t.Fatalf("finalize: %+v err=%v", res, err)
		}
		if n := s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, conv); n != 0 {
			t.Fatalf("outbound messages = %d, want 0", n)
		}
	})

	t.Run("a closed 24 h window or a missing line refuses the courtesy, not the finalize", func(t *testing.T) {
		official, _ := s.conversation(&s.agent)
		s.channel(official, "meta_cloud") // the customer never wrote: the window is closed
		res, err := finalize(s.agent, official, text)
		if err != nil || !res.Changed || res.Farewell != domain.FarewellWindowClosed {
			t.Fatalf("window closed: %+v err=%v", res, err)
		}
		bare, _ := s.conversation(&s.agent) // no channel connection at all
		res, err = finalize(s.agent, bare, text)
		if err != nil || !res.Changed || res.Farewell != domain.FarewellNoChannel {
			t.Fatalf("no line: %+v err=%v", res, err)
		}
		for _, c := range []uuid.UUID{official, bare} {
			if n := s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, c); n != 0 {
				t.Errorf("a refused notice left %d outbound message(s)", n)
			}
			if n := s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='closed'`, c); n != 1 {
				t.Error("the finalize was stopped by the refused courtesy")
			}
		}
	})

	t.Run("a supervisor who does not hold it may finalize and notify; an unassigned one cannot be notified", func(t *testing.T) {
		held, _ := s.conversation(&s.agent)
		s.channel(held, "waha")
		var res application.FinalizeResult
		var err error
		in := finalizeInput(held)
		in.Farewell = text
		s.as(s.env.UserA, s.env.TenantA, func(ctx context.Context) { res, err = s.svc.Finalize(ctx, in) }) // the seeded admin has conversation.manage
		if err != nil || res.Farewell != domain.FarewellQueued {
			t.Fatalf("supervisor: %+v err=%v", res, err)
		}
		free, _ := s.conversation(nil)
		s.channel(free, "waha")
		in = finalizeInput(free)
		in.Farewell = text
		s.as(s.env.UserA, s.env.TenantA, func(ctx context.Context) { res, err = s.svc.Finalize(ctx, in) })
		if err != nil || !res.Changed || res.Farewell != domain.FarewellNotAllowed {
			t.Fatalf("unassigned: %+v err=%v", res, err)
		}
		if n := s.queuedNotices(free); n != 0 {
			t.Errorf("notices on an unassigned conversation = %d, want 0", n)
		}
	})

	t.Run("a failure AFTER the notice was queued rolls everything back: no message, conversation still open", func(t *testing.T) {
		conv, _ := s.conversation(&s.agent)
		s.channel(conv, "waha")
		s.exec(`CREATE OR REPLACE FUNCTION zz_boom() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$`)
		s.exec(`CREATE TRIGGER zz_boom BEFORE INSERT ON conversation_closures FOR EACH ROW EXECUTE FUNCTION zz_boom()`)
		defer func() {
			s.exec(`DROP TRIGGER IF EXISTS zz_boom ON conversation_closures`)
			s.exec(`DROP FUNCTION IF EXISTS zz_boom()`)
		}()
		// the request's transaction as the middleware runs it: a failed handler rolls it back (a statement error would abort it anyway)
		in := finalizeInput(conv)
		in.Farewell = text
		failed := platformdb.WithTenantSession(context.Background(), s.env.App, s.agent, false, func(sc context.Context) error {
			tc, err := tenancydomain.NewTenantContext(s.env.TenantA, s.agent, tenancydomain.AccessSourceDirect)
			if err != nil {
				return err
			}
			_, err = s.svc.Finalize(tenancydomain.WithTenantContext(sc, tc), in)
			return err
		})
		if failed == nil {
			t.Fatal("the finalize was expected to fail")
		}
		if n := s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, conv); n != 0 {
			t.Fatalf("a notice outlived the failed finalize: %d message(s)", n)
		}
		if n := s.count(`SELECT count(*) FROM outbox_events o JOIN messages m ON m.id::text = o.aggregate_id WHERE m.conversation_id=$1`, conv); n != 0 {
			t.Fatalf("a delivery job outlived the failed finalize: %d", n)
		}
		if n := s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='open'`, conv); n != 1 {
			t.Error("the conversation was closed by a failed finalize")
		}
	})

	t.Run("a key already used in the conversation (by anyone) gets no second message and the notice is not reported as queued", func(t *testing.T) {
		conv, _ := s.conversation(&s.agent)
		s.channel(conv, "waha")
		// another person used the notice's key in this conversation: the idempotency index is per sender, so only a check across senders sees it
		s.exec(`INSERT INTO messages(id, tenant_id, conversation_id, direction, message_type, body, status, idempotency_key, request_hash, sent_by_user_id)
		        VALUES($1,$2,$3,'outbound','text','outro texto','failed',$4,'not-the-hash',$5)`, uuid.New(), s.env.TenantA, conv, "farewell-"+conv.String(), s.observer)
		res, err := finalize(s.agent, conv, text)
		if err != nil || !res.Changed || res.Farewell != domain.FarewellUnavailable {
			t.Fatalf("finalize: %+v err=%v, want it to go on with the state unavailable", res, err)
		}
		if n := s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, conv); n != 1 {
			t.Errorf("outbound messages = %d, want only the earlier one", n)
		}
	})

	t.Run("a notice that looks like it carries a credential, or is too long, is refused and nothing changes", func(t *testing.T) {
		conv, _ := s.conversation(&s.agent)
		s.channel(conv, "waha")
		for name, f := range map[string]string{
			"a credential": "use Bearer abcdefgh12345678 para entrar",
			"too long":     strings.Repeat("a", domain.MaxFarewell+1),
		} {
			if _, err := finalize(s.agent, conv, f); err == nil {
				t.Errorf("%s: want an invalid-input error", name)
			}
		}
		if n := s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='open'`, conv); n != 1 || s.queuedNotices(conv) != 0 {
			t.Error("a refused notice changed something")
		}
	})

	t.Run("somebody who may not even finalize learns nothing and queues nothing", func(t *testing.T) {
		conv, _ := s.conversation(&s.agent)
		s.channel(conv, "waha")
		_, err := finalize(s.observer, conv, text) // neither claim nor manage
		if err == nil {
			t.Fatal("want a refusal")
		}
		if s.queuedNotices(conv) != 0 {
			t.Error("a refused finalize queued the notice")
		}
	})
}
