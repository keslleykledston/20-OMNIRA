package adapters_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/attendance/adapters"
	"github.com/omnira/omnira/internal/attendance/application"
	"github.com/omnira/omnira/internal/attendance/domain"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/flows/flowstest"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	inboxapplication "github.com/omnira/omnira/internal/inbox/application"
)

// stack is a real Postgres (disposable DB) with two tenants. Tenant A also has an attendant (conversation.claim only) and a
// member whose role carries neither claim nor manage; the seeded admin has both.
type stack struct {
	t               *testing.T
	env             *flowstest.Env
	svc             *application.Service
	agent, observer uuid.UUID
}

func newStack(t *testing.T) *stack {
	t.Helper()
	env := flowstest.New(t)
	s := &stack{t: t, env: env, agent: uuid.New(), observer: uuid.New()}
	s.svc = application.NewService(adapters.NewPostgresRepository(env.App), adapters.NewAuthorizer(env.App),
		adapters.NewAuditor(auditadapters.NewPostgresAuditEventRepository(env.App)))
	ctx := context.Background()
	var agentRole, observerRole uuid.UUID
	if err := env.Seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL LIMIT 1`).Scan(&agentRole); err != nil {
		t.Fatal(err)
	}
	observerRole = uuid.New()
	s.exec(`INSERT INTO roles(id, tenant_id, key, name) VALUES($1,$2,'observer-test','Observer')`, observerRole, env.TenantA)
	for u, role := range map[uuid.UUID]uuid.UUID{s.agent: agentRole, s.observer: observerRole} {
		s.exec(`INSERT INTO users(id, external_subject, email, status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
		s.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, env.TenantA, u, role)
	}
	t.Cleanup(func() { // tenants cascade; users are global
		_, _ = env.Seed.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2)`, s.agent, s.observer)
	})
	return s
}

func (s *stack) exec(sql string, args ...any) {
	s.t.Helper()
	if _, err := s.env.Seed.Exec(context.Background(), sql, args...); err != nil {
		s.t.Fatalf("%v\n%s", err, sql)
	}
}

func (s *stack) count(sql string, args ...any) (n int) {
	s.t.Helper()
	if err := s.env.Seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

// conversation seeds an open conversation of tenant A, optionally assigned to a user.
func (s *stack) conversation(assignee *uuid.UUID) (conv, contact uuid.UUID) {
	s.t.Helper()
	conv, contact = s.env.SeedConversation(s.t, s.env.TenantA, "none")
	if assignee != nil {
		s.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, conv, *assignee)
	}
	return conv, contact
}

func (s *stack) ticket(conv uuid.UUID, external, topicScoped bool) uuid.UUID {
	s.t.Helper()
	id := uuid.New()
	var ext *string
	if external {
		v := "ERP-" + id.String()[:8]
		ext = &v
	}
	s.exec(`INSERT INTO tickets(id, tenant_id, conversation_id, status, priority, subject, external_ticket_id, topic_scoped, provider)
	        VALUES($1,$2,$3,'open','medium','',$4,$5,$6)`, id, s.env.TenantA, conv, ext, topicScoped, map[bool]any{true: "erp", false: nil}[external])
	return id
}

func (s *stack) as(user uuid.UUID, tenant uuid.UUID, fn func(ctx context.Context)) {
	s.env.AsUser(s.t, tenant, user, fn)
}

func finalizeInput(conv uuid.UUID) domain.FinalizeInput {
	return domain.FinalizeInput{ConversationID: conv, Reason: domain.ReasonResolved, Summary: "Cliente tinha o link fora; reiniciamos a ONU e voltou.",
		Note: "resolvido no primeiro contato"}
}

func TestFinalizeClosesTheConversationItsLocalTicketsAndRecordsWhatIsPending(t *testing.T) {
	s := newStack(t)
	conv, contact := s.conversation(&s.agent)
	local := s.ticket(conv, false, false) // at most ONE active non-topic ticket per conversation (tickets_active_conversation_uq)
	topic := s.ticket(conv, false, true)
	s.exec(`UPDATE conversations SET automation_mode='bot' WHERE id=$1`, conv) // even a bot-held one ends clean

	in := finalizeInput(conv)
	due := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	in.FollowUps = []domain.FollowUpInput{
		{Kind: domain.KindPromise, Text: "Ligar amanhã com o resultado da visita técnica", OwnerUserID: &s.env.UserA, DueAt: &due},
		{Kind: domain.KindPending, Text: "Enviar a segunda via da fatura"},
	}
	var res application.FinalizeResult
	var err error
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) { res, err = s.svc.Finalize(ctx, in) })
	if err != nil || !res.Changed || res.Closure == nil || len(res.FollowUps) != 2 {
		t.Fatalf("finalize: %+v err=%v", res, err)
	}
	if res.Closure.Source != domain.SourceAgent || res.Closure.LocalTicketsClosed != 1 || res.Closure.TicketsKept != 1 || res.Closure.ContactID != contact {
		t.Fatalf("closure: %+v", res.Closure)
	}
	var status, mode string
	var closedAt *time.Time
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT status, automation_mode, closed_at FROM conversations WHERE id=$1`, conv).Scan(&status, &mode, &closedAt); err != nil {
		t.Fatal(err)
	}
	if status != "closed" || mode != "none" || closedAt == nil {
		t.Fatalf("conversation must be closed and released from the bot: %s %s %v", status, mode, closedAt)
	}
	ticketStatus := func(id uuid.UUID) (st string) {
		_ = s.env.Seed.QueryRow(context.Background(), `SELECT status FROM tickets WHERE id=$1`, id).Scan(&st)
		return
	}
	if ticketStatus(local) != "closed" || ticketStatus(topic) != "open" {
		t.Fatalf("only the local, non-topic ticket is closed: local=%s topic=%s", ticketStatus(local), ticketStatus(topic))
	}
	if n := s.count(`SELECT count(*) FROM follow_up_items WHERE conversation_id=$1 AND status='open' AND truth='agent_confirmed'`, conv); n != 2 {
		t.Fatalf("both items are stored open and confirmed by the person: %d", n)
	}
	// audit: who, what, counts; never the summary text
	var meta string
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT metadata::text FROM audit_events WHERE tenant_id=$1 AND action='conversation.closed' AND resource_id=$2`, s.env.TenantA, conv).Scan(&meta); err != nil {
		t.Fatalf("an audit event must exist: %v", err)
	}
	if strings.Contains(meta, "ONU") || !strings.Contains(meta, "resolved") {
		t.Fatalf("audit carries enums and counts only: %s", meta)
	}
}

// An ERP-linked ticket is authoritative there (ADR-0013): finalizing never closes it, and says so.
func TestFinalizeNeverClosesAnErpLinkedTicket(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation(&s.env.UserA)
	erp := s.ticket(conv, true, false)
	var res application.FinalizeResult
	var err error
	s.as(s.env.UserA, s.env.TenantA, func(ctx context.Context) { res, err = s.svc.Finalize(ctx, finalizeInput(conv)) })
	if err != nil || res.Closure.LocalTicketsClosed != 0 || res.Closure.TicketsKept != 1 {
		t.Fatalf("closure must report the ticket it left open: %+v %v", res.Closure, err)
	}
	var st string
	_ = s.env.Seed.QueryRow(context.Background(), `SELECT status FROM tickets WHERE id=$1`, erp).Scan(&st)
	if st != "open" {
		t.Fatalf("the ERP ticket must stay open: %s", st)
	}
}

func TestFinalizeIsIdempotentAndSerializedUnderConcurrency(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation(&s.env.UserA)
	in := finalizeInput(conv)
	in.FollowUps = []domain.FollowUpInput{{Kind: domain.KindPending, Text: "Confirmar a instalação"}}

	var wg sync.WaitGroup
	changed := make(chan bool, 8)
	ids := make(chan uuid.UUID, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.as(s.env.UserA, s.env.TenantA, func(ctx context.Context) {
				res, err := s.svc.Finalize(ctx, in)
				if err != nil {
					t.Errorf("finalize: %v", err)
					return
				}
				changed <- res.Changed
				ids <- res.Closure.ID
			})
		}()
	}
	wg.Wait()
	close(changed)
	close(ids)
	n := 0
	for c := range changed {
		if c {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("exactly one caller finalizes, the rest get the original back: %d", n)
	}
	seen := map[uuid.UUID]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("every caller sees the same closure: %v", seen)
	}
	if s.count(`SELECT count(*) FROM conversation_closures WHERE conversation_id=$1`, conv) != 1 || s.count(`SELECT count(*) FROM follow_up_items WHERE conversation_id=$1`, conv) != 1 {
		t.Fatal("no duplicate closure or follow-up")
	}
	if s.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='conversation.closed' AND resource_id=$2`, s.env.TenantA, conv) != 1 {
		t.Fatal("one audit event")
	}
}

