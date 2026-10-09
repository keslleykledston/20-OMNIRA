// Package distribution is the Hub's answer to "who attends which instance" (ADR-0038 phase 4): work pools (groups of the hub's
// agents), the instances (optionally one queue) each pool answers for, a per-person capacity, and the automatic distribution of
// a new conversation among the pool's members.
//
// A pool GRANTS nothing. Distribution only chooses among people who already hold a live, reply-capable grant on the instance
// (has_active_hub_access asked again, inside the assignment's own transaction, for the person chosen), never one who lost it, and
// never for a suspended company. Pools are administered only by an active admin of the hub, proven in the writing transaction.
package distribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/authority"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

var (
	// ErrForbidden: not an active admin of this hub (the HTTP layer answers a uniform 404).
	ErrForbidden = errors.New("distribution: forbidden")
	// ErrInvalid: well-formed but refused (422).
	ErrInvalid = errors.New("distribution: invalid request")
	// ErrNotFound: the pool/instance/person named is not part of this hub (404).
	ErrNotFound = errors.New("distribution: not found")
	// ErrConflict: another pool already answers for that instance/queue (409).
	ErrConflict = errors.New("distribution: conflict")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

const (
	// Manual: nothing is assigned by itself, people claim. RoundRobin: a new conversation goes to the least loaded available member.
	Manual     = "manual"
	RoundRobin = "round_robin"

	MaxOpenDefault = 10
	maxMembers     = 200
	maxInstances   = 500
)

// Service is safe for concurrent use.
type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// ------------------------------------------------------------------ read model

type Member struct {
	UserID  uuid.UUID `json:"user_id"`
	Email   string    `json:"email"`
	Name    string    `json:"name"`
	MaxOpen int       `json:"max_open"`
	// Load: conversations of this hub's instances assigned to the person and not finished.
	Load int `json:"load"`
}

type Instance struct {
	TenantID uuid.UUID  `json:"tenant_id"`
	Name     string     `json:"name"`
	QueueID  *uuid.UUID `json:"queue_id,omitempty"`
}

type Pool struct {
	ID           uuid.UUID  `json:"id"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Distribution string     `json:"distribution"`
	Members      []Member   `json:"members"`
	Instances    []Instance `json:"instances"`
}

func (s *Service) tx(ctx context.Context, actor, hub uuid.UUID, fn func(c context.Context, q platformdb.Querier) error) error {
	if actor == uuid.Nil || hub == uuid.Nil {
		return ErrForbidden
	}
	return platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(c context.Context) error {
		q := platformdb.QuerierFromContext(c, s.pool)
		ok, err := authority.LockHubAdmin(c, q, hub, actor)
		if err != nil {
			return err
		}
		if !ok {
			return ErrForbidden
		}
		return fn(c, q)
	})
}

// loads counts, per person, the unfinished conversations assigned to them in any instance of the hub.
func loads(ctx context.Context, q platformdb.Querier, hub uuid.UUID) (map[uuid.UUID]int, error) {
	rows, err := q.Query(ctx, `
		SELECT c.assigned_to_user_id, count(*)
		FROM conversations c
		WHERE c.assigned_to_user_id IS NOT NULL AND c.status <> 'closed'
		  AND c.tenant_id IN (SELECT tenant_id FROM hub_tenant_service_contracts WHERE hub_id = $1)
		GROUP BY 1`, hub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]int{}
	for rows.Next() {
		var u uuid.UUID
		var n int
		if err := rows.Scan(&u, &n); err != nil {
			return nil, err
		}
		out[u] = n
	}
	return out, rows.Err()
}

func (s *Service) List(ctx context.Context, actor, hub uuid.UUID) ([]Pool, error) {
	out := []Pool{}
	err := s.tx(ctx, actor, hub, func(c context.Context, q platformdb.Querier) error {
		var err error
		out, err = list(c, q, hub)
		return err
	})
	return out, err
}

func list(c context.Context, q platformdb.Querier, hub uuid.UUID) ([]Pool, error) {
	pools := []Pool{}
	idx := map[uuid.UUID]int{}
	rows, err := q.Query(c, `SELECT id, name, COALESCE(description, ''), distribution FROM work_pools WHERE hub_id = $1 ORDER BY lower(name), id`, hub)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p Pool
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Distribution); err != nil {
			rows.Close()
			return nil, err
		}
		p.Members, p.Instances = []Member{}, []Instance{}
		idx[p.ID] = len(pools)
		pools = append(pools, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	load, err := loads(c, q, hub)
	if err != nil {
		return nil, err
	}
	mrows, err := q.Query(c, `
		SELECT m.work_pool_id, m.user_id, COALESCE(u.email, ''), COALESCE(u.display_name, ''), m.max_open
		FROM work_pool_members m JOIN users u ON u.id = m.user_id
		WHERE m.hub_id = $1 ORDER BY lower(COALESCE(u.email, '')), m.user_id`, hub)
	if err != nil {
		return nil, err
	}
	for mrows.Next() {
		var pool uuid.UUID
		var m Member
		if err := mrows.Scan(&pool, &m.UserID, &m.Email, &m.Name, &m.MaxOpen); err != nil {
			mrows.Close()
			return nil, err
		}
		m.Load = load[m.UserID]
		if i, ok := idx[pool]; ok {
			pools[i].Members = append(pools[i].Members, m)
		}
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return nil, err
	}
	irows, err := q.Query(c, `
		SELECT wi.work_pool_id, wi.tenant_id, COALESCE(NULLIF(t.trade_name, ''), t.legal_name), wi.queue_id
		FROM work_pool_instances wi JOIN tenants t ON t.id = wi.tenant_id
		WHERE wi.hub_id = $1 ORDER BY lower(COALESCE(NULLIF(t.trade_name, ''), t.legal_name)), wi.tenant_id, wi.queue_id NULLS FIRST`, hub)
	if err != nil {
		return nil, err
	}
	for irows.Next() {
		var pool uuid.UUID
		var in Instance
		if err := irows.Scan(&pool, &in.TenantID, &in.Name, &in.QueueID); err != nil {
			irows.Close()
			return nil, err
		}
		if i, ok := idx[pool]; ok {
			pools[i].Instances = append(pools[i].Instances, in)
		}
	}
	irows.Close()
	return pools, irows.Err()
}

// ------------------------------------------------------------------ writes

func audit(c context.Context, q platformdb.Querier, actor uuid.UUID, action string, pool uuid.UUID, meta map[string]any) error {
	meta["via"] = "omnira-work-pools"
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = q.Exec(c, `INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata)
	                    VALUES ($1, NULL, $2, $3, 'work_pool', $4, 'success', $5, $6)`,
		uuid.New(), actor, action, pool.String(), uuid.NewString(), raw)
	return err
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 200 {
		return "", invalid("the name must have 1-200 characters")
	}
	return name, nil
}

func cleanDistribution(d string) (string, error) {
	if d == "" {
		return Manual, nil
	}
	if d != Manual && d != RoundRobin {
		return "", invalid("distribution must be manual or round_robin")
	}
	return d, nil
}

func (s *Service) Create(ctx context.Context, actor, hub uuid.UUID, name, distribution string) (Pool, error) {
	name, err := cleanName(name)
	if err != nil {
		return Pool{}, err
	}
	if distribution, err = cleanDistribution(distribution); err != nil {
		return Pool{}, err
	}
	var out Pool
	err = s.tx(ctx, actor, hub, func(c context.Context, q platformdb.Querier) error {
		var id uuid.UUID
		if err := q.QueryRow(c, `INSERT INTO work_pools (hub_id, name, distribution) VALUES ($1, $2, $3) RETURNING id`, hub, name, distribution).Scan(&id); err != nil {
			return err
		}
		if err := audit(c, q, actor, "hub.pool.created", id, map[string]any{"hub_id": hub, "name": name, "distribution": distribution}); err != nil {
			return err
		}
		out = Pool{ID: id, Name: name, Distribution: distribution, Members: []Member{}, Instances: []Instance{}}
		return nil
	})
	return out, err
}

// lockHub serializes everything that decides WHO gets automatic conversations in this hub: the distribution itself and every edit of a pool's
// members, capacities and instances take the same per-hub lock for the rest of their transaction, so an edit can neither be overtaken by an
// assignment that already read the old members (Codex review) nor overshoot a capacity that was just lowered. Manual claims and transfers do
// not take it: capacity is a soft limit of the AUTOMATIC distribution.
func lockHub(c context.Context, q platformdb.Querier, hub uuid.UUID) error {
	_, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended('hub-distribution:' || $1::text, 0))`, hub)
	return err
}

