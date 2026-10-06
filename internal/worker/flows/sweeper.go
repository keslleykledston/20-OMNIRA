package flows

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/flows/adapters"
	"github.com/omnira/omnira/internal/flows/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

const (
	batch            = 50
	StrandedAfter    = 2 * time.Minute
	defaultSweepTick = 15 * time.Second
)

// Sweeper does the time-driven work: it fires the timeout of waiting runs, gives back conversations the bot holds without
// a run, and frees the run slot of conversations closed meanwhile. Each item is its own transaction in its own tenant, so
// one failure never blocks the rest.
type Sweeper struct {
	pool   *pgxpool.Pool
	repo   *adapters.PostgresFlowRepository
	engine *application.Engine
	now    func() time.Time
	logf   func(string, ...any)
}

func NewSweeper(pool *pgxpool.Pool, repo *adapters.PostgresFlowRepository, engine *application.Engine) *Sweeper {
	return &Sweeper{pool: pool, repo: repo, engine: engine, now: func() time.Time { return time.Now().UTC() }, logf: log.Printf}
}

func (s *Sweeper) WithClock(now func() time.Time) *Sweeper { s.now = now; return s }

type SweepResult struct {
	TimedOut, Released, Cancelled int
}

func (s *Sweeper) admin(ctx context.Context, fn func(context.Context) error) error {
	return platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, fn)
}

// Tick runs one sweep.
func (s *Sweeper) Tick(ctx context.Context) SweepResult {
	var res SweepResult
	now := s.now()

	var due []adapters.DueRun
	if err := s.admin(ctx, func(c context.Context) (err error) { due, err = s.repo.DueRuns(c, now, batch); return }); err != nil {
		s.logf("flows sweeper: listing due runs: %v", err)
	}
	for _, d := range due {
		d := d
		err := platformdb.WithSystemTenantSession(ctx, s.pool, d.TenantID, func(c context.Context) error {
			_, err := s.engine.OnTimeout(c, d.RunID)
			return err
		})
		if err != nil {
			s.logf("flows sweeper: timeout of run %s failed: %v", d.RunID, err)
			continue
		}
		res.TimedOut++
	}

	var stranded []adapters.StrandedConversation
	if err := s.admin(ctx, func(c context.Context) (err error) {
		stranded, err = s.repo.StrandedConversations(c, now.Add(-StrandedAfter), batch)
		return
	}); err != nil {
		s.logf("flows sweeper: listing stranded conversations: %v", err)
	}
	for _, st := range stranded {
		st := st
		err := platformdb.WithSystemTenantSession(ctx, s.pool, st.TenantID, func(c context.Context) error {
			return s.engine.ReleaseStranded(c, st.ConversationID)
		})
		if err != nil {
			s.logf("flows sweeper: releasing conversation %s failed: %v", st.ConversationID, err)
			continue
		}
		res.Released++
	}

	if err := s.admin(ctx, func(c context.Context) error {
		n, err := s.repo.CancelRunsOfClosedConversations(c)
		res.Cancelled = int(n)
		return err
	}); err != nil {
		s.logf("flows sweeper: cancelling runs of closed conversations: %v", err)
	}
	return res
}

// Run sweeps until ctx ends.
func (s *Sweeper) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = defaultSweepTick
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if r := s.Tick(ctx); r != (SweepResult{}) {
				s.logf("flows sweeper: timeouts=%d released=%d cancelled=%d", r.TimedOut, r.Released, r.Cancelled)
			}
		}
	}
}