func TestFinalizeAuthorization(t *testing.T) {
	s := newStack(t)
	finalize := func(user uuid.UUID, conv uuid.UUID) (application.FinalizeResult, error) {
		var res application.FinalizeResult
		var err error
		s.as(user, s.env.TenantA, func(ctx context.Context) { res, err = s.svc.Finalize(ctx, finalizeInput(conv)) })
		return res, err
	}
	mine, _ := s.conversation(&s.agent)
	others, _ := s.conversation(&s.env.UserA)
	unassigned, _ := s.conversation(nil)

	if _, err := finalize(s.observer, mine); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a member with neither claim nor manage learns nothing: %v", err)
	}
	if _, err := finalize(s.agent, others); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an attendant cannot finalize somebody else's conversation: %v", err)
	}
	if _, err := finalize(s.agent, unassigned); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("nor an unassigned one (queue or bot): only a supervisor: %v", err)
	}
	if s.count(`SELECT count(*) FROM conversations WHERE status='closed' AND id IN ($1,$2,$3)`, mine, others, unassigned) != 0 {
		t.Fatal("a refused call must change nothing")
	}
	if res, err := finalize(s.agent, mine); err != nil || !res.Changed || res.Closure.Source != domain.SourceAgent {
		t.Fatalf("the owner finalizes their own: %+v %v", res, err)
	}
	if res, err := finalize(s.env.UserA, others); err != nil || res.Closure.Source != domain.SourceAgent {
		t.Fatalf("the admin finalizing their own is an agent action: %+v %v", res, err)
	}
	if res, err := finalize(s.env.UserA, unassigned); err != nil || res.Closure.Source != domain.SourceSupervisor {
		t.Fatalf("the admin finalizing an unassigned one acts as supervisor: %+v %v", res, err)
	}
	if _, err := finalize(s.env.UserA, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown conversation: %v", err)
	}
	// a staff (internal) conversation is never finalized here
	staff := uuid.New()
	s.exec(`INSERT INTO conversations(id, tenant_id, internal_user_id, conversation_kind, automation_mode) VALUES($1,$2,$3,'internal','none')`, staff, s.env.TenantA, s.env.UserA)
	if _, err := finalize(s.env.UserA, staff); !errors.Is(err, domain.ErrNotAContact) {
		t.Fatalf("internal conversation: %v", err)
	}
}

