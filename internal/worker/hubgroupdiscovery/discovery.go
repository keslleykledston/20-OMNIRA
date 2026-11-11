// Package hubgroupdiscovery lets a new WhatsApp group surface by itself in the Hub's group organizer
// catalog (ADR-0059 addendum), the moment a Hub<->tenant contract delegates 'groups' management AND the
// tenant's own WhatsApp number is already a member of it -- without anyone first clicking "Ativar" on
// the tenant's Groups page. This NEVER turns message reading on for a group: ADR-0015's rule ("a group
// message is only stored once an administrator clicked Ativar") is untouched by design -- see the
// comment on the wa_groups upsert below.
//
// Trust model, same shape as hubprojector/hubdistributor: the (hub, tenant, connection) triples processed
// here come ONLY from a live hub_tenant_service_contracts row with 'groups' in management_scopes, and that
// is re-checked again, structurally, inside hub_autodiscover_catalog_upsert's own SQL (migration 000187)
// at write time -- never trusted from this Go code's own earlier read. A suspended tenant is frozen
// (platformdb.LockTenantActive), exactly like the projector: nothing is written while it is suspended.
//
// Rate/cost control: there is no circuit breaker in front of the WAHA provider anywhere in this codebase
// today, so this job is the only thing standing between a misconfigured interval and hammering WAHA. It
// is driven by a single configurable interval (OMNIRA_HUB_GROUP_AUTODISCOVERY_INTERVAL_SECONDS) and only
// ever looks at the small set of (hub, tenant, connection) triples a live contract actually names -- never
// "every tenant".
package hubgroupdiscovery

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/opsmetrics"
)

// Group is the minimal shape this package needs from a live provider listing: enough to catalog a group
// (its WhatsApp id and name), never anything about its participants or messages.
type Group struct {
	ProviderGroupID string
	Name            string
}

// Directory lists ONE tenant connection's WhatsApp groups, live from the provider. This package never
// imports internals of the groups module to get one: a composition root (apps/*/cmd/*/main.go) wraps the
// existing groups/adapters.Directory into this small interface of its own, the same way
// groups/adapters.WahaDirectory already wraps the channel provider for the groups module itself.
type Directory interface {
	List(ctx context.Context, tenantID, connectionID uuid.UUID) ([]Group, error)
}

// Signals of the autodiscovery job (docs/architecture/OBSERVABILITY.md): freshness and volume, never a
// group name or a JID at any log level -- those are WhatsApp content/identifiers, not operational
// cardinality, and ADR-0059 Gate 5 already set this rule for the rest of the group organizer.
var (
	runsMetric = opsmetrics.NewCounter("omnira_hub_group_autodiscovery_runs_total",
		"Complete passes of the Hub group autodiscovery job by outcome (error = at least one triple failed)", "outcome", "ok", "error")
	triplesMetric = opsmetrics.NewCounter("omnira_hub_group_autodiscovery_triples_total",
		"Eligible (hub, tenant, connection) triples the autodiscovery job processed, by outcome", "outcome", "ok", "error")
	groupsSeenMetric = opsmetrics.NewCounter("omnira_hub_group_autodiscovery_groups_seen_total",
		"Groups seen live from the provider across every triple of a pass", "")
	catalogWrittenMetric = opsmetrics.NewCounter("omnira_hub_group_autodiscovery_catalog_written_total",
		"hub_group_catalog rows the autodiscovery job created or refreshed", "")
	waGroupsCreatedMetric = opsmetrics.NewCounter("omnira_hub_group_autodiscovery_wa_groups_created_total",
		"wa_groups rows the autodiscovery job created (it never enables one; enabling stays a human's explicit action, ADR-0015)", "")
	lastSuccess = opsmetrics.NewGauge("omnira_hub_group_autodiscovery_last_success_timestamp_seconds",
		"Unix time of the last autodiscovery pass that finished without any failing triple")
	lastDuration   = opsmetrics.NewGauge("omnira_hub_group_autodiscovery_last_duration_seconds", "Duration of the last autodiscovery pass")
	intervalMetric = opsmetrics.NewGauge("omnira_hub_group_autodiscovery_interval_seconds", "Configured interval between autodiscovery passes")
)

