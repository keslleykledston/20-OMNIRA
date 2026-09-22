package routing

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/omnira/omnira/internal/routing/ports"
)

// IAM4.2-B0: routing liveness. Neither mechanism here assigns anything —
// both only re-enqueue job.routing.assign.v1 through the existing
// outbox/NATS/consumer/AssignRoundRobin pipeline (ports.LivenessRepository).
// This file never touches MaxDeliver/backoff, never adds presence to
// eligibility, and does not implement IAM4.2-B1 (routing_require_presence).
const (
	// SweepInterval is how often the safety sweep ticks. Named constant, not
	// a functional limit: the sweep drains every due batch before waiting
	// for the next tick (see Sweep.Tick), so this only bounds how quickly a
	// missed event/restart/race is noticed, never how much work gets done.
	SweepInterval = 60 * time.Second
	// SweepBatchSize bounds one Retrigger call. A tenant/queue with more due
	// conversations than this is walked over several ticks, in
	// routing_retry_at order — bounded, not starved, because every
	// processed row is bumped past every not-yet-processed due row.
	SweepBatchSize = 200
	// SweepRetryBackoff must comfortably exceed the JetStream in-flight
	// window (5s * MaxDeliver(10) = ~50s) so the sweep never re-fires a
	// conversation that is still being retried by the original delivery.
	SweepRetryBackoff = 90 * time.Second
	// WakeupPerQueue bounds how many stale conversations one presence-online
	// event retriggers per queue the reactivated agent belongs to. Small on
	// purpose: this only needs to wake the mechanism, not route everything
	// itself — the atomic AssignRoundRobin decides who actually gets it.
	WakeupPerQueue = 5
)

// presenceTransitionEvent mirrors internal/presence/adapters.TransitionEvent
// (not imported directly, to avoid coupling this package to the presence
// package for one small struct): {tenant_id, agent_profile_id, status,
// occurred_at}, published only on aggregated online/offline transitions,
// never per heartbeat.
type presenceTransitionEvent struct {
	TenantID       uuid.UUID `json:"tenant_id"`
	AgentProfileID uuid.UUID `json:"agent_profile_id"`
	Status         string    `json:"status"`
}

// Sweep is the slow, bounded safety net: it recovers from a missed NATS
// message, a worker restart mid-flight, a race, or any of the wakeup
// conditions this slice does not build yet (queue availability, AgentProfile
// activation, capacity freed) — it is deliberately not coupled to presence
// alone.
type Sweep struct {
	repo ports.LivenessRepository
	// Interval/BatchSize/Backoff are exported so tests can shrink them;
	// production wiring leaves them at the package constants.
	Interval  time.Duration
	BatchSize int
	Backoff   time.Duration
}

func NewSweep(repo ports.LivenessRepository) *Sweep {
	return &Sweep{repo: repo, Interval: SweepInterval, BatchSize: SweepBatchSize, Backoff: SweepRetryBackoff}
}

func (s *Sweep) Run(ctx context.Context) {
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick(ctx)
		}
	}
}

// Tick drains every due batch (not just one) before returning, so a burst of
// stale conversations does not have to wait a full Interval to catch up —
// same shape as internal/presence/application.Reaper.Tick.
func (s *Sweep) Tick(ctx context.Context) {
	for {
		n, err := s.repo.Retrigger(ctx, nil, nil, s.BatchSize, s.Backoff)
		if err != nil {
			log.Printf("routing liveness sweep: retrigger failed: %v", err)
			return
		}
		if n > 0 {
			log.Printf("routing liveness sweep: retriggered %d stale unassigned conversation(s)", n)
		}
		if n < s.BatchSize {
			return
		}
	}
}

// PresenceWakeup subscribes to presence.changed.* (best-effort NATS core,
// same tier as the presence transitions it consumes — a missed message here
// is still caught by Sweep) and, only on an online transition, retriggers a
// bounded number of stale unassigned conversations in the reactivated
// agent's queues. It never routes anything itself — it only wakes the
// existing mechanism up sooner than the next sweep tick.
type PresenceWakeup struct {
	repo ports.LivenessRepository
	nc   *nats.Conn
	sub  *nats.Subscription
	// PerQueue is exported so tests can shrink it; production wiring leaves
	// it at WakeupPerQueue.
	PerQueue int
}

func NewPresenceWakeup(repo ports.LivenessRepository, nc *nats.Conn) *PresenceWakeup {
	return &PresenceWakeup{repo: repo, nc: nc, PerQueue: WakeupPerQueue}
}

// Start subscribes on a stable queue group so that, if this worker is ever
// run with multiple replicas, exactly one of them handles each presence
// transition — avoiding a redundant retrigger storm, not a correctness
// requirement (Retrigger's SKIP LOCKED already makes duplicates harmless).
func (w *PresenceWakeup) Start(ctx context.Context) error {
	sub, err := w.nc.QueueSubscribe("presence.changed.*", "routing-liveness-wakeup", func(msg *nats.Msg) {
		w.handle(ctx, msg.Data)
	})
	if err != nil {
		return err
	}
	w.sub = sub
	return nil
}

func (w *PresenceWakeup) Stop() {
	if w.sub != nil {
		_ = w.sub.Unsubscribe()
	}
}

func (w *PresenceWakeup) handle(ctx context.Context, data []byte) {
	var event presenceTransitionEvent
	if err := json.Unmarshal(data, &event); err != nil {
		log.Printf("routing liveness wakeup: dropping malformed presence event")
		return
	}
	// Only an agent becoming reachable is a reason to wake routing early;
	// an offline transition needs no action here.
	if event.Status != "online" || event.TenantID == uuid.Nil || event.AgentProfileID == uuid.Nil {
		return
	}
	queueIDs, err := w.repo.ActiveQueuesForAgent(ctx, event.TenantID, event.AgentProfileID)
	if err != nil {
		log.Printf("routing liveness wakeup: failed to resolve queues: %v", err)
		return
	}
	tenantID := event.TenantID
	for _, queueID := range queueIDs {
		queueID := queueID
		n, err := w.repo.Retrigger(ctx, &tenantID, &queueID, w.PerQueue, SweepRetryBackoff)
		if err != nil {
			log.Printf("routing liveness wakeup: retrigger failed for queue: %v", err)
			continue
		}
		if n > 0 {
			log.Printf("routing liveness wakeup: retriggered %d conversation(s) after presence online", n)
		}
	}
}