func TestFinalizeRefusesBadInputAndChangesNothing(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation(&s.env.UserA)
	foreignUser := s.env.UserB // member of tenant B only
	cases := map[string]func(*domain.FinalizeInput){
		"unknown reason": func(in *domain.FinalizeInput) { in.Reason = "whatever" },
		"note too long":  func(in *domain.FinalizeInput) { in.Note = strings.Repeat("x", domain.MaxNote+1) },
		"credential": func(in *domain.FinalizeInput) {
			in.Summary = "o cliente passou o token Bearer abcdefghijklmnop1234 por aqui"
		},
		"empty follow-up": func(in *domain.FinalizeInput) {
			in.FollowUps = []domain.FollowUpInput{{Kind: domain.KindPending, Text: "   "}}
		},
		"bad follow-up kind": func(in *domain.FinalizeInput) { in.FollowUps = []domain.FollowUpInput{{Kind: "x", Text: "ok"}} },
		"too many items": func(in *domain.FinalizeInput) {
			for i := 0; i <= domain.MaxFollowUps; i++ {
				in.FollowUps = append(in.FollowUps, domain.FollowUpInput{Kind: domain.KindInfo, Text: fmt.Sprintf("item %d", i)})
			}
		},
		"owner of another tenant": func(in *domain.FinalizeInput) {
			in.FollowUps = []domain.FollowUpInput{{Kind: domain.KindPending, Text: "ok", OwnerUserID: &foreignUser}}
		},
		"bad truth": func(in *domain.FinalizeInput) { in.SummaryTruth = "system_verified" },
	}
	for name, mutate := range cases {
		in := finalizeInput(conv)
		mutate(&in)
		var err error
		s.as(s.env.UserA, s.env.TenantA, func(ctx context.Context) { _, err = s.svc.Finalize(ctx, in) })
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	if s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='open'`, conv) != 1 || s.count(`SELECT count(*) FROM conversation_closures`) != 0 {
		t.Fatal("every refusal leaves the conversation open and writes nothing")
	}
}

func TestAttendanceIsTenantIsolated(t *testing.T) {
	s := newStack(t)
	conv, contact := s.conversation(&s.env.UserA)
	in := finalizeInput(conv)
	in.FollowUps = []domain.FollowUpInput{{Kind: domain.KindPromise, Text: "Retornar a ligação"}}
	var res application.FinalizeResult
	s.as(s.env.UserA, s.env.TenantA, func(ctx context.Context) { res, _ = s.svc.Finalize(ctx, in) })
	if res.Closure == nil {
		t.Fatal("setup: finalize failed")
	}
	// tenant B's admin (holds claim+manage in ITS tenant) cannot touch, read or resolve tenant A's data
	s.as(s.env.UserB, s.env.TenantB, func(ctx context.Context) {
		if _, err := s.svc.Finalize(ctx, finalizeInput(conv)); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("finalize across tenants must look like not found: %v", err)
		}
		if _, err := s.svc.HistoryOfConversation(ctx, conv, 5); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("conversation context across tenants: %v", err)
		}
		h, err := s.svc.HistoryOfContact(ctx, contact, 5)
		if err != nil || len(h.Attendances) != 0 || len(h.OpenFollowUps) != 0 {
			t.Errorf("history across tenants must be empty: %+v %v", h, err)
		}
		if _, err := s.svc.ResolveFollowUp(ctx, res.FollowUps[0].ID, domain.ResolveFollowUpInput{Status: domain.StatusDone}); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("resolve across tenants: %v", err)
		}
	})
	if s.count(`SELECT count(*) FROM follow_up_items WHERE id=$1 AND status='open'`, res.FollowUps[0].ID) != 1 {
		t.Fatal("tenant A's item is untouched")
	}
}

func TestHistoryAndFollowUpLifecycle(t *testing.T) {
	s := newStack(t)
	conv, contact := s.conversation(&s.agent)
	in := finalizeInput(conv)
	soon := time.Now().Add(24 * time.Hour).UTC()
	in.FollowUps = []domain.FollowUpInput{
		{Kind: domain.KindPending, Text: "sem prazo"},
		{Kind: domain.KindPromise, Text: "com prazo", DueAt: &soon},
	}
	var res application.FinalizeResult
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) { res, _ = s.svc.Finalize(ctx, in) })

	// the next attendant of the SAME contact (even in a new conversation) sees the attendance and what is open
	next, _ := s.env.SeedConversation(t, s.env.TenantA, "none")
	s.exec(`UPDATE conversations SET contact_id=$2 WHERE id=$1`, next, contact)
	s.as(s.env.UserA, s.env.TenantA, func(ctx context.Context) {
		h, err := s.svc.HistoryOfConversation(ctx, next, 5)
		if err != nil || h.ContactID != contact || len(h.Attendances) != 1 || len(h.OpenFollowUps) != 2 {
			t.Fatalf("history: %+v %v", h, err)
		}
		if h.OpenFollowUps[0].Text != "com prazo" {
			t.Fatalf("dated items come first: %q", h.OpenFollowUps[0].Text)
		}
		if h.Attendances[0].Closure.Summary == "" || len(h.Attendances[0].FollowUps) != 2 {
			t.Fatalf("the attendance carries its summary and its items: %+v", h.Attendances[0])
		}
	})
	// an observer (no claim/manage) can read but not resolve
	s.as(s.observer, s.env.TenantA, func(ctx context.Context) {
		if _, err := s.svc.HistoryOfContact(ctx, contact, 5); err != nil {
			t.Errorf("any active member reads the history: %v", err)
		}
		if _, err := s.svc.ResolveFollowUp(ctx, res.FollowUps[0].ID, domain.ResolveFollowUpInput{Status: domain.StatusDone}); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("resolve needs claim or manage: %v", err)
		}
	})
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
		item, err := s.svc.ResolveFollowUp(ctx, res.FollowUps[0].ID, domain.ResolveFollowUpInput{Status: domain.StatusDone, Note: "feito"})
		if err != nil || item.Status != domain.StatusDone || item.ResolvedAt == nil {
			t.Fatalf("resolve: %+v %v", item, err)
		}
		if _, err := s.svc.ResolveFollowUp(ctx, res.FollowUps[0].ID, domain.ResolveFollowUpInput{Status: domain.StatusDropped}); !errors.Is(err, domain.ErrAlreadyHandled) {
			t.Errorf("a resolved item cannot be resolved again: %v", err)
		}
		if _, err := s.svc.ResolveFollowUp(ctx, res.FollowUps[1].ID, domain.ResolveFollowUpInput{Status: "open"}); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("only done or dropped: %v", err)
		}
	})
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
		h, _ := s.svc.HistoryOfContact(ctx, contact, 5)
		if len(h.OpenFollowUps) != 1 || h.OpenFollowUps[0].Text != "com prazo" {
			t.Fatalf("only the unresolved item stays open: %+v", h.OpenFollowUps)
		}
	})
}

func TestTheNextMessageAfterFinalizingStartsANewAttendance(t *testing.T) {
	s := newStack(t)
	line := uuid.New()
	s.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`, line, s.env.TenantA, line.String())
	s.exec(`INSERT INTO queues(tenant_id, name, mode, is_default) VALUES($1,'default','manual',true)`, s.env.TenantA)
	phone := fmt.Sprintf("+55119%08d", rand.Intn(100000000))
	store := inboxadapters.NewPostgresInboundStore(s.env.App)
	svc := inboxapplication.NewInboundService(store, store, store, inboxadapters.TicketStore{PostgresInboundStore: store}, store)
	ingest := func(provider string) (conv uuid.UUID, ticketCreated bool) {
		s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) {
			res, err := svc.Ingest(ctx, channeldomain.ChannelConnection{ID: line, TenantID: s.env.TenantA},
				channeldomain.InboundMessage{ConnectionID: line.String(), ProviderMessageID: provider, FromE164: phone, Text: "oi"})
			if err != nil {
				t.Fatal(err)
			}
			conv = res.Conversation.ID
		})
		return conv, true
	}
	first, _ := ingest("wamid-a")
	if again, _ := ingest("wamid-b"); again != first {
		t.Fatal("while the attendance is open, the next message joins the same conversation")
	}
	s.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, first, s.env.UserA)
	var res application.FinalizeResult
	s.as(s.env.UserA, s.env.TenantA, func(ctx context.Context) { res, _ = s.svc.Finalize(ctx, finalizeInput(first)) })
	if res.Closure == nil {
		t.Fatal("setup: finalize failed")
	}
	second, _ := ingest("wamid-c")
	if second == first {
		t.Fatal("after finalizing, the next message must start a NEW conversation")
	}
	if s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='open'`, second) != 1 || s.count(`SELECT count(*) FROM tickets WHERE conversation_id=$1 AND status='open'`, second) != 1 {
		t.Fatal("the new attendance is open and has its own ticket")
	}
	if s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='closed'`, first) != 1 {
		t.Fatal("the finalized conversation stays closed")
	}
	// and the new attendant can see what happened before
	s.as(s.env.UserA, s.env.TenantA, func(ctx context.Context) {
		h, err := s.svc.HistoryOfConversation(ctx, second, 5)
		if err != nil || len(h.Attendances) != 1 || h.Attendances[0].Closure.ConversationID != first {
			t.Fatalf("the new conversation sees the previous attendance: %+v %v", h, err)
		}
	})
}

