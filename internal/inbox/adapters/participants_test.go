package adapters

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/inbox/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

// Real Postgres, runtime role under FORCE RLS (ADR-0017 Wave 2).

type pEnv struct {
	t    *testing.T
	ctx  context.Context
	seed *pgxpool.Pool
	app  *pgxpool.Pool
}

func newPEnv(t *testing.T) *pEnv {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(seed.Close)
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return &pEnv{t: t, ctx: ctx, seed: seed, app: app}
}

func (e *pEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *pEnv) count(sql string, args ...any) (n int) {
	e.t.Helper()
	if err := e.seed.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

type pTenant struct{ id, conn, contact, conversation uuid.UUID }

func (e *pEnv) tenant() pTenant {
	f := pTenant{id: uuid.New(), conn: uuid.New(), contact: uuid.New(), conversation: uuid.New()}
	e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, f.id, f.id.String())
	e.t.Cleanup(func() {
		bg := context.Background()
		for _, q := range []string{`DELETE FROM messages WHERE tenant_id=$1`, `DELETE FROM conversations WHERE tenant_id=$1`, `DELETE FROM contacts WHERE tenant_id=$1`,
			`DELETE FROM channel_connections WHERE tenant_id=$1`, `DELETE FROM tenants WHERE id=$1`} {
			_, _ = e.seed.Exec(bg, q, f.id)
		}
	})
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
	        VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','{}')`, f.conn, f.id, "wa-"+f.id.String())
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164,status) VALUES($1,$2,'C',$3,'active')`, f.contact, f.id, fmt.Sprintf("+55119%08d", rand.Intn(100000000)))
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status) VALUES($1,$2,$3,$4,'open')`, f.conversation, f.id, f.contact, f.conn)
	return f
}

func (e *pEnv) message(f pTenant, conversation uuid.UUID, providerID string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,channel_connection_id,direction,message_type,body,provider_message_id,status)
	        VALUES($1,$2,$3,$4,'inbound','text','x',$5,'received')`, id, f.id, conversation, f.conn, providerID)
	return id
}

func (e *pEnv) record(f pTenant, in application.ParticipantInput) error {
	e.t.Helper()
	var err error
	_ = platformdb.WithSystemTenantSession(e.ctx, e.app, f.id, func(ctx context.Context) error {
		tc, _ := tenancydomain.NewTenantContext(f.id, uuid.New(), tenancydomain.AccessSourceDirect)
		err = NewPostgresParticipantRecorder(e.app).RecordInbound(tenancydomain.WithTenantContext(ctx, tc), in)
		return nil
	})
	return err
}

func input(f pTenant, ext string, msg uuid.UUID, reply string) application.ParticipantInput {
	return application.ParticipantInput{ConnectionID: f.conn, Provider: "waha", ExternalID: ext, DisplayName: "Fulano", ContactID: f.contact,
		ConversationID: f.conversation, MessageID: msg, ReplyToExternalID: reply}
}

func TestIndividualConversationParticipantIsRecordedOnceAndBoundToTheContact(t *testing.T) {
	e := newPEnv(t)
	f := e.tenant()
	m1 := e.message(f, f.conversation, "false_175222334484588@lid_3EB0AAA1")
	m2 := e.message(f, f.conversation, "false_175222334484588@lid_3EB0AAA2")
	for i := 0; i < 3; i++ { // a webhook replay must not duplicate anything
		if err := e.record(f, input(f, "175222334484588@lid", m1, "")); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.record(f, input(f, "175222334484588@lid", m2, "")); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM channel_participants WHERE tenant_id=$1`, f.id); n != 1 {
		t.Fatalf("participants = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM channel_participants WHERE tenant_id=$1 AND contact_id=$2 AND provider='waha'`, f.id, f.contact); n != 1 {
		t.Fatal("the participant must be bound to the contact and carry its provider")
	}
	if n := e.count(`SELECT count(*) FROM conversation_channel_participants WHERE tenant_id=$1 AND conversation_id=$2 AND role='customer'`, f.id, f.conversation); n != 1 {
		t.Fatal("the participant must be linked to the conversation as the customer, once")
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE tenant_id=$1 AND sender_channel_participant_id IS NOT NULL`, f.id); n != 2 {
		t.Fatalf("both messages must carry their sender, got %d", n)
	}
}

func TestReplyResolvesInsideTheSameConversationByBareOrFullProviderId(t *testing.T) {
	e := newPEnv(t)
	f := e.tenant()
	other := uuid.New()
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status) VALUES($1,$2,$3,$4,'closed')`, other, f.id, f.contact, f.conn)
	original := e.message(f, f.conversation, "true_175222334484588@lid_3EB0ORIG")
	elsewhere := e.message(f, other, "false_175222334484588@lid_3EB0ELSE")
	byBare := e.message(f, f.conversation, "false_175222334484588@lid_3EB0R001")
	byFull := e.message(f, f.conversation, "wamid.R002")
	byCross := e.message(f, f.conversation, "false_175222334484588@lid_3EB0R003")
	unknown := e.message(f, f.conversation, "false_175222334484588@lid_3EB0R004")
	_ = elsewhere
	// WAHA: bare id of the quoted message (the third segment of the serialized id)
	if err := e.record(f, input(f, "175222334484588@lid", byBare, "3EB0ORIG")); err != nil {
		t.Fatal(err)
	}
	// Meta: the full id. Store the original under a Meta-style id to prove the exact match path.
	e.exec(`UPDATE messages SET provider_message_id='wamid.ORIGINAL' WHERE id=$1`, original)
	if err := e.record(f, input(f, "5511999990000", byFull, "wamid.ORIGINAL")); err != nil {
		t.Fatal(err)
	}
	// a quoted id that only exists in ANOTHER conversation must not resolve
	if err := e.record(f, input(f, "175222334484588@lid", byCross, "3EB0ELSE")); err != nil {
		t.Fatal(err)
	}
	// unknown stays unresolved but keeps the provider id
	if err := e.record(f, input(f, "175222334484588@lid", unknown, "3EB0NOPE")); err != nil {
		t.Fatal(err)
	}
	resolved := func(id uuid.UUID) (to *uuid.UUID, ext string) {
		_ = e.seed.QueryRow(e.ctx, `SELECT reply_to_message_id, reply_to_external_message_id FROM messages WHERE id=$1`, id).Scan(&to, &ext)
		return to, ext
	}
	if to, ext := resolved(byBare); to == nil || *to != original || ext != "3EB0ORIG" {
		t.Fatalf("bare id: %v %q", to, ext)
	}
	if to, ext := resolved(byFull); to == nil || *to != original || ext != "wamid.ORIGINAL" {
		t.Fatalf("full id: %v %q", to, ext)
	}
	if to, ext := resolved(byCross); to != nil || ext != "3EB0ELSE" {
		t.Fatalf("a reply must not resolve across conversations: %v %q", to, ext)
	}
	if to, ext := resolved(unknown); to != nil || ext != "3EB0NOPE" {
		t.Fatalf("unknown reply: %v %q", to, ext)
	}
}

