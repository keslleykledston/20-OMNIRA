package adapters_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	"github.com/omnira/omnira/internal/platform/claimhook"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
	routingapplication "github.com/omnira/omnira/internal/routing/application"
	"github.com/omnira/omnira/internal/servicectx/adapters"
)

// Assuming a conversation opens its protocol (owner's decision, 2026-10-15; OMNIRA_SERVICE_AUTO_OPEN_ON_CLAIM_ENABLED). Taking the conversation never depends
// on the module: a failure, a missing key or a flag that is off leave the claim exactly as it was.

func (s *stack) assigner(hook *claimhook.Hook) *routingapplication.Assigner {
	a := routingapplication.NewAssigner(routingadapters.NewPostgresConversationAssigner(s.env.App),
		routingadapters.NewAuditRecorder(auditadapters.NewPostgresAuditEventRepository(s.env.App)))
	if hook != nil {
		a.WithClaimHook(hook)
	}
	return a
}

// agentProfile makes the member an active agent (what self-claim asks for).
func (s *stack) agentProfile(user uuid.UUID) {
	s.t.Helper()
	s.exec(`INSERT INTO agent_profiles(tenant_id, membership_id) SELECT tenant_id, id FROM memberships WHERE tenant_id = $1 AND user_id = $2 ON CONFLICT DO NOTHING`, s.env.TenantA, user)
}

func (s *stack) claim(a *routingapplication.Assigner, user, conv uuid.UUID) (routingapplication.AssignResult, error) {
	var res routingapplication.AssignResult
	err := s.try(user, s.env.TenantA, func(ctx context.Context) (e error) {
		res, e = a.Assign(ctx, conv, uuid.Nil)
		return
	})
	return res, err
}

func (s *stack) installedHook() *claimhook.Hook {
	h := &claimhook.Hook{}
	h.Set(adapters.ClaimHook(s.env.App, s.svc))
	return h
}

func TestAMemberWhoAssumesAConversationGetsItsProtocol(t *testing.T) {
	s := newStack(t)
	s.agentProfile(s.agent)
	a := s.assigner(s.installedHook())
	conv, _ := s.conversation()
	res, err := s.claim(a, s.agent, conv)
	if err != nil || !res.Changed {
		t.Fatalf("claim: %v %+v", err, res)
	}
	rows := s.sessionsOf(conv)
	if len(rows) != 1 || rows[0].protocol != s.proto(s.env.TenantA, 1001) || rows[0].status != "open" || rows[0].openedSource != "operator" || rows[0].openedBy == nil || *rows[0].openedBy != s.agent {
		t.Fatalf("the assumed conversation has its protocol, opened by the person: %+v", rows)
	}
	// the same person assuming again changes nothing: still one protocol
	if res, err = s.claim(a, s.agent, conv); err != nil || res.Changed {
		t.Fatalf("repeat: %v %+v", err, res)
	}
	if rows = s.sessionsOf(conv); len(rows) != 1 {
		t.Fatalf("an idempotent claim opens nothing more: %+v", rows)
	}
	// the next conversation gets the next number
	conv2, _ := s.conversation()
	if _, err := s.claim(a, s.agent, conv2); err != nil {
		t.Fatal(err)
	}
	if rows = s.sessionsOf(conv2); len(rows) != 1 || rows[0].protocol != s.proto(s.env.TenantA, 1002) {
		t.Fatalf("second: %+v", rows)
	}
	if n := s.count(`SELECT count(*) FROM context_events WHERE conversation_id = $1 AND event_type = 'service_session.opened' AND actor_id = $2`, conv, s.agent); n != 1 {
		t.Fatalf("the timeline says who opened it: %d", n)
	}
}

func TestAConversationAlreadyHoldingAProtocolKeepsIt(t *testing.T) {
	s := newStack(t)
	s.agentProfile(s.agent)
	conv, _ := s.conversation()
	existing := s.mustOpen(s.agent, conv) // opened by hand before the claim
	if _, err := s.claim(s.assigner(s.installedHook()), s.agent, conv); err != nil {
		t.Fatal(err)
	}
	rows := s.sessionsOf(conv)
	if len(rows) != 1 || rows[0].protocol != existing.Protocol {
		t.Fatalf("the standing session is not duplicated: %+v", rows)
	}
}

func TestWithoutTheHookOrTheKeyClaimingStillWorksAndOpensNothing(t *testing.T) {
	s := newStack(t)
	s.agentProfile(s.agent)
	conv, _ := s.conversation()
	if res, err := s.claim(s.assigner(nil), s.agent, conv); err != nil || !res.Changed {
		t.Fatalf("flag off: %v %+v", err, res)
	}
	if rows := s.sessionsOf(conv); len(rows) != 0 {
		t.Fatalf("the hook is not installed: %+v", rows)
	}
	// a hook installed but empty (flag off in main) does nothing too
	conv2, _ := s.conversation()
	if res, err := s.claim(s.assigner(&claimhook.Hook{}), s.agent, conv2); err != nil || !res.Changed || len(s.sessionsOf(conv2)) != 0 {
		t.Fatalf("empty hook: %v %+v", err, res)
	}
	// a member who may claim but not open a service session (no service.create) just claims
	claimer := uuid.New()
	role := uuid.New()
	s.exec(`INSERT INTO roles(id, tenant_id, key, name) VALUES($1,$2,'claim-only','Claim only')`, role, s.env.TenantA)
	s.exec(`INSERT INTO role_permissions(role_id, permission_key) VALUES($1,'conversation.claim')`, role)
	s.exec(`INSERT INTO users(id, external_subject, email, status) VALUES($1,$2,$3,'active')`, claimer, claimer.String(), claimer.String()+"@invalid")
	s.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, s.env.TenantA, claimer, role)
	s.agentProfile(claimer)
	t.Cleanup(func() { _, _ = s.env.Seed.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, claimer) }) // best effort, like the stack: tenants go first
	conv3, _ := s.conversation()
	if res, err := s.claim(s.assigner(s.installedHook()), claimer, conv3); err != nil || !res.Changed {
		t.Fatalf("no service.create: %v %+v", err, res)
	}
	if rows := s.sessionsOf(conv3); len(rows) != 0 {
		t.Fatalf("without the key there is no protocol, and no error: %+v", rows)
	}
}

func TestAFailingHookNeverUndoesTheClaim(t *testing.T) {
	s := newStack(t)
	s.agentProfile(s.agent)
	h := &claimhook.Hook{}
	h.Set(func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
		return false, errors.New("the module is down")
	})
	conv, _ := s.conversation()
	if res, err := s.claim(s.assigner(h), s.agent, conv); err != nil || !res.Changed {
		t.Fatalf("claim: %v %+v", err, res)
	}
	var holder *uuid.UUID
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT assigned_to_user_id FROM conversations WHERE id = $1`, conv).Scan(&holder); err != nil || holder == nil || *holder != s.agent {
		t.Fatalf("the conversation is the agent's: %v %v", err, holder)
	}
}

func TestAManagerAssigningToSomeoneElseDoesNotOpenTheProtocol(t *testing.T) {
	s := newStack(t)
	s.agentProfile(s.agent)
	conv, _ := s.conversation()
	a := s.assigner(s.installedHook())
	err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		_, e := a.Assign(ctx, conv, s.agent)
		return e
	})
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if rows := s.sessionsOf(conv); len(rows) != 0 {
		t.Fatalf("only the person who assumes it opens the attendance: %+v", rows)
	}
}
