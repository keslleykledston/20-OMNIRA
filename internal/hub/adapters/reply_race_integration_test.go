package adapters_test

// Codex H1 (ADR-0037): the write path used to ask "does the agent still hold the delegation?" with a plain read, so a
// revocation that was in flight (not yet committed) was invisible to the check, and committed a moment later, after
// the message had been written. The write now pins every row the answer depends on (FOR SHARE) until its own commit.
//
// The proof is deterministic, with no timing luck on the winning side: a second transaction takes the row lock the way
// an administrative change does (an UPDATE it has not committed yet), the agent's write is started, and it must WAIT
// for that transaction. When the change commits, the write must see it. Removing any one of the five pins makes the
// matching case return before the commit, which fails the test.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

type raceCase struct {
	name string
	// change is run, uncommitted, by the "administrator" transaction; args picks its parameters.
	change string
	args   func(w *world, grant, user uuid.UUID) []any
	// after is the status the write must get once the change commits.
	after int
}

func TestHubReply_WriteWaitsForAnInFlightRevocationAndThenHonoursIt(t *testing.T) {
	cases := []raceCase{
		{"the grant is revoked", `UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`,
			func(_ *world, grant, _ uuid.UUID) []any { return []any{grant} }, http.StatusNotFound},
		{"the contract is revoked", `UPDATE hub_tenant_service_contracts SET status = 'revoked' WHERE id = $1`,
			func(w *world, _, _ uuid.UUID) []any { return []any{w.contract["A"]} }, http.StatusNotFound},
		{"the company is suspended", `UPDATE tenants SET status = 'suspended' WHERE id = $1`,
			func(w *world, _, _ uuid.UUID) []any { return []any{w.tenant["A"]} }, http.StatusNotFound},
		{"the hub is suspended", `UPDATE service_hubs SET status = 'suspended' WHERE id = $1`,
			func(w *world, _, _ uuid.UUID) []any { return []any{w.hub} }, http.StatusNotFound},
		// M1: the send context is loaded BEFORE the lock, so a finalization in flight must be seen after it
		{"the attendance is being finalized", `UPDATE conversations SET status = 'closed' WHERE id = $1`,
			func(w *world, _, _ uuid.UUID) []any { return []any{w.conv["A"]} }, http.StatusConflict},
		// membership: an unrelated touch of the row changes nothing, but the write must still wait for it
		{"the person's hub membership is being edited", `UPDATE hub_memberships SET updated_at = now() WHERE hub_id = $1 AND user_id = $2`,
			func(w *world, _, user uuid.UUID) []any { return []any{w.hub, user} }, http.StatusOK},
	}
	for _, op := range []string{"claim", "reply"} {
		for _, tc := range cases {
			tc, op := tc, op
			t.Run(op+" while "+tc.name, func(t *testing.T) {
				w := newWorld(t)
				api := newHubAPI(t, w)
				w.connect("A")
				alice := w.hubAgent("alice")
				grant := w.grantReply(alice, "A")
				if op == "reply" { // the reply path needs the conversation to be theirs already
					if code := api.claim(alice, "A", "A"); code != http.StatusOK {
						t.Fatalf("setup claim: %d", code)
					}
				}
				doWrite := func() int {
					if op == "claim" {
						return api.claim(alice, "A", "A")
					}
					code, _, _ := api.reply(alice, "A", "A", "oi", "key-race-"+uuid.NewString()[:8])
					return code
				}

				tx, err := w.owner.Begin(w.ctx)
				w.must(err)
				defer func() { _ = tx.Rollback(context.Background()) }()
				if _, err := tx.Exec(w.ctx, tc.change, tc.args(w, grant, alice)...); err != nil {
					t.Fatalf("administrative change: %v", err)
				}

				done := make(chan int, 1)
				go func() { done <- doWrite() }()
				select {
				case code := <-done:
					t.Fatalf("the write finished (%d) while a revocation was still in flight: it did not wait for it", code)
				case <-time.After(1500 * time.Millisecond):
				}

				w.must(tx.Commit(w.ctx))

				select {
				case code := <-done:
					want := tc.after
					if want == http.StatusOK && op == "reply" {
						want = http.StatusAccepted // a queued reply answers 202
					}
					if code != want {
						t.Fatalf("after the change committed the write got %d, want %d", code, want)
					}
				case <-time.After(20 * time.Second):
					t.Fatal("the write never finished after the change committed")
				}
				if tc.after != http.StatusOK {
					// a refused write left nothing behind
					if op == "claim" && w.assignee("A") != nil {
						t.Fatal("the refused claim still assigned the conversation")
					}
					if op == "reply" && w.outbound("A") != 0 {
						t.Fatal("the refused reply still queued a message")
					}
				}
			})
		}
	}
}
