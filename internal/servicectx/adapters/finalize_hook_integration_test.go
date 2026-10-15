package adapters_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	attendanceadapters "github.com/omnira/omnira/internal/attendance/adapters"
	attendanceapplication "github.com/omnira/omnira/internal/attendance/application"
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	"github.com/omnira/omnira/internal/servicectx/adapters"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

// attendance builds the ADR-0020 service the way the API does, with the service-context hook when wanted.
func (s *stack) attendance(hook bool) *attendanceapplication.Service {
	svc := attendanceapplication.NewService(attendanceadapters.NewPostgresRepository(s.env.App), attendanceadapters.NewAuthorizer(s.env.App),
		attendanceadapters.NewAuditor(auditadapters.NewPostgresAuditEventRepository(s.env.App)))
	if hook {
		svc.WithSessionCloser(adapters.FinalizeHook(s.env.App, s.svc))
	}
	return svc
}

func (s *stack) finalize(att *attendanceapplication.Service, user, conv uuid.UUID, reason attendancedomain.CloseReason) error {
	return s.try(user, s.env.TenantA, func(ctx context.Context) error {
		_, err := att.Finalize(ctx, attendancedomain.FinalizeInput{ConversationID: conv, Reason: reason, Summary: "Cliente tinha o link fora; voltou.", Note: "ok"})
		return err
	})
}

type sessionRow struct {
	protocol, status, canonical, openedSource, closedSource string
	openedBy                                                *uuid.UUID
}

func (s *stack) sessionsOf(conv uuid.UUID) (out []sessionRow) {
	s.t.Helper()
	rows, err := s.env.Seed.Query(context.Background(), `SELECT protocol, status, COALESCE(resolution_canonical,''), opened_source, COALESCE(closed_source,''), opened_by_user_id
		FROM service_sessions WHERE conversation_id=$1 ORDER BY opened_at`, conv)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r sessionRow
		if err := rows.Scan(&r.protocol, &r.status, &r.canonical, &r.openedSource, &r.closedSource, &r.openedBy); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (s *stack) assign(conv, user uuid.UUID) {
	s.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, conv, user)
}

// An attendance that only gave information gets its protocol when it ends, in the same transaction that closes the conversation (Gate 1).
func TestFinalizeGivesAnInformationalAttendanceItsProtocolAndClosesIt(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	s.assign(conv, s.agent)
	if err := s.finalize(s.attendance(true), s.agent, conv, attendancedomain.ReasonResolved); err != nil {
		t.Fatal(err)
	}
	rows := s.sessionsOf(conv)
	if len(rows) != 1 || rows[0].protocol != s.proto(s.env.TenantA, 1001) || rows[0].status != "closed" || rows[0].canonical != domain.ResInformationProvided ||
		rows[0].openedSource != "finalize" || rows[0].closedSource != "finalize" || rows[0].openedBy == nil || *rows[0].openedBy != s.agent {
		t.Fatalf("session after finalize: %+v", rows)
	}
	if n := s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='closed'`, conv); n != 1 {
		t.Fatal("the conversation is closed as before")
	}
	// idempotent: finalizing again changes nothing and creates no second session
	if err := s.finalize(s.attendance(true), s.agent, conv, attendancedomain.ReasonResolved); err != nil {
		t.Fatal(err)
	}
	if len(s.sessionsOf(conv)) != 1 {
		t.Fatal("a replayed finalize opens no new session")
	}
}

func TestFinalizeClosesTheSessionThatWasAlreadyOpenInsteadOfOpeningAnother(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	s.assign(conv, s.agent)
	opened := s.mustOpen(s.agent, conv)
	if err := s.finalize(s.attendance(true), s.agent, conv, attendancedomain.ReasonNoResponse); err != nil {
		t.Fatal(err)
	}
	rows := s.sessionsOf(conv)
	if len(rows) != 1 || rows[0].protocol != opened.Protocol || rows[0].status != "abandoned" || rows[0].canonical != domain.ResAbandoned || rows[0].openedSource != "operator" {
		t.Fatalf("the open session is the one that ends, abandoned when the customer went silent: %+v", rows)
	}
}

func TestFinalizeWithoutTheHookIsExactlyAsBefore(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	s.assign(conv, s.agent)
	if err := s.finalize(s.attendance(false), s.agent, conv, attendancedomain.ReasonResolved); err != nil {
		t.Fatal(err)
	}
	if len(s.sessionsOf(conv)) != 0 {
		t.Fatal("the module is off: no session")
	}
	if n := s.count(`SELECT count(*) FROM conversation_closures WHERE conversation_id=$1`, conv); n != 1 {
		t.Fatal("the closure is written")
	}
}

// Ending the attendance never waits on the optional module: a person without the service keys still finalizes, and the failure leaves nothing behind.
func TestAFailingHookNeverBlocksFinalizing(t *testing.T) {
	s := newStack(t)
	role := uuid.New()
	s.exec(`INSERT INTO roles(id, tenant_id, key, name) VALUES($1,$2,'claim-only','Claim only')`, role, s.env.TenantA)
	s.exec(`INSERT INTO role_permissions(role_id, permission_key) VALUES($1,'conversation.claim')`, role)
	user := uuid.New()
	s.exec(`INSERT INTO users(id, external_subject, email, status) VALUES($1,$3,$2,'active')`, user, user.String()+"@invalid", user.String())
	s.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, s.env.TenantA, user, role)
	t.Cleanup(func() { _, _ = s.env.Seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user) })
	conv, _ := s.conversation()
	s.assign(conv, user)
	if err := s.finalize(s.attendance(true), user, conv, attendancedomain.ReasonResolved); err != nil {
		t.Fatalf("finalizing must succeed even when the module refuses (no service.create): %v", err)
	}
	if n := s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='closed'`, conv); n != 1 {
		t.Fatal("the conversation is closed")
	}
	if n := s.count(`SELECT count(*) FROM conversation_closures WHERE conversation_id=$1`, conv); n != 1 {
		t.Fatal("the closure is written")
	}
	if len(s.sessionsOf(conv)) != 0 {
		t.Fatal("the refused attempt leaves no session behind (rolled back to its savepoint)")
	}
}

// The flow engine finalizes in a system session (source "flow"); a Hub agent finalizes through the Hub's write path, also in a system session, on behalf of a person.
func TestFinalizeBySystemAndForAHubAgentClosesTheSessionToo(t *testing.T) {
	s := newStack(t)
	att := s.attendance(true)

	flowConv, _ := s.conversation()
	s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) {
		if _, err := att.FinalizeBySystemWithReason(ctx, flowConv, attendancedomain.ReasonNoResponse, "silêncio"); err != nil {
			t.Fatal(err)
		}
	})
	rows := s.sessionsOf(flowConv)
	if len(rows) != 1 || rows[0].status != "abandoned" || rows[0].openedSource != "flow" || rows[0].closedSource != "flow" || rows[0].openedBy != nil {
		t.Fatalf("flow: %+v", rows)
	}

	hubConv, _ := s.conversation()
	s.assign(hubConv, s.agent)
	s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) {
		if _, err := att.FinalizeAsHubAgent(ctx, s.agent, attendancedomain.FinalizeInput{ConversationID: hubConv, Reason: attendancedomain.ReasonResolved, Summary: "ok"}, false); err != nil {
			t.Fatal(err)
		}
	})
	rows = s.sessionsOf(hubConv)
	if len(rows) != 1 || rows[0].status != "closed" || rows[0].openedBy == nil || *rows[0].openedBy != s.agent {
		t.Fatalf("hub agent: %+v", rows)
	}
}