// lockPool takes the hub lock and then the pool row (of THIS hub), so two edits of one pool serialize with each other and with distribution.
func lockPool(c context.Context, q platformdb.Querier, hub, pool uuid.UUID) error {
	if err := lockHub(c, q, hub); err != nil {
		return err
	}
	var id uuid.UUID
	err := q.QueryRow(c, `SELECT id FROM work_pools WHERE id = $1 AND hub_id = $2 FOR UPDATE`, pool, hub).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: no such pool in this hub", ErrNotFound)
	}
	return err
}

func (s *Service) Update(ctx context.Context, actor, hub, pool uuid.UUID, name, distribution *string) error {
	if name == nil && distribution == nil {
		return invalid("nothing to change")
	}
	return s.tx(ctx, actor, hub, func(c context.Context, q platformdb.Querier) error {
		if err := lockPool(c, q, hub, pool); err != nil {
			return err
		}
		meta := map[string]any{"hub_id": hub}
		if name != nil {
			n, err := cleanName(*name)
			if err != nil {
				return err
			}
			if _, err := q.Exec(c, `UPDATE work_pools SET name = $2, updated_at = now() WHERE id = $1`, pool, n); err != nil {
				return err
			}
			meta["name"] = n
		}
		if distribution != nil {
			d, err := cleanDistribution(*distribution)
			if err != nil {
				return err
			}
			if _, err := q.Exec(c, `UPDATE work_pools SET distribution = $2, updated_at = now() WHERE id = $1`, pool, d); err != nil {
				return err
			}
			meta["distribution"] = d
		}
		return audit(c, q, actor, "hub.pool.updated", pool, meta)
	})
}

