package adapters

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/conversationcontrol"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/opsmetrics"
)

var leaseMetric = opsmetrics.NewCounter("omnira_external_lease_expired_total", "CRM holds freed because the lease ran out", "outcome", "freed", "skipped", "error")

// LeaseGrace is how long after the lease a silent application is still left alone before its conversations are freed.
const LeaseGrace = 120 * time.Second

// LeaseSweepHookAfterDue, when set (tests only), runs between the moment the expired leases are listed and the moment each one is judged under its lock.
var LeaseSweepHookAfterDue func()

// SweepLeases frees the conversations whose application stopped renewing its lease (ADR-0046 §6.1). It is safe to run in several workers: each conversation is
// re-read under its own lock and freed only if the same installation still holds it AND its lease is still expired. Returns how many it freed.
//
// only, when given, limits the sweep to those companies (the worker passes none: it serves every company; tests pass their own so that a sweep never touches the
// holds of another test sharing the database).
func SweepLeases(ctx context.Context, pool *pgxpool.Pool, grace time.Duration, limit int, only ...uuid.UUID) (int, error) {
	var tenants []uuid.UUID
	if len(only) > 0 {
		tenants = only
	}
	rows, err := pool.Query(ctx, `SELECT tenant_id, conversation_id, installation_id FROM external_leases_due(make_interval(secs => $1), $2, $3)`, grace.Seconds(), limit, tenants)
	if err != nil {
		return 0, err
	}
	type due struct {
		tenant, conversation, installation uuid.UUID
		orphan                             bool
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.tenant, &d.conversation, &d.installation); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// holds whose application has no usable credential left (tokens expired or all revoked): they can never be renewed or released by the application
	orows, err := pool.Query(ctx, `SELECT tenant_id, conversation_id, installation_id FROM external_orphan_holds($1, $2)`, limit, tenants)
	if err != nil {
		return 0, err
	}
	for orows.Next() {
		d := due{orphan: true}
		if err := orows.Scan(&d.tenant, &d.conversation, &d.installation); err != nil {
			orows.Close()
			return 0, err
		}
		list = append(list, d)
	}
	orows.Close()
	if err := orows.Err(); err != nil {
		return 0, err
	}
	if LeaseSweepHookAfterDue != nil {
		LeaseSweepHookAfterDue()
	}
	freed := 0
	for _, d := range list {
		var did bool
		err := platformdb.WithSystemTenantSession(ctx, pool, d.tenant, func(ctx context.Context) error {
			q := platformdb.QuerierFromContext(ctx, pool)
			// the installation's gate first (the order everything else uses), then the conversation, then the lease row
			if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(external_installation_gate_key($1))`, d.installation); err != nil {
				return err
			}
			if _, err := q.Exec(ctx, `SELECT set_config('omnira.event_origin', 'system', true)`); err != nil {
				return err
			}
			// the conversation first (conversation -> lease), then the lease as it is NOW (a renewal or a re-claim that got there first wins)
			cur, err := (conversationcontrol.Service{}).Lock(ctx, q, d.tenant, d.conversation)
			if err != nil || !cur.HeldBy(conversationcontrol.KindCRMApplication, d.installation) {
				_, derr := q.Exec(ctx, `DELETE FROM external_conversation_leases WHERE tenant_id=$1 AND conversation_id=$2 AND installation_id=$3`, d.tenant, d.conversation, d.installation)
				return derr // the hold ended some other way: the lease row is just stale
			}
			var stillExpired bool
			reason := "lease_expired"
			if d.orphan {
				reason = "no_credential"
				if err := q.QueryRow(ctx, `
					SELECT NOT EXISTS (SELECT 1 FROM external_app_credentials k WHERE k.tenant_id = $1 AND k.installation_id = $2 AND k.revoked_at IS NULL AND k.expires_at > now())`,
					d.tenant, d.installation).Scan(&stillExpired); err != nil {
					return err
				}
			} else if err := q.QueryRow(ctx, `
				SELECT COALESCE((SELECT l.lease_until + make_interval(secs => $4) < now() FROM external_conversation_leases l
				                  WHERE l.tenant_id = $1 AND l.conversation_id = $2 AND l.installation_id = $3), false)`,
				d.tenant, d.conversation, d.installation, grace.Seconds()).Scan(&stillExpired); err != nil {
				return err
			}
			if !stillExpired {
				return nil
			}
			ok, err := (conversationcontrol.Service{}).ExpireHold(ctx, q, d.tenant, d.conversation, d.installation, reason)
			if err != nil {
				return err
			}
			did = ok
			_, err = q.Exec(ctx, `DELETE FROM external_conversation_leases WHERE tenant_id=$1 AND conversation_id=$2 AND installation_id=$3`, d.tenant, d.conversation, d.installation)
			return err
		})
		switch {
		case err != nil:
			leaseMetric.Inc("error")
		case did:
			freed++
			leaseMetric.Inc("freed")
		default:
			leaseMetric.Inc("skipped")
		}
	}
	return freed, nil
}
