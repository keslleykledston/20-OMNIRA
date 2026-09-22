// Package domain holds the presence vocabulary frozen by ADR-0010: a boolean
// online/offline signal, aggregated per AgentProfile across sessions/tabs.
// away/busy, skills and analytics are explicitly out of scope for IAM4.2.
package domain

// Status is the only presence signal IAM4.2-A exposes.
type Status string

const (
	StatusOnline  Status = "online"
	StatusOffline Status = "offline"
)
