package adapters_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/servicectx/adapters"
)

// ADR-0048 + ADR-0049: a CRM that finalizes a conversation it holds ends the attendance like a person does: the protocol ends with it and the customer is told the
// number (unless the application opts out). The notice is the application's own message, queued at the control version it holds.

// held makes an installation the holder of the conversation (the canonical control write: version + 1, no human assignee) and returns the installation and version.
func (s *stack) held(conv uuid.UUID) (uuid.UUID, int64) {
	s.t.Helper()
	inst := uuid.New()
	s.exec(`INSERT INTO external_app_installations(id,tenant_id,name,scopes) VALUES($1,$2,$3,ARRAY['conversation.finalize','message.send'])`, inst, s.env.TenantA, "CRM "+inst.String()[:8])
	s.exec(`UPDATE conversations SET assigned_to_user_id=NULL, control_kind='crm_application', control_holder_id=$2, control_version=control_version+1, control_since=now(), control_origin='crm' WHERE id=$1`, conv, inst)
	var v int64
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT control_version FROM conversations WHERE id=$1`, conv).Scan(&v); err != nil {
		s.t.Fatal(err)
	}
	return inst, v
}

func (s *stack) finalizeAsApp(inst, conv uuid.UUID, version int64, notify *bool) error {
	s.t.Helper()
	att := s.attendance(true)
	n := adapters.NewNotice(s.env.App)
	att.WithExternalSessionNotice(adapters.ExternalFinalizeNotice(s.env.App, n, s.svc))
	return platformdb.WithSystemTenantSession(context.Background(), s.env.App, s.env.TenantA, func(ctx context.Context) error {
		_, err := att.FinalizeByExternalApp(ctx, inst, conv, attendancedomain.ReasonResolved, "ok", version, notify)
		return err
	})
}

func TestACrmFinalizeEndsTheProtocolAndTellsTheCustomer(t *testing.T) {
	s := newStack(t)
	s.switchNotice(s.env.TenantA, true)
	conv, _ := s.conversation()
	s.withLine(conv, "waha", "unofficial")
	inst, v := s.held(conv)
	if err := s.finalizeAsApp(inst, conv, v, nil); err != nil {
		t.Fatal(err)
	}
	rows := s.sessionsOf(conv)
	if len(rows) != 1 || rows[0].status != "closed" || rows[0].closedSource != "api" {
		t.Fatalf("the protocol ends with the attendance: %+v", rows)
	}
	got := s.told(conv)
	if len(got) != 1 || !strings.Contains(got[0], "Seu atendimento foi encerrado") || !strings.Contains(got[0], "Protocolo de atendimento: "+rows[0].protocol) {
		t.Fatalf("the customer is told the number: %q", got)
	}
	if n := s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND sent_by_installation_id=$2 AND sent_by_user_id IS NULL`, conv, inst); n != 1 {
		t.Fatalf("the notice is the application's own message: %d", n)
	}
	if n := s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='closed'`, conv); n != 1 {
		t.Fatal("the conversation is closed")
	}
	// the same finalize again changes nothing and tells nobody again
	if err := s.finalizeAsApp(inst, conv, v, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.told(conv)) != 1 || len(s.sessionsOf(conv)) != 1 {
		t.Fatal("a replay neither messages nor opens a second protocol")
	}
}

func TestACrmCanFinalizeWithoutTellingTheProtocol(t *testing.T) {
	s := newStack(t)
	s.switchNotice(s.env.TenantA, true)
	conv, _ := s.conversation()
	s.withLine(conv, "waha", "unofficial")
	inst, v := s.held(conv)
	no := false
	if err := s.finalizeAsApp(inst, conv, v, &no); err != nil {
		t.Fatal(err)
	}
	if got := s.told(conv); len(got) != 0 {
		t.Fatalf("notify_protocol=false tells nobody: %q", got)
	}
	if rows := s.sessionsOf(conv); len(rows) != 1 || rows[0].status != "closed" {
		t.Fatalf("the protocol still ends: %+v", rows)
	}
}

func TestACrmFinalizeNeverWaitsOnTheCourtesy(t *testing.T) {
	s := newStack(t)
	// the company's switch is OFF: nobody is told, the attendance still ends
	off, _ := s.conversation()
	s.withLine(off, "waha", "unofficial")
	instOff, vOff := s.held(off)
	if err := s.finalizeAsApp(instOff, off, vOff, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.told(off); len(got) != 0 {
		t.Fatalf("switch off: %q", got)
	}
	// the switch is ON but the version is stale (the notice cannot be sent as the holder): finalize and the protocol still go through
	s.switchNotice(s.env.TenantA, true)
	stale, _ := s.conversation()
	s.withLine(stale, "waha", "unofficial")
	instStale, vStale := s.held(stale)
	if err := s.finalizeAsApp(instStale, stale, vStale-1, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.told(stale); len(got) != 0 {
		t.Fatalf("a notice that cannot be proven is not sent: %q", got)
	}
	if rows := s.sessionsOf(stale); len(rows) != 1 || rows[0].status != "closed" {
		t.Fatalf("the protocol ends anyway: %+v", rows)
	}
	if n := s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='closed'`, stale); n != 1 {
		t.Fatal("the conversation is closed")
	}
}
