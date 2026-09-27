// Package jobsstream is the single source of truth for OMNIRA_JOBS
// retention. PILOT.4D3-B1/B2: a reconciler must consume the SAME effective
// MaxAge that configures the stream, or the two drift and either strand work
// (reconciler thinks it has more time than the stream actually keeps) or
// spam duplicate intents (reconciler fires before the stream would ever
// have expired anything). One constant, imported everywhere retention
// matters, makes drift impossible by construction.
package jobsstream

import "time"

// MaxAge is OMNIRA_JOBS' configured retention age. PILOT.4D3-B2: still 0
// (unbounded) — the live stream's actual current config is unchanged by
// this slice. PILOT.4D3-C is the slice that both raises this to a chosen
// bounded value AND wires it into the CreateOrUpdateStream calls in
// internal/worker/routing and internal/worker/delivery, atomically with
// enabling the reconciler — never one without the other.
const MaxAge time.Duration = 0

// ReconciliationGrace is the safety margin added on top of MaxAge before a
// still-queued send job is considered stranded (PILOT.4D3-B1). Measured,
// not invented: a disposable NATS 2.10.29 stream's MaxAge cleanup fired
// ~19ms after the nominal age boundary; 60s is a defensive margin against
// clock/poll granularity, not tied to that number itself.
const ReconciliationGrace = 60 * time.Second
