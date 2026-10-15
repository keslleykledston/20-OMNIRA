package adapters

import "github.com/omnira/omnira/internal/platform/opsmetrics"

// Metrics of the module (ADR-0049 phase 6). Every label VALUE comes from a closed set written here: an outcome, a status, a source, a result. Never a company, a
// person, a conversation, a protocol or any text from a request (those belong to logs and traces, docs/architecture/OBSERVABILITY.md). A value outside its set is
// dropped by the counter and counted, so a bug cannot turn a metric into an unbounded series.
var (
	canonicalResults = []string{"information_provided", "resolved_first_contact", "task_created", "case_created", "external_service_order", "transferred", "abandoned", "other"}
	caseStatuses     = []string{"open", "investigating", "waiting", "resolved", "closed", "cancelled"}
	taskStatuses     = []string{"open", "in_progress", "done", "cancelled"}
	requestStates    = []string{"in_flight", "confirmed_success", "confirmed_failure", "outcome_unknown"}

	serviceCounters = map[string]*opsmetrics.Counter{
		"service_sessions_opened_total":   opsmetrics.NewCounter("omnira_service_sessions_opened_total", "Service sessions opened, by source", "source", "operator", "finalize", "flow", "api", "system"),
		"service_sessions_closed_total":   opsmetrics.NewCounter("omnira_service_sessions_closed_total", "Service sessions closed, by canonical result", "result", canonicalResults...),
		"service_sessions_reopened_total": opsmetrics.NewCounter("omnira_service_sessions_reopened_total", "Service sessions reopened", ""),
		"service_cases_opened_total":      opsmetrics.NewCounter("omnira_service_cases_opened_total", "Service cases (ocorrencias) opened", ""),
		"service_case_status_total":       opsmetrics.NewCounter("omnira_service_case_status_total", "Service case status changes, by new status", "status", caseStatuses...),
		"service_tasks_created_total":     opsmetrics.NewCounter("omnira_service_tasks_created_total", "Service tasks created, by origin", "origin", "human", "flow", "ai", "api"),
		"service_task_status_total":       opsmetrics.NewCounter("omnira_service_task_status_total", "Service task status changes, by new status", "status", taskStatuses...),
		"service_external_links_total":    opsmetrics.NewCounter("omnira_service_external_links_total", "ERP/CRM records linked to the service context", "outcome", "linked"),
		"service_external_requests_total": opsmetrics.NewCounter("omnira_service_external_requests_total", "Requests to the ERP, by state reached", "state", requestStates...),
		"service_external_updates_total":  opsmetrics.NewCounter("omnira_service_external_updates_total", "Inbound ERP updates, by outcome", "outcome", "status_changed", "out_of_order"),
	}
)

// ServiceMetrics implements application.Metrics on the process-wide opsmetrics registry (api and worker both render it on /metrics).
type ServiceMetrics struct{}

func (ServiceMetrics) Inc(name string, labels ...string) {
	c, ok := serviceCounters[name]
	if !ok {
		return
	}
	v := ""
	if len(labels) > 0 {
		v = labels[0]
	}
	c.Inc(v)
}

// ServiceMetricValue reads a counter (tests).
func ServiceMetricValue(name, value string) uint64 {
	if c, ok := serviceCounters[name]; ok {
		return c.Value(value)
	}
	return 0
}
