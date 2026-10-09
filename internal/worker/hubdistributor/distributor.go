// Package hubdistributor runs the automatic distribution of the Hub's work pools (ADR-0038 phase 4): every few seconds it looks at the
// open, unassigned conversations that a round-robin pool answers for and lets distribution.Service give each to the least loaded
// member who still holds a live reply grant. It decides nothing itself: the pool, the person, the capacity and the authorization all
// come from persisted rows and are proven again inside the assignment's own transaction (see internal/hub/distribution).
package hubdistributor

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/distribution"
)

type Distributor struct {
	svc   *distribution.Service
	batch int
}

func New(pool *pgxpool.Pool) *Distributor {
	return &Distributor{svc: distribution.New(pool), batch: 50}
}

// RunOnce distributes one batch. It reports how many items it looked at and how many it assigned; one item that fails does not stop
// the others (it is retried on the next round).
func (d *Distributor) RunOnce(ctx context.Context) (tried, assigned int, err error) {
	items, err := d.svc.Pending(ctx, d.batch)
	if err != nil {
		return 0, 0, err
	}
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		tried++
		who, err := d.svc.Distribute(ctx, it.Hub, it.ID)
		if err != nil {
			log.Printf("hub distributor: item %s: %v", it.ID, err)
			continue
		}
		if who != nil {
			assigned++
		}
	}
	return tried, assigned, nil
}

// Run polls until ctx is done.
func (d *Distributor) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tried, assigned, err := d.RunOnce(ctx)
			switch {
			case err != nil:
				log.Printf("hub distributor: %v", err)
			case assigned > 0:
				log.Printf("hub distributor: assigned %d of %d conversation(s)", assigned, tried)
			}
		}
	}
}