func (s *Service) Delete(ctx context.Context, actor, hub, pool uuid.UUID) error {
	return s.tx(ctx, actor, hub, func(c context.Context, q platformdb.Querier) error {
		if err := lockPool(c, q, hub, pool); err != nil {
			return err
		}
		if _, err := q.Exec(c, `DELETE FROM work_pools WHERE id = $1`, pool); err != nil {
			return err
		}
		return audit(c, q, actor, "hub.pool.deleted", pool, map[string]any{"hub_id": hub})
	})
}

type MemberSpec struct {
	UserID  uuid.UUID `json:"user_id"`
	MaxOpen int       `json:"max_open"`
}

// SetMembers replaces the pool's members. Everyone must belong to THIS hub; a person who stays keeps their place in the rotation.
func (s *Service) SetMembers(ctx context.Context, actor, hub, pool uuid.UUID, specs []MemberSpec) error {
	if len(specs) > maxMembers {
		return invalid("a pool has at most %d members", maxMembers)
	}
	seen := map[uuid.UUID]bool{}
	ids := make([]uuid.UUID, 0, len(specs))
	for i := range specs {
		if specs[i].UserID == uuid.Nil {
			return invalid("every member needs a user_id")
		}
		if seen[specs[i].UserID] {
			return invalid("a person can be listed only once")
		}
		seen[specs[i].UserID] = true
		if specs[i].MaxOpen == 0 {
			specs[i].MaxOpen = MaxOpenDefault
		}
		if specs[i].MaxOpen < 1 || specs[i].MaxOpen > 500 {
			return invalid("max_open must be between 1 and 500")
		}
		ids = append(ids, specs[i].UserID)
	}
	return s.tx(ctx, actor, hub, func(c context.Context, q platformdb.Querier) error {
		if err := lockPool(c, q, hub, pool); err != nil {
			return err
		}
		var known int
		if err := q.QueryRow(c, `SELECT count(*) FROM hub_memberships WHERE hub_id = $1 AND user_id = ANY($2)`, hub, ids).Scan(&known); err != nil {
			return err
		}
		if known != len(ids) {
			return fmt.Errorf("%w: every member must belong to this hub", ErrNotFound)
		}
		if _, err := q.Exec(c, `DELETE FROM work_pool_members WHERE work_pool_id = $1 AND NOT (user_id = ANY($2))`, pool, ids); err != nil {
			return err
		}
		for _, m := range specs {
			if _, err := q.Exec(c, `INSERT INTO work_pool_members (work_pool_id, hub_id, user_id, max_open) VALUES ($1, $2, $3, $4)
			                        ON CONFLICT (work_pool_id, user_id) DO UPDATE SET max_open = EXCLUDED.max_open`, pool, hub, m.UserID, m.MaxOpen); err != nil {
				return err
			}
		}
		return audit(c, q, actor, "hub.pool.members_set", pool, map[string]any{"hub_id": hub, "members": len(specs)})
	})
}