var providerGroupIDRe = regexp.MustCompile(`^[0-9]{5,20}(-[0-9]{1,20})?@g\.us$`)

type triple struct {
	hub, tenant, connection uuid.UUID
}

// Job runs the autodiscovery pass. Safe for concurrent use (RunOnce/Run never share mutable state beyond
// the pool and the injected Directory, both read-only after New).
type Job struct {
	pool *pgxpool.Pool
	dir  Directory
}

// New builds the job. dir may be nil (WAHA disabled, OMNIRA_WAHA_ENABLED=false): RunOnce then does
// nothing instead of failing, mirroring how the rest of the groups module degrades when WAHA is off.
func New(pool *pgxpool.Pool, dir Directory) *Job {
	return &Job{pool: pool, dir: dir}
}

// Run polls on a fixed interval until ctx ends. The first pass runs immediately, same as the projector.
func (j *Job) Run(ctx context.Context, interval time.Duration) {
	if interval < time.Second {
		interval = 5 * time.Minute
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	intervalMetric.Set(interval.Seconds())
	for {
		started := time.Now()
		err := j.RunOnce(ctx)
		lastDuration.Set(time.Since(started).Seconds())
		if err != nil {
			runsMetric.Inc("error")
			log.Printf("hub group autodiscovery: %v", err)
		} else {
			runsMetric.Inc("ok")
			lastSuccess.SetNow()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// eligibleSQL mirrors the "live" CTE hubprojector.reconcileSQL already uses for "contract is live": a
// hub_tenant_service_contracts row that is active, within its validity window, with 'groups' delegated,
// whose hub is itself active, joined to the tenant's own active WhatsApp (WAHA) connections. This is the
// ONLY source of (hub, tenant, connection) triples this job will ever look at -- never "every tenant".
const eligibleSQL = `
SELECT c.hub_id, c.tenant_id, cc.id
FROM hub_tenant_service_contracts c
JOIN service_hubs h ON h.id = c.hub_id
JOIN channel_connections cc ON cc.tenant_id = c.tenant_id
WHERE c.status = 'active' AND c.valid_from <= now() AND (c.valid_until IS NULL OR c.valid_until > now())
  AND 'groups' = ANY(c.management_scopes)
  AND h.status = 'active'
  AND cc.channel = 'whatsapp' AND cc.provider = 'waha' AND cc.status = 'active'`

func (j *Job) eligibleTriples(ctx context.Context) ([]triple, error) {
	var out []triple
	// A plain system session (no particular tenant): this only READS the global contracts/connections
	// tables, which is why, unlike runTriple below, it does not need LockTenantActive on anything yet.
	err := platformdb.WithTenantSession(ctx, j.pool, uuid.Nil, true, func(c context.Context) error {
		rows, err := platformdb.QuerierFromContext(c, j.pool).Query(c, eligibleSQL)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t triple
			if err := rows.Scan(&t.hub, &t.tenant, &t.connection); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// RunOnce discovers every eligible triple exactly once. One triple failing (a WAHA timeout, a revoked
// contract mid-pass, ...) never stops the others -- same rule as hubdistributor.RunOnce; the error is
// logged and the pass continues, and the joined errors (if any) are returned for Run to log/count once.
func (j *Job) RunOnce(ctx context.Context) error {
	if j.dir == nil {
		return nil
	}
	triples, err := j.eligibleTriples(ctx)
	if err != nil {
		return fmt.Errorf("hubgroupdiscovery: list eligible triples: %w", err)
	}
	var errs []error
	for _, t := range triples {
		if ctx.Err() != nil {
			break
		}
		if err := j.runTriple(ctx, t); err != nil {
			triplesMetric.Inc("error")
			errs = append(errs, fmt.Errorf("hub %s tenant %s connection %s: %w", t.hub, t.tenant, t.connection, err))
			continue
		}
		triplesMetric.Inc("ok")
	}
	return errors.Join(errs...)
}

func (j *Job) runTriple(ctx context.Context, t triple) error {
	groups, err := j.dir.List(ctx, t.tenant, t.connection)
	if err != nil {
		return fmt.Errorf("list groups: %w", err)
	}
	groupsSeenMetric.Add("", uint64(len(groups)))
	return platformdb.WithSystemTenantSession(ctx, j.pool, t.tenant, func(c context.Context) error {
		// ADR-0038: a suspended tenant is frozen, never processed -- same guard hubprojector.run takes
		// before writing anything.
		if active, err := platformdb.LockTenantActive(c, platformdb.QuerierFromContext(c, j.pool), t.tenant); err != nil || !active {
			return err
		}
		q := platformdb.QuerierFromContext(c, j.pool)
		for _, g := range groups {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !providerGroupIDRe.MatchString(g.ProviderGroupID) {
				continue // defensive: the provider should never send anything that is not a WhatsApp group id
			}
			name := g.Name
			if r := []rune(name); len(r) > 200 {
				name = string(r[:200])
			}
			if err := j.upsertWaGroup(c, q, t, g.ProviderGroupID, name); err != nil {
				return err
			}
			if err := j.upsertCatalogEntry(c, q, t, g.ProviderGroupID, name); err != nil {
				return err
			}
		}
		return nil
	})
}

// upsertWaGroup makes sure the group is KNOWN to the tenant (wa_groups), so the existing discovery bridge
// (hub_discover_tenant_groups, migration 000185) and the tenant's own Groups page can already see it --
// but it NEVER sets enabled/enabled_by/enabled_at. ADR-0015 is explicit: a group's messages are only ever
// stored once an administrator clicks "Ativar" (internal/groups/adapters/http.go Handler.Enable); that
// remains the ONLY place that ever sets enabled=true, and it is never called from here.
//
// A plain INSERT works here (no SECURITY DEFINER function needed): wa_groups' RLS (migration 000057) is
// "has_active_membership(tenant_id, current_user_id()) OR is_system_admin()", and
// platformdb.WithSystemTenantSession already set app.is_system_admin=true for this session -- confirmed
// by reading internal/platform/db/session.go and the policy text itself before deciding this.
func (j *Job) upsertWaGroup(ctx context.Context, q platformdb.Querier, t triple, providerGroupID, name string) error {
	var created bool
	err := q.QueryRow(ctx, `
		INSERT INTO wa_groups (tenant_id, channel_connection_id, provider_group_id, name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, channel_connection_id, provider_group_id)
		DO UPDATE SET name = EXCLUDED.name, updated_at = now()
		RETURNING (xmax = 0)`, t.tenant, t.connection, providerGroupID, name).Scan(&created)
	if err != nil {
		return fmt.Errorf("wa_groups upsert: %w", err)
	}
	if created {
		waGroupsCreatedMetric.Add("", 1)
	}
	return nil
}

// upsertCatalogEntry registers the group in the Hub's catalog through the SECURITY DEFINER function
// (migration 000187): hub_group_catalog's RLS has no is_system_admin() bypass by design, so a system job
// cannot write it directly -- the function re-checks the live contract itself, structurally, instead.
//
// A result of zero rows means the contract lost 'groups' (or was revoked/expired) between
// eligibleTriples and this call: that is an expected race under the function's own "fail closed to
// empty, never an error" rule (same convention as hub_discover_tenant_groups), not a bug, so it is
// treated as success-with-nothing-written rather than an error for this triple.
func (j *Job) upsertCatalogEntry(ctx context.Context, q platformdb.Querier, t triple, providerGroupID, name string) error {
	rows, err := q.Query(ctx, `SELECT created FROM hub_autodiscover_catalog_upsert($1, $2, $3, $4, $5)`,
		t.hub, t.tenant, t.connection, providerGroupID, name)
	if err != nil {
		return fmt.Errorf("catalog upsert: %w", err)
	}
	defer rows.Close()
	wrote := false
	for rows.Next() {
		wrote = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("catalog upsert: %w", err)
	}
	if wrote {
		catalogWrittenMetric.Add("", 1)
	}
	return nil
}
