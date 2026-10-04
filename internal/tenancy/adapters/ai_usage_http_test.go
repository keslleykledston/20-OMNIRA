package adapters

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	aiusageadapters "github.com/omnira/omnira/internal/aiusage/adapters"
)

func TestAIUsageEndpointIsAdminOnlyTenantScopedAndShowsTheBudget(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a, b := seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	agent := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	adminB := seedTeamMember(t, seed, b.tenantID, "tenant_admin", "active")
	ledger := aiusageadapters.NewPostgresLedger(app)
	h := aiHandler(t, app).WithUsage(ledger)

	ins := func(tenant uuid.UUID, provider, task string, cost float64, ok bool) {
		if _, err := seed.Exec(t.Context(), `INSERT INTO ai_usage(tenant_id,provider,model,task,input_tokens,output_tokens,cost_usd,success) VALUES($1,$2,'m',$3,1000,50,$4,$5)`, tenant, provider, task, cost, ok); err != nil {
			t.Fatal(err)
		}
	}
	ins(a.tenantID, "gemini", "description", 2.50, true)
	ins(a.tenantID, "gemini", "document_text", 1.25, false)
	ins(a.tenantID, "openai", "topic_summary", 0, true)
	ins(b.tenantID, "gemini", "description", 7.00, true)

	rec := callAI(t, app, h.Usage, a.tenantID, admin, http.MethodGet, "")
	var got struct {
		Month     string  `json:"month"`
		Budget    float64 `json:"budget_usd"`
		Spent     float64 `json:"spent_usd"`
		Remaining float64 `json:"remaining_usd"`
		Items     []struct {
			Provider string `json:"provider"`
			Task     string `json:"task"`
			Calls    int    `json:"calls"`
			Failures int    `json:"failures"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 {
		t.Fatalf("usage = %d %s %v", rec.Code, rec.Body.String(), err)
	}
	if got.Month != time.Now().UTC().Format("2006-01") || got.Budget != 10 || got.Spent != 3.75 || got.Remaining != 6.25 || len(got.Items) != 3 {
		t.Fatalf("usage body = %+v", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("usage must not be cached")
	}
	// tenant B's admin sees only B
	var gotB struct {
		Spent float64 `json:"spent_usd"`
		Items []any   `json:"items"`
	}
	recB := callAI(t, app, h.Usage, b.tenantID, adminB, http.MethodGet, "")
	_ = json.Unmarshal(recB.Body.Bytes(), &gotB)
	if recB.Code != 200 || gotB.Spent != 7 || len(gotB.Items) != 1 {
		t.Fatalf("tenant B = %d %+v", recB.Code, gotB)
	}
	if rec := callAI(t, app, h.Usage, a.tenantID, agent, http.MethodGet, ""); rec.Code != http.StatusForbidden {
		t.Errorf("agent = %d, want 403", rec.Code)
	}
	// an empty past month, and bad input
	if rec := callAI(t, app, h.Usage, a.tenantID, admin, http.MethodGet, ""); rec.Code != 200 {
		t.Errorf("repeat = %d", rec.Code)
	}
	// unwired handler: the endpoint does not exist
	if rec := callAI(t, app, aiHandler(t, app).Usage, a.tenantID, admin, http.MethodGet, ""); rec.Code != http.StatusNotFound {
		t.Errorf("unwired = %d, want 404", rec.Code)
	}
}
