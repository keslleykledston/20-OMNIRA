package adapters_test

// Transfer of a conversation inside the hub (ADR-0038 phase 4) on a real PostgreSQL, through the real handler and the caller's own RLS
// session: only the holder hands it over, only to a person who holds a live reply-capable grant for that conversation, never to
// somebody who lost it meanwhile, never across companies, and the history says who handed it to whom.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func (a *hubAPI) transfer(user uuid.UUID, itemKey, expectedTenantKey string, to *uuid.UUID) (int, string) {
	a.w.t.Helper()
	code, body, _ := a.post(user, a.w.hub, a.w.itemID(itemKey), "transfer", map[string]any{"expected_tenant_id": a.w.tenantFor(expectedTenantKey), "to_user_id": to}, nil)
	return code, body
}

func (a *hubAPI) candidates(user uuid.UUID, itemKey, expectedTenantKey string) (int, string) {
	a.w.t.Helper()
	url := fmt.Sprintf("%s/api/v1/hubs/%s/inbox/%s/transfer-candidates?expected_tenant_id=%s", a.srv.URL, a.w.hub, a.w.itemID(itemKey), a.w.tenantFor(expectedTenantKey))
	req, err := http.NewRequest("GET", url, nil)
	a.w.must(err)
	req.Header.Set("X-Test-User", user.String())
	resp, err := http.DefaultClient.Do(req)
	a.w.must(err)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func ptr(u uuid.UUID) *uuid.UUID { return &u }

func TestHubTransfer_OnlyTheHolderToSomeoneWhoMayAnswer(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	w.connect("A")
	w.connect("B")
	alice, bob, carol := w.hubAgent("alice"), w.hubAgent("bob"), w.hubAgent("carol")
	reader, other := w.hubAgent("reader"), w.hubAgent("other")
	w.grantReply(alice, "A")
	w.grantReply(bob, "A")
	w.grantReply(carol, "B") // answers B, not A
	w.grant(reader, "A")     // read-only on A
	w.grantReply(other, "A")

	if code := api.claim(alice, "A", "A"); code != 200 {
		t.Fatalf("claim: %d", code)
	}

	t.Run("somebody who does not hold it cannot transfer (409), and nothing changes", func(t *testing.T) {
		if code, body := api.transfer(bob, "A", "A", ptr(other)); code != http.StatusConflict || !strings.Contains(body, "not yours") {
			t.Fatalf("a person who does not hold it: %d %s", code, body)
		}
		if h := w.assignee("A"); h == nil || *h != alice {
			t.Fatal("a refused transfer moved the conversation")
		}
		if code, _ := api.candidates(bob, "A", "A"); code != http.StatusConflict {
			t.Fatalf("candidates for a person who does not hold it: %d", code)
		}
	})
	t.Run("a read-only person cannot even ask (403)", func(t *testing.T) {
		if code, _ := api.transfer(reader, "A", "A", ptr(bob)); code != http.StatusForbidden {
			t.Fatalf("read-only: %d", code)
		}
	})
	t.Run("the candidates are who may answer THIS company, least loaded first, never the holder", func(t *testing.T) {
		code, body := api.candidates(alice, "A", "A")
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		var out struct {
			Items []struct {
				UserID uuid.UUID `json:"user_id"`
				Load   int       `json:"load"`
			} `json:"items"`
		}
		w.must(json.Unmarshal([]byte(body), &out))
		got := map[uuid.UUID]bool{}
		for _, c := range out.Items {
			got[c.UserID] = true
		}
		if !got[bob] || !got[other] {
			t.Errorf("people who may answer A are missing: %s", body)
		}
		if got[alice] || got[carol] || got[reader] {
			t.Errorf("the holder, someone who only answers B and a read-only person must not be offered: %s", body)
		}
	})
	t.Run("a stale screen (wrong company) is a 409 and offers nobody", func(t *testing.T) {
		if code, _ := api.candidates(alice, "A", "B"); code != http.StatusConflict {
			t.Fatalf("wrong company: %d", code)
		}
		if code, _ := api.transfer(alice, "A", "B", ptr(bob)); code != http.StatusConflict {
			t.Fatalf("transfer with the wrong company: %d", code)
		}
	})
	t.Run("not to a person without a reply grant on this company, nor to oneself, nor to a stranger", func(t *testing.T) {
		for name, to := range map[string]uuid.UUID{"read-only": reader, "answers another company": carol, "oneself": alice, "a stranger": uuid.New()} {
			if code, body := api.transfer(alice, "A", "A", ptr(to)); code != http.StatusConflict || !strings.Contains(body, "not available") {
				t.Errorf("%s: %d %s", name, code, body)
			}
		}
		if h := w.assignee("A"); h == nil || *h != alice {
			t.Fatal("a refused transfer moved the conversation")
		}
	})
	t.Run("the holder hands it over; history and audit say who gave it to whom", func(t *testing.T) {
		if code, body := api.transfer(alice, "A", "A", ptr(bob)); code != http.StatusOK {
			t.Fatalf("transfer: %d %s", code, body)
		}
		if h := w.assignee("A"); h == nil || *h != bob {
			t.Fatal("the conversation did not move to bob")
		}
		if n := w.count(`SELECT count(*) FROM assignment_events WHERE conversation_id = $1 AND reason = 'hub_transfer' AND from_user_id = $2 AND to_user_id = $3 AND changed_by = $2`, w.conv["A"], alice, bob); n != 1 {
			t.Fatalf("history rows: %d", n)
		}
		if n := w.count(`SELECT count(*) FROM audit_events WHERE action = 'hub.conversation.transferred' AND actor_id = $1 AND tenant_id = $2`, alice, w.tenant["A"]); n != 1 {
			t.Fatalf("audit rows: %d", n)
		}
		// alice no longer holds it: she cannot hand it over again
		if code, _ := api.transfer(alice, "A", "A", ptr(other)); code != http.StatusConflict {
			t.Fatalf("a previous holder must not transfer again: %d", code)
		}
	})
	t.Run("giving it back to the queue leaves it unassigned and is recorded", func(t *testing.T) {
		if code, body := api.transfer(bob, "A", "A", nil); code != http.StatusOK {
			t.Fatalf("release: %d %s", code, body)
		}
		if w.assignee("A") != nil {
			t.Fatal("released conversation must be unassigned")
		}
		if n := w.count(`SELECT count(*) FROM assignment_events WHERE conversation_id = $1 AND reason = 'hub_release' AND to_user_id IS NULL AND changed_by = $2`, w.conv["A"], bob); n != 1 {
			t.Fatalf("release history: %d", n)
		}
		// and anyone who may answer can claim it again
		if code := api.claim(other, "A", "A"); code != http.StatusOK {
			t.Fatalf("claim after release: %d", code)
		}
	})
}

func TestHubTransfer_RefusedWhenTheAccessOfEitherSideEndsOrTheConversationIsClosed(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	w.connect("A")
	alice, bob := w.hubAgent("alice"), w.hubAgent("bob")
	w.grantReply(alice, "A")
	gb := w.grantReply(bob, "A")
	api.claim(alice, "A", "A")

	// the person chosen lost the grant: refused (the check is made in the transaction, not on the stale list)
	w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, gb)
	if code, _ := api.transfer(alice, "A", "A", ptr(bob)); code != http.StatusConflict {
		t.Fatalf("a revoked grant must not receive: %d", code)
	}
	w.exec(`UPDATE effective_access_grants SET status = 'active' WHERE id = $1`, gb)
	w.exec(`UPDATE users SET status = 'inactive' WHERE id = $1`, bob)
	if code, _ := api.transfer(alice, "A", "A", ptr(bob)); code != http.StatusConflict {
		t.Fatalf("an inactive account must not receive: %d", code)
	}
	w.exec(`UPDATE users SET status = 'active' WHERE id = $1`, bob)
	// the contract of the company is suspended: nobody can transfer (the holder's own access is gone too)
	w.exec(`UPDATE hub_tenant_service_contracts SET status = 'suspended' WHERE id = $1`, w.contract["A"])
	if code, _ := api.transfer(alice, "A", "A", ptr(bob)); code != http.StatusNotFound {
		t.Fatalf("a suspended contract: %d", code)
	}
	w.exec(`UPDATE hub_tenant_service_contracts SET status = 'active' WHERE id = $1`, w.contract["A"])
	// a finalized conversation cannot be handed over
	w.exec(`UPDATE conversations SET status = 'closed' WHERE id = $1`, w.conv["A"])
	if code, _ := api.transfer(alice, "A", "A", ptr(bob)); code != http.StatusConflict {
		t.Fatalf("a closed conversation: %d", code)
	}
	if h := w.assignee("A"); h == nil || *h != alice {
		t.Fatal("none of the refusals may move the conversation")
	}
}

