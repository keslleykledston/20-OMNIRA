#!/usr/bin/env bash
# Mutation test for "a suspended company is not served" (ADR-0038, Codex H-02/H-03/M-01) and for the reply write path pinning
# its authorization rows (Codex H1/M1, ADR-0037): weakens each guard in turn; the real-Postgres tests must go red every time
# and green on the original. Throwaway database only. Never edit this file while it runs (bash reads it incrementally).
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubsuspmut-$$; DB=omnira_test_suspmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig"
FILES="internal/hub/replying/service.go internal/platform/db/tenant_active.go internal/inbox/adapters/webhook.go internal/groups/adapters/intake.go internal/worker/delivery/postgres.go internal/worker/delivery/send.go internal/worker/flows/handler.go internal/worker/flows/sweeper.go internal/flows/adapters/postgres_runs.go internal/worker/hubprojector/projector.go internal/worker/routing/postgres.go internal/routing/adapters/liveness_postgres.go internal/intelligence/adapters/job_store.go internal/media/adapters/repository.go internal/media/adapters/analysis_repository.go internal/worker/delivery/reconcile_postgres.go internal/media/application/processor.go internal/media/application/vision.go internal/media/application/transcriber.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=suspmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

mkdb() {
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
RUNRE='TestMediaIsNeitherFetched|TestVisionRunsEvery|TestTranscriptionRunsEvery|TestASuspendedCompanyGetsNoAnalysisJob|TestWhileActiveHolds|TestClaimDoesNotRace|TestAnalysisClaimAndEnqueueDoNotRace|TestAnAIJobClaimDoesNotRace|TestRetrigger_ASuspensionInFlight|TestReconcile_ASuspensionInFlight|TestSweeperWaitsForASuspension|TestASuspendedCompanysJobsAreNotClaimed|TestASuspendedCompanysMediaIsNotClaimed|TestRunnerDoesNotRun|TestRetrigger_SuspendedCompany|TestASuspendedCompanys|TestSweeperDoesNotCancel|TestReconcile_SuspendedCompany|TestHubReply_WriteWaits|TestHubReply_CapabilityAndHappyPath|TestLockTenantActive|TestPostgresDeliveryStateMachine|TestSuspendedCompanysFlows|TestProjector_SuspendedCompany|TestMetaWebhookEndToEnd|TestIntakeDropsGroupMessagesOfASuspendedCompany'
PKGS="./internal/media/application ./internal/worker/routing ./internal/routing/adapters ./internal/intelligence/adapters ./internal/media/adapters ./internal/hub/adapters ./internal/platform/db ./internal/worker/delivery ./internal/worker/flows ./internal/worker/hubprojector ./internal/inbox/adapters ./internal/groups/adapters"
run() { # the host toolchain with its warm module and build caches (a cold container recompiles everything per mutant)
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -run "$RUNRE" $PKGS 2>&1
}
# the verdict is read from the output (the exit status of a pipeline ending in grep is grep's)
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 12 ]; }

mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -25; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"