type InstanceSpec struct {
	TenantID uuid.UUID  `json:"tenant_id"`
	QueueID  *uuid.UUID `json:"queue_id,omitempty"`
}

// SetInstances replaces what the pool answers for. Each instance needs an ACTIVE contract with this hub, a queue must belong to its
// instance, and at most one pool answers for a given (instance, queue).
func (s *Service) SetInstances(ctx context.Context, actor, hub, pool uuid.UUID, specs []InstanceSpec) error {
	if len(specs) > maxInstances {
		return invalid("a pool answers for at most %d instances", maxInstances)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].TenantID.String() < specs[j].TenantID.String() })
	return s.tx(ctx, actor, hub, func(c context.Context, q platformdb.Querier) error {
		if err := lockPool(c, q, hub, pool); err != nil {
			return err
		}
		for _, in := range specs {
			if in.TenantID == uuid.Nil {
				return invalid("every instance needs a tenant_id")
			}
			var ok bool
			if err := q.QueryRow(c, `SELECT EXISTS (SELECT 1 FROM hub_tenant_service_contracts WHERE hub_id = $1 AND tenant_id = $2 AND status = 'active')`, hub, in.TenantID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%w: instance %s has no active contract with this hub", ErrNotFound, in.TenantID)
			}
			if in.QueueID != nil {
				if err := q.QueryRow(c, `SELECT EXISTS (SELECT 1 FROM queues WHERE tenant_id = $1 AND id = $2)`, in.TenantID, *in.QueueID).Scan(&ok); err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("%w: the queue does not belong to that instance", ErrNotFound)
				}
			}
		}
		if _, err := q.Exec(c, `DELETE FROM work_pool_instances WHERE work_pool_id = $1`, pool); err != nil {
			return err
		}
		for _, in := range specs {
			if _, err := q.Exec(c, `INSERT INTO work_pool_instances (work_pool_id, hub_id, tenant_id, queue_id) VALUES ($1, $2, $3, $4)`, pool, hub, in.TenantID, in.QueueID); err != nil {
				var pg *pgconn.PgError
				if errors.As(err, &pg) && pg.Code == "23505" {
					return fmt.Errorf("%w: another pool already answers for that instance (or queue)", ErrConflict)
				}
				return err
			}
		}
		return audit(c, q, actor, "hub.pool.instances_set", pool, map[string]any{"hub_id": hub, "instances": len(specs)})
	})
}