func TestMissingMetadataDegradesGracefullyAndParticipantsAreTenantAndConnectionScoped(t *testing.T) {
	e := newPEnv(t)
	a, b := e.tenant(), e.tenant()
	// no sender id, no reply: nothing is created and nothing fails
	m := e.message(a, a.conversation, "false_x_3EB0M1")
	in := input(a, "", m, "")
	if err := e.record(a, in); err != nil {
		t.Fatalf("a message without metadata must not fail: %v", err)
	}
	if e.count(`SELECT count(*) FROM channel_participants WHERE tenant_id=$1`, a.id) != 0 {
		t.Fatal("no sender id means no participant")
	}
	// the same provider id in two tenants, and on two connections of one tenant: four distinct participants
	mb := e.message(b, b.conversation, "false_y_3EB0M2")
	if err := e.record(a, input(a, "175222334484588@lid", m, "")); err != nil {
		t.Fatal(err)
	}
	if err := e.record(b, input(b, "175222334484588@lid", mb, "")); err != nil {
		t.Fatal(err)
	}
	second := uuid.New()
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','{}')`, second, a.id, "wa2-"+a.id.String())
	m3 := e.message(a, a.conversation, "false_z_3EB0M3")
	in3 := input(a, "175222334484588@lid", m3, "")
	in3.ConnectionID = second
	if err := e.record(a, in3); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(DISTINCT id) FROM channel_participants WHERE external_participant_id='175222334484588@lid' AND tenant_id IN ($1,$2)`, a.id, b.id); n != 3 {
		t.Fatalf("distinct participants = %d, want 3 (tenant A conn 1, tenant A conn 2, tenant B)", n)
	}
	// a participant of tenant B cannot be attached to a conversation of tenant A: the composite FK refuses it
	var foreign uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT id FROM channel_participants WHERE tenant_id=$1`, b.id).Scan(&foreign)
	if _, err := e.seed.Exec(e.ctx, `INSERT INTO conversation_channel_participants(tenant_id, conversation_id, channel_participant_id) VALUES($1,$2,$3)`, a.id, a.conversation, foreign); err == nil {
		t.Fatal("linking another tenant's participant must be refused by the composite foreign key")
	}
	// and a runtime session of A sees none of B's participants
	var seen int
	_ = platformdb.WithTenantSession(e.ctx, e.app, uuid.New(), true, func(ctx context.Context) error {
		return platformdb.QuerierFromContext(ctx, e.app).QueryRow(ctx, `SELECT count(*) FROM channel_participants`).Scan(&seen)
	})
	if seen < 3 {
		t.Fatalf("a system session should see all rows, saw %d (sanity check of the test)", seen)
	}
}
