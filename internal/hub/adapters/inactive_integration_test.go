package adapters_test

// Codex review: an account that is no longer active reads and answers nothing through a Hub, whatever grants it still has. The database says
// so (has_active_hub_access, has_hub_manage_access) and the write paths, which re-ask it inside their own transactions, follow.

import (
	"net/http"
	"testing"
)

func TestInactiveAccountReadsAndAnswersNothingThroughTheHub(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	w.connect("A")
	alice := w.hubAgent("alice")
	w.grantReply(alice, "A")

	if n := w.n(alice, `SELECT count(*) FROM conversations WHERE tenant_id = $1`, w.tenant["A"]); n == 0 {
		t.Fatal("baseline: a live reply grant reads the instance's conversations")
	}
	w.exec(`UPDATE users SET status = 'inactive' WHERE id = $1`, alice)
	if n := w.n(alice, `SELECT count(*) FROM conversations WHERE tenant_id = $1`, w.tenant["A"]); n != 0 {
		t.Errorf("an inactive account must read nothing through the hub, saw %d conversation(s)", n)
	}
	if n := w.n(alice, `SELECT count(*) FROM hub_inbox_items WHERE tenant_id = $1`, w.tenant["A"]); n != 0 {
		t.Errorf("nor the inbox projection, saw %d", n)
	}
	if code := api.claim(alice, "A", "A"); code != http.StatusNotFound {
		t.Errorf("an inactive account must not claim: %d", code)
	}
	if w.assignee("A") != nil {
		t.Error("a refused claim changed the conversation")
	}
	w.exec(`UPDATE users SET status = 'active' WHERE id = $1`, alice)
	if code := api.claim(alice, "A", "A"); code != http.StatusOK {
		t.Errorf("reactivating the account restores access: %d", code)
	}
}