var _ = pgxpool.New // keep the import used by helpers in other test files of the package

func (s *stack) message(conv uuid.UUID, direction, body string, ago time.Duration) {
	s.t.Helper()
	s.exec(`INSERT INTO messages(id, tenant_id, conversation_id, direction, message_type, body, status, created_at) VALUES($1,$2,$3,$4,'text',$5,'received',$6)`,
		uuid.New(), s.env.TenantA, conv, direction, body, time.Now().Add(-ago))
}

// Earlier messages of the SAME contact only: another contact of the tenant and another tenant never match.
func TestSearchHistoryIsScopedToTheContact(t *testing.T) {
	s := newStack(t)
	current, contact := s.conversation(&s.agent)
	previous, _ := s.env.SeedConversation(t, s.env.TenantA, "none")
	s.exec(`UPDATE conversations SET contact_id=$2, status='closed', closed_at=now() WHERE id=$1`, previous, contact)
	other, _ := s.conversation(&s.agent) // a different contact of the same tenant
	s.message(previous, "inbound", "a fatura de agosto veio com valor errado", 48*time.Hour)
	s.message(previous, "outbound", "vamos conferir a fatura e retornar", 47*time.Hour)
	s.message(previous, "inbound", "o token é Bearer abcdefghijklmnop1234 da fatura", 46*time.Hour)
	s.message(previous, "inbound", "100% certo de que a fatura_2 chegou", 45*time.Hour)
	s.message(current, "inbound", "fatura de novo", time.Hour)
	s.message(other, "inbound", "fatura do OUTRO contato", time.Hour)

	var hits []domain.HistoryHit
	var err error
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) { hits, err = s.svc.SearchHistoryOfConversation(ctx, current, "fatura", 8) })
	if err != nil || len(hits) != 4 {
		t.Fatalf("the contact's earlier messages only, the current conversation left out: %d %v", len(hits), err)
	}
	for _, h := range hits {
		if h.ConversationID != previous || strings.Contains(h.Snippet, "OUTRO") || strings.Contains(h.Snippet, "Bearer") {
			t.Fatalf("scope or credential leak: %+v", h)
		}
	}
	if hits[0].At.Before(hits[len(hits)-1].At) {
		t.Fatal("newest first")
	}
	if !strings.Contains(fmt.Sprint(hits), domain.CredentialOmitted) {
		t.Fatalf("the message with a credential is replaced, not dropped: %+v", hits)
	}
	// % and _ are literal: "100%" matches only the message that really contains it
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) { hits, _ = s.svc.SearchHistoryOfConversation(ctx, current, "100%", 8) })
	if len(hits) != 1 {
		t.Fatalf("wildcards are literal: %d", len(hits))
	}
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) { hits, _ = s.svc.SearchHistoryOfConversation(ctx, current, "tu_a", 8) })
	if len(hits) != 0 {
		t.Fatalf("an underscore is not 'any character': %d", len(hits))
	}
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) { hits, _ = s.svc.SearchHistoryOfConversation(ctx, current, "fatura", 2) })
	if len(hits) != 2 {
		t.Fatalf("limit: %d", len(hits))
	}
	// excluding a topic leaves out the messages already linked to it (they are already in the copilot's context)
	// tenant B cannot search tenant A's contact; an invalid query is refused
	s.as(s.env.UserB, s.env.TenantB, func(ctx context.Context) {
		if _, err := s.svc.SearchHistoryOfConversation(ctx, current, "fatura", 5); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("across tenants must look like not found: %v", err)
		}
	})
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
		if _, err := s.svc.SearchHistoryOfConversation(ctx, current, "a", 5); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("short query: %v", err)
		}
	})
}

