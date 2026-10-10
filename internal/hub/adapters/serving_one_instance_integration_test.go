package adapters_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Migration 000111: a delegated request acts for ONE instance in the database too. The same hub serves A and B and the same person holds the
// same keys on both; acting for A, nothing of B is visible or writable, in any table a delegate can reach.
func TestTheDelegatedContextIsPinnedToOneInstance(t *testing.T) {
	w := newWorld(t)
	keys := []string{"conversation.read", "conversation.reply", "conversation.claim", "media.read", "contact.read", "contact.classify", "account.read"}
	for _, k := range []string{"A", "B"} {
		w.mediaOf(k, k)
		w.outboundMediaOf(k, k)
		w.ceiling(k, keys...)
	}
	acctA, acctB := w.account("A", "Conta A"), w.account("B", "Conta B")
	contactB := w.contactOf("B")
	w.exec(`INSERT INTO contact_account_links (tenant_id, contact_id, account_id, relationship_type, source) VALUES ($1,$2,$3,'other','manual')`, w.tenant["B"], contactB, acctB)
	w.exec(`INSERT INTO contact_account_links (tenant_id, contact_id, account_id, relationship_type, source) VALUES ($1,$2,$3,'other','manual')`, w.tenant["A"], w.contactOf("A"), acctA)
	agent := w.hubAgent("agent")
	w.grantKeys(w.grant(agent, "A"), keys...)
	w.grantKeys(w.grant(agent, "B"), keys...)

	tables := []string{"conversations", "messages", "contacts", "contact_account_links", "customer_accounts", "message_media", "message_media_analysis", "message_outbound_media"}
	count := func(ctx context.Context, q platformdb.Querier, table string, tenant uuid.UUID) int {
		var n int
		w.must(q.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, tenant).Scan(&n))
		return n
	}

	t.Run("acting for A shows A and nothing of B, in every table", func(t *testing.T) {
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			for _, table := range tables {
				if count(ctx, q, table, w.tenant["A"]) == 0 {
					t.Errorf("%s: acting for A shows nothing of A (the pin must not hide the instance itself)", table)
				}
				if n := count(ctx, q, table, w.tenant["B"]); n != 0 {
					t.Errorf("%s: acting for A shows %d rows of B", table, n)
				}
			}
		})
	})
	t.Run("acting for B shows B and nothing of A", func(t *testing.T) {
		w.attend(agent, "B", func(ctx context.Context, q platformdb.Querier) {
			for _, table := range tables {
				if count(ctx, q, table, w.tenant["B"]) == 0 {
					t.Errorf("%s: acting for B shows nothing of B", table)
				}
				if n := count(ctx, q, table, w.tenant["A"]); n != 0 {
					t.Errorf("%s: acting for B shows %d rows of A", table, n)
				}
			}
		})
	})
	t.Run("acting for A cannot write into B, with a key that B also grants", func(t *testing.T) {
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			tag, err := q.Exec(ctx, `UPDATE contacts SET alias = 'x' WHERE id = $1`, contactB)
			w.must(err)
			if tag.RowsAffected() != 0 {
				t.Error("a contact of B was updated while acting for A")
			}
			other := w.account("B", "Outra de B")
			if err := w.try(ctx, q, `INSERT INTO contact_account_links (tenant_id, contact_id, account_id, relationship_type, source) VALUES ($1,$2,$3,'other','manual')`, w.tenant["B"], contactB, other); err == nil {
				t.Error("a link was added to B while acting for A")
			}
		})
	})
	t.Run("fail closed: acting for a hub without a valid acting instance matches nothing", func(t *testing.T) {
		for _, bad := range []string{"", "not-a-uuid"} {
			w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
				w.must(q.QueryRow(ctx, `SELECT set_config('app.acting_tenant', $1, true)`, bad).Scan(new(string)))
				for _, table := range tables {
					if n := count(ctx, q, table, w.tenant["A"]); n != 0 {
						t.Errorf("acting_tenant=%q: %s still shows %d rows", bad, table, n)
					}
				}
			})
		}
	})
	t.Run("outside the delegated context nothing changed: a member still sees their instance", func(t *testing.T) {
		member := w.user("member")
		w.directMember(member, "A")
		if n := w.n(member, `SELECT count(*) FROM conversations WHERE tenant_id = $1`, w.tenant["A"]); n == 0 {
			t.Error("a member lost sight of their own instance")
		}
		if n := w.n(member, `SELECT count(*) FROM conversations WHERE tenant_id = $1`, w.tenant["B"]); n != 0 {
			t.Error("a member of A saw B")
		}
	})
}