killed() {
  if verdict_green "$2"; then echo "FAIL: mutation '$1' SURVIVED"; exit 1; fi
  # a build error is not a kill: a real kill has at least one failing TEST
  [ "$(echo "$2" | grep -c -- '--- FAIL')" -ge 1 ] || { echo "$2" | tail -12; echo "FAIL: mutation '$1' was inconclusive (no failing test; build error?)"; exit 1; }
  echo "   killed: $1 ($(echo "$2" | grep -c -- '--- FAIL') failing tests)"
}
N=0
mut() { # mut <name> <file> <from> <to> [<file2> <from2> <to2>]   (a second file is for a defence that exists twice)
  N=$((N+1)); if [ "$N" -le "${SKIP:-0}" ]; then echo "   skipped: $1"; return 0; fi
  python3 - "$WORK/orig" "$2" "$3" "$4" "${5:-}" "${6:-}" "${7:-}" <<'PY'
import sys
orig,f1,a1,b1,f2,a2,b2=sys.argv[1:8]
for f,a,b in ((f1,a1,b1),(f2,a2,b2)):
    if not f: continue
    s=open(orig+"/"+f).read()
    assert a in s, "mutation target not found: "+a
    open(f,'w').write(s.replace(a,b,1))
PY
  killed "$1" "$(run || true)"
  cp "$WORK/orig/$2" "$2"
  if [ -n "${5:-}" ]; then cp "$WORK/orig/$5" "$5"; fi
}
S=internal/hub/replying/service.go
mut "reply does not pin the company row"            $S 'SELECT 1 FROM tenants WHERE id = $1 FOR SHARE' 'SELECT 1 FROM tenants WHERE id = $1'
mut "reply does not pin the hub row"                $S 'SELECT 1 FROM service_hubs WHERE id = $1 FOR SHARE' 'SELECT 1 FROM service_hubs WHERE id = $1'
mut "reply does not pin the contract row"           $S 'tenant_id = $2 FOR SHARE`, []any{t.Item.HubID, t.Item.TenantID}},' 'tenant_id = $2`, []any{t.Item.HubID, t.Item.TenantID}},'
mut "reply does not pin the hub membership row"     $S 'user_id = $2 FOR SHARE`' 'user_id = $2`'
mut "reply does not pin the grant row"              $S 'AND user_id = $3 FOR SHARE`' 'AND user_id = $3`'
mut "a conversation finalized in flight is answered" $S 'if err == nil && closed {' 'if err == nil && closed && false {'
mut "the audit of a sent message names a conversation" $S '"hub.message.sent", "message"' '"hub.message.sent", "conversation"'
H=internal/platform/db/tenant_active.go
mut "the tenant lock is not a lock"                 $H 'WHERE id = $1 FOR SHARE`' 'WHERE id = $1`'
mut "a suspended company counts as active"          $H "SELECT status = 'active' FROM tenants" "SELECT true FROM tenants"
mut "webhook intake records for a suspended company" internal/inbox/adapters/webhook.go 'if !active {' 'if !active && false {'
mut "group intake records for a suspended company"  internal/groups/adapters/intake.go '} else if !active {' '} else if !active && false {'
mut "delivery sends for a suspended company"        internal/worker/delivery/postgres.go 'job.TenantSuspended = !tenantActive' 'job.TenantSuspended = !tenantActive && false'
mut "flow jobs run for a suspended company"         internal/worker/flows/handler.go '		if !active {
			return nil
		}
		return fn(c)' '		_ = active
		return fn(c)'
mut "flow timeouts fire for a suspended company"    internal/worker/flows/sweeper.go 'err != nil || !active {' 'err != nil || (!active && false) {' \
    internal/flows/adapters/postgres_runs.go "JOIN tenants t ON t.id = r.tenant_id AND t.status = 'active'" "JOIN tenants t ON t.id = r.tenant_id"
mut "the projector copies a suspended company"      internal/worker/hubprojector/projector.go 'err != nil || !active {' 'err != nil || (!active && false) {'
mut "routing assigns for a suspended company"        internal/worker/routing/postgres.go '		if !active {
			return nil
		}
		return fn(c)' '		_ = active
		return fn(c)'
mut "the liveness sweep re-triggers a suspended company" internal/routing/adapters/liveness_postgres.go "JOIN tenants t ON t.id = c.tenant_id AND t.status = 'active'" "JOIN tenants t ON t.id = c.tenant_id"
mut "the sweeper tidies a suspended company's runs"  internal/flows/adapters/postgres_runs.go "AND r.tenant_id IN (SELECT id FROM active)" "AND true"
mut "reconciliation re-enqueues a suspended company"  internal/worker/delivery/reconcile_postgres.go "JOIN tenants tn ON tn.id = m.tenant_id AND tn.status = 'active'" "JOIN tenants tn ON tn.id = m.tenant_id"
mut "analysis jobs are created for a suspended company" internal/media/adapters/analysis_repository.go "JOIN tenants tn ON tn.id = mm.tenant_id AND tn.status = 'active'" "JOIN tenants tn ON tn.id = mm.tenant_id"
mut "the liveness sweep races a suspension in flight" internal/routing/adapters/liveness_postgres.go '			  FOR SHARE OF t SKIP LOCKED' ''
mut "the run tidy-up races a suspension in flight"    internal/flows/adapters/postgres_runs.go '		  FOR SHARE OF t
		)' '		)'
