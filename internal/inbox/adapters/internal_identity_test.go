package adapters_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	accountsadapters "github.com/omnira/omnira/internal/accounts/adapters"
	accountsdomain "github.com/omnira/omnira/internal/accounts/domain"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	contactsadapters "github.com/omnira/omnira/internal/contacts/adapters"
	contactsdomain "github.com/omnira/omnira/internal/contacts/domain"
	conversationsdomain "github.com/omnira/omnira/internal/conversations/domain"
	identityadapters "github.com/omnira/omnira/internal/identity/adapters"
	identitydomain "github.com/omnira/omnira/internal/identity/domain"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	inboxapplication "github.com/omnira/omnira/internal/inbox/application"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ADR-0018 Wave 5 on real Postgres, runtime role under FORCE RLS: the resolver order (verified internal identity, then
// external contact), conversation_kind and its recomputation.

func (e *kindEnv) n(sql string, args ...any) (n int) {
	e.t.Helper()
	if err := e.seed.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return
}

func (e *kindEnv) str(sql string, args ...any) (s string) {
	e.t.Helper()
	if err := e.seed.QueryRow(e.ctx, sql, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return
}

func (e *kindEnv) connection(tenant uuid.UUID) channeldomain.ChannelConnection {
	id := uuid.New()
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
	        VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','{}')`, id, tenant, "wa-"+id.String())
	return channeldomain.ChannelConnection{ID: id, TenantID: tenant, Channel: "whatsapp", Provider: "waha"}
}

type svcOpts struct{ resolver, kind bool }

func (e *kindEnv) service(o svcOpts) *inboxapplication.InboundService {
	store := inboxadapters.NewPostgresInboundStore(e.app).WithConversationKind(o.kind)
	svc := inboxapplication.NewInboundService(store, store, store, inboxadapters.TicketStore{PostgresInboundStore: store}, store).
		WithParticipants(inboxadapters.NewPostgresParticipantRecorder(e.app))
	if o.resolver {
		svc = svc.WithIdentity(identityadapters.NewSenderResolver(e.app, true), store)
	}
	return svc
}

var msgSeq int

func (e *kindEnv) ingest(svc *inboxapplication.InboundService, conn channeldomain.ChannelConnection, from, name, participant string) *inboxapplication.InboundResult {
	e.t.Helper()
	msgSeq++
	var res *inboxapplication.InboundResult
	err := platformdb.WithSystemTenantSession(e.ctx, e.app, conn.TenantID, func(ctx context.Context) error {
		var err error
		res, err = svc.Ingest(ctx, conn, channeldomain.InboundMessage{
			ProviderMessageID: fmt.Sprintf("pm-%d-%s", msgSeq, uuid.NewString()), ConnectionID: conn.ID.String(), FromE164: from,
			SenderName: name, Text: "Olá", Timestamp: time.Now(), ParticipantID: participant, ProviderChatID: participant,
		})
		return err
	})
	if err != nil {
		e.t.Fatalf("ingest: %v", err)
	}
	return res
}

// asUser runs fn in a tenant session of a real user (RLS applies, the permission matrix is the caller's business).
func (e *kindEnv) asUser(tenant, user uuid.UUID, fn func(ctx context.Context)) {
	e.t.Helper()
	if err := platformdb.WithTenantSession(e.ctx, e.app, user, false, func(ctx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		fn(tenancydomain.WithTenantContext(ctx, tc))
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
}

// verified registers AND verifies a staff identity through the real repository (so conflicts are detected).
func (e *kindEnv) verified(tenant, admin, user uuid.UUID, typ identitydomain.Type, scope, raw, normalized string) (id uuid.UUID, conflicts int) {
	e.t.Helper()
	repo := identityadapters.NewRepository(e.app)
	e.asUser(tenant, admin, func(ctx context.Context) {
		i, err := repo.Create(ctx, tenant, admin, user, typ, scope, raw, normalized)
		if err != nil {
			e.t.Fatal(err)
		}
		res, err := repo.Verify(ctx, tenant, admin, i.ID, identitydomain.SourceAdmin)
		if err != nil {
			e.t.Fatal(err)
		}
		id, conflicts = i.ID, len(res.Conflicts)
	})
	return
}

func (e *kindEnv) account(tenant, user uuid.UUID, name string) uuid.UUID {
	var id uuid.UUID
	repo := accountsadapters.NewPostgresRepository(e.app)
	e.asUser(tenant, user, func(ctx context.Context) {
		a, err := repo.CreateAccount(ctx, tenant, name, accountsdomain.TypeCustomer)
		if err != nil {
			e.t.Fatal(err)
		}
		id = a.ID
	})
	return id
}

func (e *kindEnv) classify(tenant, user, contact uuid.UUID, kind contactsdomain.ContactKind, acc *uuid.UUID) contactsadapters.Change {
	e.t.Helper()
	var links []contactsdomain.AccountLinkInput
	if acc != nil {
		links = append(links, contactsdomain.AccountLinkInput{AccountID: *acc, Relationship: contactsdomain.RelEmployee, Primary: true})
	}
	var ch contactsadapters.Change
	e.asUser(tenant, user, func(ctx context.Context) {
		var err error
		ch, err = contactsadapters.NewClassificationRepository(e.app).Classify(ctx, tenant, user, contact, kind, contactsdomain.SourceManual, links, false)
		if err != nil {
			e.t.Fatal(err)
		}
	})
	return ch
}

func TestStaffMessageBecomesAnInternalConversationWithoutAContact(t *testing.T) {
	e := newKindEnv(t)
	a := e.tenant()
	admin, staff := e.member(a, "tenant_admin"), e.member(a, "tenant_agent")
	conn := e.connection(a)
	e.verified(a, admin, staff, identitydomain.TypePhone, "", "+55 92 99999-0101", "+5592999990101")
	svc := e.service(svcOpts{resolver: true, kind: true})

	res := e.ingest(svc, conn, "+5592999990101", "Colega K3G", "5592999990101@lid")
	if res.Contact != nil || res.Conversation == nil || res.Conversation.InternalUserID == nil || *res.Conversation.InternalUserID != staff {
		t.Fatalf("staff sender must produce an internal conversation without a contact: %+v", res)
	}
	if e.n(`SELECT count(*) FROM contacts WHERE tenant_id=$1 AND phone_e164='+5592999990101'`, a) != 0 {
		t.Fatal("a Contact was created for a staff member")
	}
	conv := res.Conversation.ID
	if k := e.str(`SELECT conversation_kind FROM conversations WHERE id=$1`, conv); k != "internal" {
		t.Fatalf("kind = %s", k)
	}
	if e.n(`SELECT count(*) FROM conversations WHERE id=$1 AND contact_id IS NULL AND queue_id IS NULL AND routing_retry_at IS NULL`, conv) != 1 {
		t.Fatal("an internal conversation has no contact and is never queued for customer service")
	}
	if e.n(`SELECT count(*) FROM tickets WHERE conversation_id=$1`, conv) != 0 || e.n(`SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type='job.routing.assign.v1'`, a) != 0 {
		t.Fatal("no placeholder ticket and no routing job for staff")
	}
	if e.n(`SELECT count(*) FROM messages WHERE conversation_id=$1`, conv) != 1 {
		t.Fatal("the message must be stored")
	}
	if e.n(`SELECT count(*) FROM conversation_channel_participants WHERE conversation_id=$1 AND role='member'`, conv) != 1 {
		t.Fatal("the participant is recorded as a member, not a customer")
	}
	// a second message continues the SAME conversation; the permission matrix is untouched
	res2 := e.ingest(svc, conn, "+5592999990101", "Colega K3G", "5592999990101@lid")
	if res2.Conversation.ID != conv || e.n(`SELECT count(*) FROM conversations WHERE tenant_id=$1`, a) != 1 {
		t.Fatal("same staff member + same connection = the same open conversation")
	}
	if e.n(`SELECT count(*) FROM role_permissions rp JOIN memberships m ON m.role_id=rp.role_id WHERE m.user_id=$1 AND rp.permission_key='identity.manage'`, staff) != 0 {
		t.Fatal("an identity must grant no permission")
	}
	// the same staff member found by PROVIDER PARTICIPANT id (a LID that carries no phone)
	e.verified(a, admin, staff, identitydomain.TypeProviderParticipant, identitydomain.ParticipantScope("waha", conn.ID), "777000111@lid", "777000111@lid")
	res3 := e.ingest(svc, conn, "+5592999990199", "Outro numero", "777000111@lid")
	if res3.Contact != nil || res3.Conversation.ID != conv {
		t.Fatalf("participant identity must also resolve to the staff member: %+v", res3)
	}
}

func TestUnknownSenderIsUnclassifiedAndClassificationRecomputesTheKind(t *testing.T) {
	e := newKindEnv(t)
	a := e.tenant()
	admin := e.member(a, "tenant_admin")
	conn := e.connection(a)
	svc := e.service(svcOpts{resolver: true, kind: true})
	res := e.ingest(svc, conn, "+5592999990102", "Joana", "")
	conv, contact := res.Conversation.ID, res.Contact.ID
	var kind string
	var flag bool
	read := func() {
		_ = e.seed.QueryRow(e.ctx, `SELECT conversation_kind, has_unclassified_participants FROM conversations WHERE id=$1`, conv).Scan(&kind, &flag)
	}
	read()
	if kind != "unclassified" || !flag || e.str(`SELECT kind FROM contacts WHERE id=$1`, contact) != "unclassified" {
		t.Fatalf("an unknown 'Olá' is an unclassified contact in an unclassified conversation: %s/%v", kind, flag)
	}
	if e.n(`SELECT count(*) FROM tickets WHERE conversation_id=$1`, conv) != 1 {
		t.Fatal("the external path keeps its existing placeholder ticket behaviour")
	}
	// classified as customer of ACME -> customer_service
	acme := e.account(a, admin, "ACME")
	ch := e.classify(a, admin, contact, contactsdomain.KindCustomer, &acme)
	read()
	if kind != "customer_service" || flag || ch.ConversationsRecomputed != 1 {
		t.Fatalf("customer = %s/%v recomputed=%d", kind, flag, ch.ConversationsRecomputed)
	}
	// a NEW conversation of that contact is born customer_service
	e.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, conv)
	res2 := e.ingest(svc, conn, "+5592999990102", "Joana", "")
	if res2.Conversation.ID == conv || e.str(`SELECT conversation_kind FROM conversations WHERE id=$1`, res2.Conversation.ID) != "customer_service" {
		t.Fatal("a new conversation of a customer starts as customer_service")
	}
	// reclassified as other -> external_other (no customer automation); spam too
	e.classify(a, admin, contact, contactsdomain.KindOther, nil)
	if e.str(`SELECT conversation_kind FROM conversations WHERE id=$1`, res2.Conversation.ID) != "external_other" {
		t.Fatal("other = external_other")
	}
	e.classify(a, admin, contact, contactsdomain.KindUnclassified, nil)
	if e.str(`SELECT conversation_kind FROM conversations WHERE id=$1`, res2.Conversation.ID) != "unclassified" {
		t.Fatal("back to unclassified")
	}
}

func TestOnlyVerifiedIdentitiesOfActiveMembersOfThisTenantMakeASenderInternal(t *testing.T) {
	e := newKindEnv(t)
	a, b := e.tenant(), e.tenant()
	adminA, staffA := e.member(a, "tenant_admin"), e.member(a, "tenant_agent")
	adminB, staffB := e.member(b, "tenant_admin"), e.member(b, "tenant_agent")
	connA, connB := e.connection(a), e.connection(b)
	svc := e.service(svcOpts{resolver: true, kind: true})

	// PENDING never counts
	repo := identityadapters.NewRepository(e.app)
	e.asUser(a, adminA, func(ctx context.Context) {
		if _, err := repo.Create(ctx, a, adminA, staffA, identitydomain.TypePhone, "", "+5592999990103", "+5592999990103"); err != nil {
			t.Fatal(err)
		}
	})
	if res := e.ingest(svc, connA, "+5592999990103", "Quase staff", ""); res.Contact == nil || res.Conversation.InternalUserID != nil {
		t.Fatal("a PENDING identity must not make the sender internal")
	}
	// the same phone verified in TENANT B does not make it internal in tenant A
	e.verified(b, adminB, staffB, identitydomain.TypePhone, "", "+5592999990103", "+5592999990103")
	if res := e.ingest(svc, connA, "+5592999990103", "Quase staff", ""); res.Contact == nil {
		t.Fatal("another tenant's identity must not leak")
	}
	if res := e.ingest(svc, connB, "+5592999990103", "Staff de B", ""); res.Contact != nil || res.Conversation.InternalUserID == nil {
		t.Fatal("inside tenant B it is staff")
	}
	// REVOKED never counts
	id, _ := e.verified(a, adminA, staffA, identitydomain.TypePhone, "", "+5592999990104", "+5592999990104")
	e.asUser(a, adminA, func(ctx context.Context) {
		if _, err := repo.Revoke(ctx, a, adminA, id); err != nil {
			t.Fatal(err)
		}
	})
	if res := e.ingest(svc, connA, "+5592999990104", "Ex staff", ""); res.Contact == nil {
		t.Fatal("a REVOKED identity must not make the sender internal")
	}
	// a member whose membership is no longer active is not staff here any more
	e.verified(a, adminA, staffA, identitydomain.TypePhone, "", "+5592999990105", "+5592999990105")
	e.exec(`UPDATE memberships SET status='revoked' WHERE tenant_id=$1 AND user_id=$2`, a, staffA)
	if res := e.ingest(svc, connA, "+5592999990105", "Saiu", ""); res.Contact == nil {
		t.Fatal("a revoked membership must not make the sender internal")
	}
	// the resolver switched off: even a verified staff phone is just a contact (the old behaviour)
	e.verified(a, adminA, e.member(a, "tenant_agent"), identitydomain.TypePhone, "", "+5592999990106", "+5592999990106")
	if res := e.ingest(e.service(svcOpts{resolver: false, kind: true}), connA, "+5592999990106", "Staff", ""); res.Contact == nil {
		t.Fatal("with the flag off, ingestion is exactly as before")
	}
	// and with the kind derivation off every conversation keeps the default
	if res := e.ingest(e.service(svcOpts{resolver: true, kind: false}), connA, "+5592999990107", "Cliente", ""); e.str(`SELECT conversation_kind FROM conversations WHERE id=$1`, res.Conversation.ID) != "unclassified" {
		t.Fatal("kind derivation off keeps the default")
	}
}

func TestIdentityConflictKeepsCustomerAutomationOffUntilAHumanResolvesIt(t *testing.T) {
	e := newKindEnv(t)
	a := e.tenant()
	admin, staff := e.member(a, "tenant_admin"), e.member(a, "tenant_agent")
	conn := e.connection(a)
	svc := e.service(svcOpts{resolver: true, kind: true})
	// the number is first a CUSTOMER of ACME (a real contact with a customer conversation)
	first := e.ingest(svc, conn, "+5592999990108", "Marcos", "")
	acme := e.account(a, admin, "ACME")
	e.classify(a, admin, first.Contact.ID, contactsdomain.KindCustomer, &acme)
	conv := first.Conversation.ID
	if e.str(`SELECT conversation_kind FROM conversations WHERE id=$1`, conv) != "customer_service" {
		t.Fatal("setup: customer_service")
	}
	// ...then it is verified as a staff identity: a conflict opens and the conversation stops being customer service
	idID, conflicts := e.verified(a, admin, staff, identitydomain.TypePhone, "", "+5592999990108", "+5592999990108")
	if conflicts != 1 || e.str(`SELECT conversation_kind FROM conversations WHERE id=$1`, conv) != "unclassified" {
		t.Fatalf("conflicts=%d kind=%s: an open conflict must suspend customer automation", conflicts, e.str(`SELECT conversation_kind FROM conversations WHERE id=$1`, conv))
	}
	// an inbound from that number meanwhile is handled as the existing contact, never as staff and never as customer service
	res := e.ingest(svc, conn, "+5592999990108", "Marcos", "")
	if res.Contact == nil || res.Contact.ID != first.Contact.ID || res.Conversation.ID != conv {
		t.Fatalf("an open conflict keeps the existing contact path: %+v", res)
	}
	e.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, conv)
	res2 := e.ingest(svc, conn, "+5592999990108", "Marcos", "")
	if e.str(`SELECT conversation_kind FROM conversations WHERE id=$1`, res2.Conversation.ID) != "unclassified" {
		t.Fatal("a NEW conversation born under an open conflict is unclassified")
	}
	// a human decides it IS the staff member: the next message is internal; the contact is left untouched
	var conflictID uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT id FROM identity_resolution_conflicts WHERE identity_id=$1 AND status='open'`, idID).Scan(&conflictID); err != nil {
		t.Fatal(err)
	}
	e.asUser(a, admin, func(ctx context.Context) {
		if _, err := identityadapters.NewRepository(e.app).ResolveConflict(ctx, a, admin, conflictID, identitydomain.ResolutionConfirmedInternal, "é o Marcos da K3G"); err != nil {
			t.Fatal(err)
		}
	})
	res3 := e.ingest(svc, conn, "+5592999990108", "Marcos", "")
	if res3.Contact != nil || res3.Conversation.InternalUserID == nil {
		t.Fatal("after confirmation the number is staff")
	}
	if e.str(`SELECT kind FROM contacts WHERE id=$1`, first.Contact.ID) != "customer" {
		t.Fatal("the contact must be left alone (never deleted, merged or reclassified silently)")
	}
}

func TestRevokingTheIdentityRestoresTheCustomerConversation(t *testing.T) {
	e := newKindEnv(t)
	a := e.tenant()
	admin, staff := e.member(a, "tenant_admin"), e.member(a, "tenant_agent")
	conn := e.connection(a)
	svc := e.service(svcOpts{resolver: true, kind: true})
	first := e.ingest(svc, conn, "+5592999990109", "Paula", "")
	acme := e.account(a, admin, "ACME")
	e.classify(a, admin, first.Contact.ID, contactsdomain.KindCustomer, &acme)
	idID, _ := e.verified(a, admin, staff, identitydomain.TypePhone, "", "+5592999990109", "+5592999990109")
	if e.str(`SELECT conversation_kind FROM conversations WHERE id=$1`, first.Conversation.ID) != "unclassified" {
		t.Fatal("conflict open: unclassified")
	}
	e.asUser(a, admin, func(ctx context.Context) {
		if _, err := identityadapters.NewRepository(e.app).Revoke(ctx, a, admin, idID); err != nil {
			t.Fatal(err)
		}
	})
	if e.str(`SELECT conversation_kind FROM conversations WHERE id=$1`, first.Conversation.ID) != "customer_service" {
		t.Fatal("the identity was wrong: the customer conversation is customer service again")
	}
}

var groupSeq int

func (e *kindEnv) group(tenant uuid.UUID, conn channeldomain.ChannelConnection) uuid.UUID {
	groupSeq++
	g := uuid.New()
	e.exec(`INSERT INTO wa_groups(id,tenant_id,channel_connection_id,provider_group_id,name,enabled) VALUES($1,$2,$3,$4,'Grupo',true)`, g, tenant, conn.ID, fmt.Sprintf("12036300000%08d@g.us", groupSeq))
	e.t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.seed.Exec(bg, `DELETE FROM conversation_topic_focus WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM wa_groups WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM channel_participants WHERE tenant_id=$1`, tenant)
	})
	return g
}

// say makes a participant (optionally bound to a contact) write in the group, through the same SQL the intake uses.
func (e *kindEnv) say(tenant uuid.UUID, conn channeldomain.ChannelConnection, g uuid.UUID, external string, contact *uuid.UUID) {
	e.t.Helper()
	p := uuid.New()
	e.exec(`INSERT INTO channel_participants(id,tenant_id,channel_connection_id,provider,external_participant_id,display_name,contact_id) VALUES($1,$2,$3,'waha',$4,'x',$5)
	        ON CONFLICT (tenant_id,channel_connection_id,provider,external_participant_id) DO NOTHING`, p, tenant, conn.ID, external, contact)
	e.exec(`INSERT INTO wa_group_messages(tenant_id,group_id,provider_message_id,author_jid,author_name,message_type,body,sent_at,sender_channel_participant_id)
	        SELECT $1,$2,$3,$4,'x','text','oi',now(), cp.id FROM channel_participants cp WHERE cp.tenant_id=$1 AND cp.channel_connection_id=$5 AND cp.provider='waha' AND cp.external_participant_id=$4`,
		tenant, g, uuid.NewString(), external, conn.ID)
	e.exec(`SELECT recompute_group_kind($1,$2)`, tenant, g)
}

func TestGroupKindFollowsEachParticipantIndividually(t *testing.T) {
	e := newKindEnv(t)
	a := e.tenant()
	admin, s1, s2 := e.member(a, "tenant_admin"), e.member(a, "tenant_agent"), e.member(a, "tenant_agent")
	conn := e.connection(a)
	e.verified(a, admin, s1, identitydomain.TypeProviderParticipant, identitydomain.ParticipantScope("waha", conn.ID), "111@lid", "111@lid")
	e.verified(a, admin, s2, identitydomain.TypePhone, "", "+5592999990202", "+5592999990202") // a "<digits>@c.us" id carries the phone
	state := func(g uuid.UUID) (string, bool) {
		var k string
		var f bool
		if err := e.seed.QueryRow(e.ctx, `SELECT conversation_kind, has_unclassified_participants FROM wa_groups WHERE id=$1`, g).Scan(&k, &f); err != nil {
			t.Fatal(err)
		}
		return k, f
	}
	// G: everybody is a verified internal human -> internal
	gInternal := e.group(a, conn)
	e.say(a, conn, gInternal, "111@lid", nil)
	e.say(a, conn, gInternal, "5592999990202@c.us", nil)
	if k, f := state(gInternal); k != "internal" || f {
		t.Fatalf("all-internal group = %s/%v", k, f)
	}
	// F: customer + staff + an unknown person -> customer_service WITH the flag (no mixed kind)
	gMixed := e.group(a, conn)
	customer := e.ingest(e.service(svcOpts{resolver: true, kind: true}), conn, "+5592999990203", "Cliente", "").Contact.ID
	acme := e.account(a, admin, "ACME")
	e.classify(a, admin, customer, contactsdomain.KindCustomer, &acme)
	e.say(a, conn, gMixed, "111@lid", nil)
	e.say(a, conn, gMixed, "5592999990203@c.us", &customer)
	if k, f := state(gMixed); k != "customer_service" || f {
		t.Fatalf("customer + staff = %s/%v", k, f)
	}
	e.say(a, conn, gMixed, "999@lid", nil) // nobody knows who this is
	if k, f := state(gMixed); k != "customer_service" || !f {
		t.Fatalf("customer + staff + unknown = %s/%v, want customer_service with has_unclassified_participants", k, f)
	}
	// an unknown participant next to staff only -> unclassified (nobody is assumed to be anybody)
	gUnknown := e.group(a, conn)
	e.say(a, conn, gUnknown, "111@lid", nil)
	e.say(a, conn, gUnknown, "888@lid", nil)
	if k, f := state(gUnknown); k != "unclassified" || !f {
		t.Fatalf("staff + unknown = %s/%v", k, f)
	}
	// reclassifying the customer as "other" re-derives the groups that person is in (same transaction)
	ch := e.classify(a, admin, customer, contactsdomain.KindOther, nil)
	if ch.GroupsRecomputed != 1 {
		t.Fatalf("groups recomputed = %d", ch.GroupsRecomputed)
	}
	if k, f := state(gMixed); k != "unclassified" || !f {
		t.Fatalf("other + staff + unknown = %s/%v", k, f)
	}
}

func TestSQLKindRulesEqualTheGoRules(t *testing.T) {
	e := newKindEnv(t)
	for i := 0; i < 3; i++ {
		for c := 0; c < 3; c++ {
			for o := 0; o < 3; o++ {
				for u := 0; u < 3; u++ {
					var parts []conversationsdomain.ParticipantClass
					add := func(n int, p conversationsdomain.ParticipantClass) {
						for k := 0; k < n; k++ {
							parts = append(parts, p)
						}
					}
					add(i, conversationsdomain.ClassInternal)
					add(c, conversationsdomain.ClassCustomer)
					add(o, conversationsdomain.ClassOther)
					add(u, conversationsdomain.ClassUnclassified)
					want, _ := conversationsdomain.ClassifyKind(parts)
					if got := e.str(`SELECT conversation_kind_from_counts($1,$2,$3,$4)`, i, c, o, u); got != string(want) {
						t.Errorf("counts (%d,%d,%d,%d): SQL=%s Go=%s", i, c, o, u, got, want)
					}
				}
			}
		}
	}
	for _, k := range []string{"unclassified", "customer", "other", "spam"} {
		if got := e.str(`SELECT contact_kind_to_conversation_kind($1)`, k); got != string(conversationsdomain.KindFromContactKind(k)) {
			t.Errorf("contact kind %s: SQL=%s Go=%s", k, got, conversationsdomain.KindFromContactKind(k))
		}
	}
}

func TestInboxHidesInternalByDefaultAndShowsItWhenAsked(t *testing.T) {
	e := newKindEnv(t)
	a := e.tenant()
	admin := e.member(a, "tenant_admin")
	staff := e.member(a, "tenant_agent")
	e.exec(`UPDATE users SET display_name='Ana da K3G' WHERE id=$1`, staff)
	conn := e.connection(a)
	e.verified(a, admin, staff, identitydomain.TypePhone, "", "+5592999990301", "+5592999990301")
	svc := e.service(svcOpts{resolver: true, kind: true})
	internal := e.ingest(svc, conn, "+5592999990301", "", "").Conversation.ID
	customerSide := e.ingest(svc, conn, "+5592999990302", "Cliente Novo", "").Conversation.ID

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(e.app), tenancyadapters.NewPostgresTenantRepository(e.app))
	h := inboxadapters.NewInboxAPIHandler(e.app)
	mux := http.NewServeMux()
	mw := tenancyadapters.AuthorizationMiddleware(e.app, authz)
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations", mw(http.HandlerFunc(h.ListConversations)))
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}", mw(http.HandlerFunc(h.GetConversation)))
	get := func(path string) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: admin, Subject: admin.String()}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	list := func(query string) (int, map[string]map[string]any) {
		code, body := get("/api/v1/tenants/" + a.String() + "/inbox/conversations" + query)
		out := map[string]map[string]any{}
		if code == 200 {
			var page struct{ Items []map[string]any }
			if err := json.Unmarshal(body, &page); err != nil {
				t.Fatal(err)
			}
			for _, it := range page.Items {
				out[it["id"].(string)] = it
			}
		}
		return code, out
	}
	if _, items := list(""); len(items) != 1 || items[customerSide.String()] == nil || items[internal.String()] != nil {
		t.Fatalf("the default list shows attendance only: %v", items)
	}
	_, items := list("?conversation_kind=internal")
	it := items[internal.String()]
	if len(items) != 1 || it == nil || it["conversation_kind"] != "internal" || it["contact_name"] != "Ana da K3G" || it["internal_user_id"] != staff.String() {
		t.Fatalf("internal list = %v", items)
	}
	if _, has := it["contact_id"]; has {
		t.Fatal("an internal conversation has no contact_id")
	}
	if _, items := list("?conversation_kind=unclassified"); len(items) != 1 || items[customerSide.String()]["has_unclassified_participants"] != true {
		t.Fatalf("unclassified list = %v", items)
	}
	if code, _ := list("?conversation_kind=mixed"); code != http.StatusBadRequest {
		t.Fatalf("there is no 'mixed' kind: %d", code)
	}
	code, body := get("/api/v1/tenants/" + a.String() + "/inbox/conversations/" + internal.String())
	var one map[string]any
	_ = json.Unmarshal(body, &one)
	if code != 200 || one["conversation_kind"] != "internal" || one["contact_name"] != "Ana da K3G" {
		t.Fatalf("get internal = %d %v", code, one)
	}
}