// Two transfers of the same conversation at once, and a transfer racing a revocation of the target: the conversation row is locked and
// the target is pinned, so exactly one transfer wins and a revoked target never receives.
func TestHubTransfer_RacesAreResolvedByTheLocks(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	w.connect("A")
	alice, bob, carol := w.hubAgent("alice"), w.hubAgent("bob"), w.hubAgent("carol")
	w.grantReply(alice, "A")
	w.grantReply(bob, "A")
	w.grantReply(carol, "A")
	api.claim(alice, "A", "A")

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, to := range []uuid.UUID{bob, carol} {
		wg.Add(1)
		go func(i int, to uuid.UUID) {
			defer wg.Done()
			codes[i], _ = api.transfer(alice, "A", "A", ptr(to))
		}(i, to)
	}
	wg.Wait()
	wins := 0
	for _, c := range codes {
		if c == http.StatusOK {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one of two simultaneous transfers may win, codes=%v", codes)
	}
	if n := w.count(`SELECT count(*) FROM assignment_events WHERE conversation_id = $1 AND reason = 'hub_transfer'`, w.conv["A"]); n != 1 {
		t.Fatalf("one history row expected, got %d", n)
	}

	// a revocation committing WHILE the transfer pins the target wins
	holder := *w.assignee("A")
	other := bob
	if holder == bob {
		other = carol
	}
	var target uuid.UUID
	for _, c := range []uuid.UUID{bob, carol} {
		if c != holder {
			target = c
		}
	}
	_ = other
	tx, err := w.owner.Begin(w.ctx)
	w.must(err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	_, err = tx.Exec(w.ctx, `UPDATE effective_access_grants SET status = 'revoked' WHERE user_id = $1 AND tenant_id = $2`, target, w.tenant["A"])
	w.must(err)
	done := make(chan int, 1)
	go func() {
		code, _ := api.transfer(holder, "A", "A", ptr(target))
		done <- code
	}()
	select {
	case <-done:
		t.Fatal("the transfer did not wait for the revocation in progress")
	case <-time.After(700 * time.Millisecond):
	}
	w.must(tx.Commit(w.ctx))
	if code := <-done; code != http.StatusConflict {
		t.Fatalf("a target revoked while the transfer ran must be refused: %d", code)
	}
	if h := w.assignee("A"); h == nil || *h != holder {
		t.Fatal("the conversation must stay with its holder")
	}
}