// The closure is a record, not a form: the application role can neither update nor delete it, and not even the owner can rewrite it.
func TestClosureRecordsAreAppendOnly(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation(&s.env.UserA)
	var res application.FinalizeResult
	s.as(s.env.UserA, s.env.TenantA, func(ctx context.Context) { res, _ = s.svc.Finalize(ctx, finalizeInput(conv)) })
	if res.Closure == nil {
		t.Fatal("setup: finalize failed")
	}
	deniedToTheApp := func(sql string) {
		t.Helper()
		var err error
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, cerr := s.env.App.Acquire(ctx)
		if cerr != nil {
			t.Fatal(cerr)
		}
		defer conn.Release()
		_, err = conn.Exec(ctx, sql, res.Closure.ID)
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s must be denied to the application role: %v", sql, err)
		}
	}
	deniedToTheApp(`UPDATE conversation_closures SET note='reescrito' WHERE id=$1`)
	deniedToTheApp(`DELETE FROM conversation_closures WHERE id=$1`)
	// the owner is bound by the trigger too
	if _, err := s.env.Seed.Exec(context.Background(), `UPDATE conversation_closures SET note='reescrito' WHERE id=$1`, res.Closure.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("not even the owner can rewrite a closure: %v", err)
	}
	if s.count(`SELECT count(*) FROM conversation_closures WHERE id=$1 AND note<>'reescrito'`, res.Closure.ID) != 1 {
		t.Fatal("the closure is unchanged")
	}
	// a cascade from the parent still works (deleting the conversation takes its closure and items with it)
	s.exec(`DELETE FROM conversations WHERE id=$1`, conv)
	if s.count(`SELECT count(*) FROM conversation_closures WHERE conversation_id=$1`, conv) != 0 || s.count(`SELECT count(*) FROM follow_up_items WHERE conversation_id=$1`, conv) != 0 {
		t.Fatal("cascades must keep working")
	}
}

