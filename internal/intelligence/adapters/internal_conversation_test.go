package adapters

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

// ADR-0018: a conversation with a verified staff member has no contact, and topics belong to a contact, so the router
// must never take it. The routing worker must also survive its NULL contact_id (no scan error, no retry storm).
func TestInternalConversationIsNeverRoutedToTopics(t *testing.T) {
	e := newEnv(t)
	f := e.tenant()
	staff := e.member(f.id, "tenant_agent")
	conn := uuid.New()
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
	        VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','{}')`, conn, f.id, "wa-"+conn.String())
	conv := uuid.New()
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,internal_user_id,conversation_kind,channel_connection_id,status) VALUES($1,$2,NULL,$3,'internal',$4,'open')`, conv, f.id, staff, conn)
	msg := e.message(f.id, conv, "oi, tudo certo?")

	repo := NewPostgresRoutingRepository(e.app)
	e.session(f.id, staff, func(ctx context.Context) {
		m, err := repo.LoadRoutable(ctx, f.id, ports.MessageRef{Kind: ports.KindConversation, ID: msg})
		if err != nil {
			t.Fatalf("LoadRoutable must cope with a NULL contact_id: %v", err)
		}
		if !m.Internal || m.ContactID != nil || !m.Inbound {
			t.Fatalf("routable = %+v", m)
		}
		// topics belong to a contact: a staff conversation does not exist for the topic API (404 on every route)
		info, err := NewPostgresTopicRepository(e.app).ConversationInfo(ctx, f.id, conv)
		if err != nil || info.Exists {
			t.Fatalf("ConversationInfo(internal) = %+v %v, want not found", info, err)
		}
	})
	// the same message of an ordinary conversation is still routable (the gate is only for staff)
	other := e.message(f.id, f.conversation, "preciso de ajuda")
	e.session(f.id, staff, func(ctx context.Context) {
		m, err := repo.LoadRoutable(ctx, f.id, ports.MessageRef{Kind: ports.KindConversation, ID: other})
		if err != nil || m.Internal || m.ContactID == nil {
			t.Fatalf("customer conversation routable = %+v %v", m, err)
		}
	})
	// and the service refuses to route it, with the sentinel the worker already treats as "nothing to do"
	svc := application.NewRoutingService(repo, NewPostgresTopicRepository(e.app), application.Flags{TopicThreadsEnabled: true, TopicAutoRoutingEnabled: true}, domain.DefaultRoutingConfig(), nil)
	e.session(f.id, staff, func(ctx context.Context) {
		if _, err := svc.Route(ctx, ports.MessageRef{Kind: ports.KindConversation, ID: msg}, application.RouteOptions{}); !errors.Is(err, application.ErrNotRoutable) {
			t.Fatalf("Route(internal) = %v, want ErrNotRoutable", err)
		}
	})
	if e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1`, f.id) != 0 {
		t.Fatal("no topic may be created for a staff conversation")
	}
}
