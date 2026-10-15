package adapters_test

import (
	"context"
	"strings"
	"testing"

	"github.com/omnira/omnira/internal/platform/opsmetrics"
	"github.com/omnira/omnira/internal/servicectx/adapters"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

// ADR-0049 phase 6: counters move with the facts, their labels come from closed sets, and nothing identifying ever reaches /metrics.
func TestMetricsCountTheFactsWithClosedLabelsAndNoIdentifiers(t *testing.T) {
	s := newStack(t)
	openedBefore := adapters.ServiceMetricValue("service_sessions_opened_total", "operator")
	closedBefore := adapters.ServiceMetricValue("service_sessions_closed_total", "information_provided")
	tasksBefore := adapters.ServiceMetricValue("service_tasks_created_total", "human")

	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	s.as(s.admin, s.env.TenantA, func(ctx context.Context) {
		if _, err := s.svc.Close(ctx, sess.ID, domain.CloseInput{ResolutionCode: domain.ResInformationProvided}, sess.Version); err != nil {
			t.Fatal(err)
		}
		if _, err := s.svc.CreateTask(ctx, domain.TaskOpenInput{Title: "Ligar", OriginType: domain.OriginHuman}); err != nil {
			t.Fatal(err)
		}
	})
	if got := adapters.ServiceMetricValue("service_sessions_opened_total", "operator") - openedBefore; got != 1 {
		t.Errorf("opened: %d", got)
	}
	if got := adapters.ServiceMetricValue("service_sessions_closed_total", "information_provided") - closedBefore; got != 1 {
		t.Errorf("closed: %d", got)
	}
	if got := adapters.ServiceMetricValue("service_tasks_created_total", "human") - tasksBefore; got != 1 {
		t.Errorf("tasks: %d", got)
	}
	// a label outside the closed set is dropped, never turned into a series
	adapters.ServiceMetrics{}.Inc("service_sessions_opened_total", s.env.TenantA.String())
	adapters.ServiceMetrics{}.Inc("service_sessions_closed_total", sess.Protocol)
	adapters.ServiceMetrics{}.Inc("a_metric_that_does_not_exist", "x")
	out := opsmetrics.Render()
	for _, secret := range []string{s.env.TenantA.String(), sess.Protocol, conv.String(), s.admin.String()} {
		if strings.Contains(out, secret) {
			t.Fatalf("/metrics must not carry %q", secret)
		}
	}
	if !strings.Contains(out, "omnira_service_sessions_opened_total") || !strings.Contains(out, "omnira_service_tasks_created_total") {
		t.Fatalf("the module's counters are rendered:\n%s", out)
	}
}
