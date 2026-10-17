#!/usr/bin/env bash
# READ-ONLY status of the headless pilot (docs/ops/HEADLESS-PILOT-PLAN.md): flags, installations, the journal, the webhook queue and the conversations an application holds.
# Prints the abort criteria of the runbook with their current value. Changes nothing. Run on the host that has the production containers.
#   scripts/headless-pilot-check.sh
set -uo pipefail
PGC=${PG_CONTAINER:-omnira-postgres}; DB=${PG_DB:-omnira_dev}; OWNER=${PG_OWNER:-omnira}
q() { docker exec -i "$PGC" psql -U "$OWNER" -d "$DB" -X -q -At -F ' | ' -c "$1" 2>&1; }
flag() { docker exec "$1" sh -c 'env | grep -E "^OMNIRA_HEADLESS_(ENABLED|WRITE_ENABLED)=" | tr "\n" " "' 2>/dev/null; }
echo "== flags (container environment)"
echo "api    : $(flag omnira-api)"
echo "worker : $(flag omnira-worker)"
echo "== migrations"
echo "last applied: $(q "select max(version) from schema_migrations")  (expected 000164_service_events_external or later)"
echo "== installations (by status)"
q "select status, count(*) from external_app_installations group by 1 order by 1" | sed 's/^/  /'
echo "  none yet" | head -0
echo "== applications (name | company | status | scopes | lease)"
q "select i.name, t.legal_name, i.status, array_to_string(i.scopes, ','), coalesce(i.lease_seconds::text,'no lease') from external_app_installations i join tenants t on t.id=i.tenant_id order by i.created_at" | sed 's/^/  /'
echo "== abort criteria"
echo "(b) oldest un-numbered fact (s, abort if > 60 sustained): $(q "select coalesce(round(extract(epoch from now() - min(occurred_at))),0) from domain_events where seq is null")"
echo "    facts in the journal: $(q "select count(*) from domain_events")  (retention default 30 days)"
echo "(a) webhook deliveries pending / dead (abort if dead keeps growing): $(q "select count(*) filter (where status='pending'), count(*) filter (where status='dead') from webhook_deliveries")"
echo "    overdue pending (s): $(q "select coalesce(round(extract(epoch from now() - min(next_attempt_at))),0) from webhook_deliveries where status='pending' and next_attempt_at < now()")"
echo "(c) conversations held by an application now: $(q "select count(*) from conversations where control_kind = 'crm_application'")"
echo "    held by an application that is NOT active (must be 0): $(q "select count(*) from conversations c where c.control_kind='crm_application' and not exists (select 1 from external_app_installations i where i.id=c.control_holder_id and i.status='active')")"
echo "    outbound messages of applications that failed with control_lost (last 24h): $(q "select count(*) from messages where sent_by_installation_id is not null and status='failed' and created_at > now() - interval '24 hours'")"
echo "(e) database locks waiting right now (abort if it keeps growing): $(q "select count(*) from pg_stat_activity where wait_event_type='Lock'")"
echo "== commands of applications, last hour (audit)"
q "select action, count(*) from audit_events where action like 'external_app.%' and created_at > now() - interval '1 hour' group by 1 order by 2 desc" | sed 's/^/  /'
echo "== host"
df -h /var | tail -1 | awk '{print "  /var: "$5" used, "$4" free"}'
echo "(d) the 5xx rate of the external API is read from the api metrics (omnira_external_requests_total{outcome=error|unavailable}); see section 4 of the runbook"