mut "reconciliation races a suspension in flight"     internal/worker/delivery/reconcile_postgres.go '			  FOR SHARE OF tn SKIP LOCKED' ''
mut "a suspended company's files are fetched"        internal/media/application/processor.go 'if !whileServing(ctx, p.repo, w.TenantID, func(ctx context.Context) {
		switch w.Status {' 'if !whileServing(ctx, struct{}{}, w.TenantID, func(ctx context.Context) {
		switch w.Status {'
mut "a suspended company's images go to the provider" internal/media/application/vision.go 'if !whileServing(ctx, p.repo, w.TenantID, func(ctx context.Context) { p.process(ctx, w) }) {' 'if !whileServing(ctx, struct{}{}, w.TenantID, func(ctx context.Context) { p.process(ctx, w) }) {'
mut "a suspended company's audio is transcribed"      internal/media/application/transcriber.go 'if !whileServing(ctx, p.repo, w.TenantID, func(ctx context.Context) { p.process(ctx, w) }) {' 'if !whileServing(ctx, struct{}{}, w.TenantID, func(ctx context.Context) { p.process(ctx, w) }) {'
mut "the operation runs outside the gate's lock"      internal/media/application/processor.go '	ran, err := g.WhileActive(ctx, tenant, func(c context.Context) error {
		fn(c)
		return nil
	})' '	_ = g
	fn(ctx)
	ran, err := true, error(nil)'
# NOT a mutant: "the gate fails open when it cannot tell" is equivalent: fn only ever runs INSIDE WhileActive, so a failing gate cannot
# run anything whatever whileServing returns (fail closed by construction; TestMediaIsNeitherFetchedNorScanned covers the failing gate).
mut "the repository gate runs for a suspended company" internal/media/adapters/repository.go 'if err != nil || !active {
			return err
		}
		ran = true' 'if err != nil || (!active && false) {
			return err
		}
		ran = true'
mut "media of a suspended company is claimed"        internal/media/adapters/repository.go "JOIN tenants t ON t.id = mm.tenant_id AND t.status = 'active'" "JOIN tenants t ON t.id = mm.tenant_id"
mut "the media claim races a suspension in flight"   internal/media/adapters/repository.go '			  FOR SHARE OF t SKIP LOCKED' ''
mut "analysis jobs of a suspended company are claimed" internal/media/adapters/analysis_repository.go "JOIN tenants t ON t.id = a.tenant_id AND t.status = 'active'" "JOIN tenants t ON t.id = a.tenant_id"
mut "the analysis claim races a suspension in flight" internal/media/adapters/analysis_repository.go '			  FOR SHARE OF t SKIP LOCKED' ''
mut "analysis jobs are created behind a suspension in flight" internal/media/adapters/analysis_repository.go '			FOR SHARE OF tn SKIP LOCKED
' ''
mut "AI jobs of a suspended company are claimed"     internal/intelligence/adapters/job_store.go "JOIN tenants t ON t.id = j.tenant_id AND t.status = 'active'" "JOIN tenants t ON t.id = j.tenant_id"
mut "the AI claim races a suspension in flight"      internal/intelligence/adapters/job_store.go '			  FOR SHARE OF t SKIP LOCKED' ''
mut "the provider is asked for an id for a suspended company" internal/worker/delivery/postgres.go '		if !active {
			return ErrTenantSuspended
		}
		candidate, err := generate(ctx)' '		if !active && false {
			return ErrTenantSuspended
		}
		candidate, err := generate(ctx)'
echo "PASS: every mutant was killed"
