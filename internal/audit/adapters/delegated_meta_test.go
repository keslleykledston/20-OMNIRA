package adapters

import (
	"context"
	"testing"

	"github.com/google/uuid"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func TestWithDelegatedContextOnlyDescribesDelegatedServing(t *testing.T) {
	tn, u, h, c, g := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	serve, _ := tenancydomain.NewHubServeTenantContext(tn, u, h, c, g, nil, "")
	manage, _ := tenancydomain.NewHubManageTenantContext(tn, u, h, c, &g, "")
	direct, _ := tenancydomain.NewTenantContext(tn, u, tenancydomain.AccessSourceDirect)

	in := map[string]any{"field": "alias"}
	got := withDelegatedContext(tenancydomain.WithTenantContext(context.Background(), serve), in)
	for k, want := range map[string]string{"via": "hub", "acting_as": "hub:" + h.String(), "hub_id": h.String(), "contract_id": c.String(), "grant_id": g.String(), "field": "alias"} {
		if got[k] != want {
			t.Errorf("serve: %s = %v, want %v", k, got[k], want)
		}
	}
	if _, touched := in["via"]; touched {
		t.Error("the caller's map must not be modified")
	}
	// keys the caller already set are kept
	kept := withDelegatedContext(tenancydomain.WithTenantContext(context.Background(), serve), map[string]any{"via": "mine", "acting_as": "mine"})
	if kept["via"] != "mine" || kept["acting_as"] != "mine" {
		t.Errorf("caller keys overwritten: %v", kept)
	}
	// every other context, and no context at all, leaves the event exactly as it was
	for name, ctx := range map[string]context.Context{
		"member":     tenancydomain.WithTenantContext(context.Background(), direct),
		"management": tenancydomain.WithTenantContext(context.Background(), manage),
		"none":       context.Background(),
	} {
		out := withDelegatedContext(ctx, map[string]any{"field": "alias"})
		if len(out) != 1 || out["field"] != "alias" {
			t.Errorf("%s: event must be untouched, got %v", name, out)
		}
	}
	if out := withDelegatedContext(context.Background(), nil); out != nil {
		t.Errorf("nil metadata stays nil: %v", out)
	}
}