// The contact ended the attendance (a flow command): the system closes it like an agent would, recorded as source "system".
func TestFinalizeBySystemClosesLikeAnAgentAndIsRecordedAsTheSystem(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation(nil)
	local := s.ticket(conv, false, false)
	s.exec(`UPDATE conversations SET automation_mode='waiting_human' WHERE id=$1`, conv)

	var err error
	s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) { err = s.svc.CloseForCustomer(ctx, conv, "Encerrado pelo cliente por comando (confirmed).") })
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	var status, mode, source, note string
	var by *uuid.UUID
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT c.status, c.automation_mode, k.source, k.note, k.closed_by_user_id
		FROM conversations c JOIN conversation_closures k ON k.conversation_id=c.id WHERE c.id=$1`, conv).Scan(&status, &mode, &source, &note, &by); err != nil {
		t.Fatal(err)
	}
	if status != "closed" || mode != "none" || source != "system" || by != nil || !strings.Contains(note, "comando") {
		t.Fatalf("closure: status=%s mode=%s source=%s by=%v note=%q", status, mode, source, by, note)
	}
	var tk string
	_ = s.env.Seed.QueryRow(context.Background(), `SELECT status FROM tickets WHERE id=$1`, local).Scan(&tk)
	if tk != "closed" {
		t.Fatalf("the local ticket must be closed: %s", tk)
	}
	// again: idempotent, still one closure
	s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) { err = s.svc.CloseForCustomer(ctx, conv, "again") })
	if err != nil || s.count(`SELECT count(*) FROM conversation_closures WHERE conversation_id=$1`, conv) != 1 {
		t.Fatalf("idempotency: err=%v", err)
	}
	// a human tenant context can NOT use the system path (agents go through Finalize and its permission checks)
	conv2, _ := s.conversation(nil)
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) { err = s.svc.CloseForCustomer(ctx, conv2, "x") })
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("only the system may close for the customer: %v", err)
	}
}
