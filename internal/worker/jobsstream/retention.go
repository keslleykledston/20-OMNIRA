// Package jobsstream is the single source of truth for OMNIRA_JOBS' stream
// policy (PILOT.4D3-C1): Name, Subjects, Storage, Retention, MaxAge,
// MaxBytes, MaxMsgs, Discard and Duplicates all live in config.go, assembled
// by Config() and applied/verified by Ensure(). Nothing outside this package
// may build its own jetstream.StreamConfig for OMNIRA_JOBS — routing and
// delivery consumers receive an already-Ensure'd stream and only manage
// their own durable consumers on it.
package jobsstream

import "time"

// ReconciliationGrace is the safety margin added on top of MaxAge before a
// still-queued send job is considered stranded (PILOT.4D3-B1). Measured,
// not invented: a disposable NATS 2.10.29 stream's MaxAge cleanup fired
// ~19ms after the nominal age boundary; 60s is a defensive margin against
// clock/poll granularity, not tied to that number itself.
const ReconciliationGrace = 60 * time.Second
