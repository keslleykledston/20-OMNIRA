#!/usr/bin/env bash
# Mutation test for work pools, automatic distribution and transfer (ADR-0038 phase 4, migration 104): weakens each rule in turn (who may
# edit, who may receive, capacity, queue scope, locks, the pinned authorization, suspended companies); the real-Postgres tests must go red
# every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubdistmut-$$; DB=omnira_test_distmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/hub/distribution/distribute.go internal/hub/distribution/service.go internal/hub/replying/service.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=distmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

mkdb() { # mkdb <migrations dir>
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
run() { # the host toolchain with its warm caches; -p 1: the two packages share this one database, one after the other
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -p 1 -timeout 240s -run 'TestPools_|TestDistribute_|TestPending_|TestHubTransfer_|TestPoolsAPI_' ./internal/hub/distribution ./internal/hub/adapters 2>&1
}
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 2 ]; }

mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -25; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"

killed() {
  if verdict_green "$2"; then echo "FAIL: mutation '$1' SURVIVED"; exit 1; fi
  [ "$(echo "$2" | grep -c -- '--- FAIL')" -ge 1 ] || { echo "$2" | tail -12; echo "FAIL: mutation '$1' was inconclusive (no failing test; build error?)"; exit 1; }
  echo "   killed: $1 ($(echo "$2" | grep -c -- '--- FAIL') failing tests)"
}
N=0
mut() { # mut <name> <file> <from> <to>
  N=$((N+1)); if [ "$N" -le "${SKIP:-0}" ]; then echo "   skipped: $1"; return 0; fi
  python3 - "$WORK/orig" "$2" "$3" "$4" <<'PY'
import sys
orig,f,a,b=sys.argv[1:5]
s=open(orig+"/"+f).read()
assert a in s, "mutation target not found: "+a
open(f,'w').write(s.replace(a,b,1))
PY
  killed "$1" "$(run || true)"
  cp "$WORK/orig/$2" "$2"
}
sqlmut() { # sqlmut <name> <migration file> <from> <to>
  N=$((N+1)); if [ "$N" -le "${SKIP:-0}" ]; then echo "   skipped: $1"; return 0; fi
  rm -rf "$WORK/mig"; mkdir -p "$WORK/mig"; cp migrations/*.sql "$WORK/mig/"
  python3 - "$WORK/mig/$2" "$3" "$4" <<'PY'
import sys
p,f,t=sys.argv[1:4]; s=open(p).read()
assert f in s, "SQL mutation target not found: "+f
open(p,'w').write(s.replace(f,t,1))
PY
  mkdb "$WORK/mig"
  killed "$1" "$(run || true)"
}
DI=internal/hub/distribution/distribute.go; DS=internal/hub/distribution/service.go; RP=internal/hub/replying/service.go
# --- who may edit pools
mut "anybody may edit pools"                               $DS '		if !ok {
			return ErrForbidden
		}
		return fn(c, q)' '		if false && !ok {
			return ErrForbidden
		}
		return fn(c, q)'
mut "another hub's pool can be edited by id"               $DS 'WHERE id = $1 AND hub_id = $2 FOR UPDATE`, pool, hub)' 'WHERE id = $1 FOR UPDATE`, pool, hub)'
mut "a person outside the hub can be a member"             $DS 'if known != len(ids) {' 'if false && known != len(ids) {'
mut "an instance without an active contract is accepted"   $DS "AND status = 'active')\`, hub, in.TenantID)" "AND status IN ('active','suspended','revoked'))\`, hub, in.TenantID)"
mut "another instance's queue is accepted"                 $DS 'if !ok {
					return fmt.Errorf("%w: the queue does not belong to that instance", ErrNotFound)
				}' 'if false && !ok {
					return fmt.Errorf("%w: the queue does not belong to that instance", ErrNotFound)
				}'
mut "capacity is not validated"                            $DS 'if specs[i].MaxOpen < 1 || specs[i].MaxOpen > 500 {' 'if false {'
mut "the same person can be listed twice"                  $DS 'if seen[specs[i].UserID] {' 'if false && seen[specs[i].UserID] {'
mut "a member who stays loses the rotation place"          $DS 'ON CONFLICT (work_pool_id, user_id) DO UPDATE SET max_open = EXCLUDED.max_open`' 'ON CONFLICT (work_pool_id, user_id) DO UPDATE SET max_open = EXCLUDED.max_open, last_assigned_at = NULL`'
mut "creating a pool is not audited"                       $DS '"hub.pool.created"' '"hub.pool.updated"'
# --- who receives
mut "a read-only grant receives"                           $DI 'has_active_hub_access($2, $1, $3, $4, true, true)' 'has_active_hub_access($2, $1, $3, $4, true, false)'
mut "the queue scope of the contract is ignored"           $DI '$2 AND status = '"'"'active'"'"')
	                           AND has_active_hub_access($2, $1, $3, $4, true, true)`, tenant, person, queue, hub)' '$2 AND status = '"'"'active'"'"')
	                           AND has_active_hub_access($2, $1, NULL, $4, false, true)`, tenant, person, queue, hub)'
mut "an inactive account receives"                         $DI "SELECT EXISTS (SELECT 1 FROM users WHERE id = \$2 AND status = 'active')" "SELECT EXISTS (SELECT 1 FROM users WHERE id = \$2)"
mut "the grant is not pinned (a racing revocation loses)"  $DI '		{`SELECT 1 FROM effective_access_grants WHERE hub_id = $1 AND tenant_id = $2 AND user_id = $3 FOR SHARE`, []any{hub, tenant, person}},' ''
mut "capacity is ignored"                                  $DI 'WHERE m.work_pool_id = $1 AND l.load < m.max_open' 'WHERE m.work_pool_id = $1'
mut "the MOST loaded goes first"                           $DI 'ORDER BY l.load, m.last_assigned_at NULLS FIRST, m.user_id`, pool, hub)' 'ORDER BY l.load DESC, m.last_assigned_at NULLS FIRST, m.user_id`, pool, hub)'
mut "the rotation is not updated"                          $DI 'UPDATE work_pool_members SET last_assigned_at = now() WHERE work_pool_id = $1 AND user_id = $2' 'UPDATE work_pool_members SET max_open = max_open WHERE work_pool_id = $1 AND user_id = $2'
mut "the longest wait is not served first"                 $DI 'ORDER BY l.load, m.last_assigned_at NULLS FIRST, m.user_id`, pool, hub)' 'ORDER BY l.load, m.user_id`, pool, hub)'
mut "a manual pool distributes"                            $DI "AND p.distribution = 'round_robin'
			ORDER BY (wi.queue_id IS NULL) LIMIT 1" "AND true
			ORDER BY (wi.queue_id IS NULL) LIMIT 1"
mut "the whole-instance pool wins over the queue's"        $DI 'ORDER BY (wi.queue_id IS NULL) LIMIT 1' 'ORDER BY (wi.queue_id IS NOT NULL) LIMIT 1'
mut "a closed or assigned conversation is reassigned"      $DI 'if status == "closed" || holder != nil {' 'if false {'
mut "a busy conversation is waited for, not skipped"       $DI 'FOR UPDATE SKIP LOCKED' 'FOR UPDATE'
mut "a suspended company is distributed to"                $DI '		if !active {
			return nil
		}' '		if false && !active {
			return nil
		}'
mut "the assignment is not recorded as the system's"       $DI "VALUES (\$1, \$2, NULL, \$3, NULL, 'hub_pool', 'system')" "VALUES (\$1, \$2, NULL, \$3, \$3, 'hub_pool', 'human')"
mut "pending lists suspended companies"                    $DI "JOIN tenants t ON t.id = i.tenant_id AND t.status = 'active'" "JOIN tenants t ON t.id = i.tenant_id"
mut "pending lists manual pools"                           $DI "AND p.distribution = 'round_robin')
			ORDER BY i.last_activity_at" "AND true)
			ORDER BY i.last_activity_at"
# --- transfer
mut "anybody may transfer a conversation they do not hold" $RP '	if assigned == nil || *assigned != actor {
		return nil, ErrNotHolder
	}' '	_ = assigned
'
mut "the person chosen is not proven"                      $RP '			if !ok {
				return ErrTransferTarget
			}' '			if false && !ok {
				return ErrTransferTarget
			}'
mut "somebody may transfer to themselves"                  $RP 'if to != nil && *to == actor {' 'if false {'
mut "a finalized conversation can be transferred"          $RP '	if closed {
		return nil, ErrClosed
	}
	if assigned == nil' '	if false && closed {
		return nil, ErrClosed
	}
	if assigned == nil'
mut "the transfer leaves no history"                       $RP "VALUES (\$1, \$2, \$3, \$4, \$3, \$5)\`, t.Item.TenantID, t.Item.ConversationID, actor, to, reason)" "VALUES (\$1, \$2, \$3, \$4, \$3, 'other')\`, t.Item.TenantID, t.Item.ConversationID, actor, to)"
mut "candidates include read-only people"                  $RP 'has_active_hub_access(u.id, $3, $4, $1, true, true)' 'has_active_hub_access(u.id, $3, $4, $1, true, false)'
mut "candidates include the holder"                        $RP 'AND u.id <> $2 AND has_active_hub_access' 'AND has_active_hub_access'
mut "candidates include inactive accounts"                 $RP "JOIN users u ON u.id = hm.user_id AND u.status = 'active'" "JOIN users u ON u.id = hm.user_id"
# --- SQL
M=000104_hub_work_pools.up.sql
sqlmut "two pools may answer for the same instance"        $M "  CONSTRAINT work_pool_instances_one_pool_per_scope UNIQUE NULLS NOT DISTINCT (hub_id, tenant_id, queue_id)
);" ");"
echo "PASS: every mutation was killed by a failing real-Postgres test ($N mutants)"
