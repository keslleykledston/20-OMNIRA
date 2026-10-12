#!/usr/bin/env bash
# Cost of the delegated-context policies on a database with real volume (ADR-0040 phases 04c/04d: "EXPLAIN das políticas novas").
# Throwaway Postgres (production migrator, non-superuser application role). Seeds two big instances (A, B: 30k conversations, ~300k messages each, 5 queues),
# then times the hot read paths of the inbox AS THE APPLICATION ROLE under RLS, in three sessions:
#   member    - a member of A (the policies of before the Hub: acting_hub IS NULL)
#   delegate  - a Hub agent attending A through the delegated context (restrictive pins + domain policies + queue scope through the grant)
#   scoped    - the same agent with a contract whose service_scope names 2 of the 5 queues
# Prints the median execution time of EXPLAIN (ANALYZE, BUFFERS) over 5 runs and the shared-buffer hits, and FAILS if a delegated read is slower than FACTOR
# (default 3x) the member's when the member's takes at least 20 ms, or slower than ABS_MS (default 250 ms; 25 ms for the point reads) in absolute terms. A
# point read pays a fixed ~5 ms for the relationship lookup (acting_scope), which is why it has its own absolute limit instead of a ratio.
# It proves cost, not correctness. Findings it made: with the policies of 108-113 the list took 10.3 s (30k conversations) because every row ran a six-table
# function up to three times, and PostgreSQL then JIT-compiled the statement (1.2 s) because the nested calls made it look expensive; migration 114 fixes both.
#
# CTX_MODE=off|measure|enforce (ADR-0042) runs the same sessions with the database in that verification mode and every transaction opening with a SIGNED context (ctx_begin): compare
# the three outputs to see what the signed context costs. Without it the sessions only set the legacy settings, as before.
#
# Usage: scripts/bench-hub-policies.sh        (CONV=30000 MSGS=10 FACTOR=5 ABS_MS=250 to change; KEEP=1 leaves the seeded database running to run EXPLAIN by hand)
# HUB_LIST_MS=600 HUB_COUNT_MS=1500 limit the Hub's own list and counters (migration 119: they took 5.6 s and 8.1 s at 30 000 items before it).
set -euo pipefail
cd "$(dirname "$0")/.."
CONV=${CONV:-30000}; MSGS=${MSGS:-10}; FACTOR=${FACTOR:-3}; ABS_MS=${ABS_MS:-250}
NAME=omnira-hubbench-$$; DB=omnira_test_bench
trap '[ -n "${KEEP:-}" ] || docker rm -fv "$NAME" >/dev/null 2>&1 || true' EXIT
docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=hubbench-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine \
  -c shared_buffers=256MB -c fsync=off -c synchronous_commit=off -c full_page_writes=off >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)
docker exec "$NAME" psql -U omnira -d postgres -X -q -c "CREATE DATABASE $DB" >/dev/null
docker run --rm --network "container:$NAME" -v "$PWD/migrations:/migrations:ro" -v "$PWD/tools:/tools:ro" \
  -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
own() { docker exec -i "$NAME" psql -U omnira -d $DB -X -q -At -v ON_ERROR_STOP=1 "$@"; }
CTX_KEY=b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2
ctx_begin_sql() { # ctx_begin_sql <user uuid> -> the challenge and the answer signed over it with the bench's own key; empty without CTX_MODE
  [ -n "${CTX_MODE:-}" ] || return 0
  printf "\nSELECT ctx_challenge() AS nonce \\gset\nSELECT ctx_begin(p || '|' || encode(hmac(convert_to(p, 'UTF8'), decode('%s','hex'), 'sha256'), 'hex')) FROM (SELECT format('b1|%%s|0|%%s', '%s', :'nonce') AS p) x;\n" "$CTX_KEY" "$1"
}
if [ -n "${CTX_MODE:-}" ]; then
  own -c "SELECT ctx_install_key(decode('$CTX_KEY','hex'))" >/dev/null; own -c "SELECT ctx_set_mode('$CTX_MODE')" >/dev/null
  echo "== session context mode: $CTX_MODE (signed contexts)"
fi
app() { docker exec -i "$NAME" env PGPASSWORD=omnira_app psql -h 127.0.0.1 -U omnira_app -d $DB -X -q -At -v ON_ERROR_STOP=1 "$@"; }

echo "== seed: 2 instances x $CONV conversations x $MSGS messages"
own <<SQL
SELECT set_config('bench.conv', '$CONV', false), set_config('bench.msgs', '$MSGS', false) \gset
INSERT INTO service_hubs (id, name) VALUES ('00000000-0000-0000-0000-0000000000b0', 'Bench Hub');
INSERT INTO tenants (id, legal_name, status) VALUES ('00000000-0000-0000-0000-00000000000a', 'Bench A', 'active'), ('00000000-0000-0000-0000-00000000000b', 'Bench B', 'active');
INSERT INTO users (id, external_subject) VALUES ('00000000-0000-0000-0000-0000000000a1', 'bench-member'), ('00000000-0000-0000-0000-0000000000a2', 'bench-delegate');
INSERT INTO memberships (tenant_id, user_id, role_id) SELECT '00000000-0000-0000-0000-00000000000a', '00000000-0000-0000-0000-0000000000a1', id FROM roles WHERE tenant_id IS NULL AND key = 'tenant_agent' LIMIT 1;
INSERT INTO hub_memberships (hub_id, user_id, role_id) SELECT '00000000-0000-0000-0000-0000000000b0', '00000000-0000-0000-0000-0000000000a2', id FROM roles WHERE tenant_id IS NULL AND key = 'hub_agent' LIMIT 1;
INSERT INTO queues (id, tenant_id, name) SELECT ('00000000-0000-0000-0001-0000000000' || l || q)::uuid, t, 'q' || q FROM generate_series(1,5) q, (VALUES ('00000000-0000-0000-0000-00000000000a'::uuid, 'a'), ('00000000-0000-0000-0000-00000000000b'::uuid, 'b')) v(t, l);
SQL
for T in a b; do
TID=00000000-0000-0000-0000-00000000000$T
own <<SQL
INSERT INTO contacts (id, tenant_id, display_name, phone_e164)
  SELECT md5('c$T' || g)::uuid, '$TID', 'Cliente ' || g, '+55' || lpad((CASE WHEN '$T'='a' THEN 1000000000 ELSE 2000000000 END + g)::text, 11, '0') FROM generate_series(1, $CONV) g;
INSERT INTO conversations (id, tenant_id, contact_id, queue_id, status)
  SELECT md5('v$T' || g)::uuid, '$TID', md5('c$T' || g)::uuid, ('00000000-0000-0000-0001-0000000000$T' || (1 + g % 5))::uuid, CASE WHEN g % 3 = 0 THEN 'closed' ELSE 'open' END FROM generate_series(1, $CONV) g;
INSERT INTO messages (tenant_id, conversation_id, direction, body, created_at)
  SELECT '$TID', md5('v$T' || g)::uuid, CASE WHEN m % 2 = 0 THEN 'inbound' ELSE 'outbound' END, 'mensagem ' || m, now() - ((g % 5000) * interval '1 minute') - (m * interval '10 seconds')
  FROM generate_series(1, $CONV) g, generate_series(1, $MSGS) m;
INSERT INTO hub_inbox_items (hub_id, tenant_id, conversation_id, queue_id)
  SELECT '00000000-0000-0000-0000-0000000000b0', '$TID', id, queue_id FROM conversations WHERE tenant_id = '$TID';
SQL
done
own <<SQL
INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, valid_from, delegable_permissions)
  VALUES ('00000000-0000-0000-0002-00000000000a', '00000000-0000-0000-0000-0000000000b0', '00000000-0000-0000-0000-00000000000a', now() - interval '1 day',
          ARRAY['conversation.read','conversation.claim','conversation.reply','media.read','contact.read','contact.classify','account.read','ticket.read','ticket.create','conversation.transfer','contact.note']),
         ('00000000-0000-0000-0002-00000000000b', '00000000-0000-0000-0000-0000000000b0', '00000000-0000-0000-0000-00000000000b', now() - interval '1 day', '{}');
INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id, valid_from, can_reply, permissions)
  VALUES ('00000000-0000-0000-0000-0000000000b0', '00000000-0000-0000-0000-0000000000a2', '00000000-0000-0000-0000-00000000000a', '00000000-0000-0000-0002-00000000000a', now() - interval '1 day', true,
          ARRAY['conversation.read','conversation.claim','conversation.reply','media.read','contact.read','contact.classify','account.read','ticket.read','ticket.create','conversation.transfer','contact.note']);
INSERT INTO contact_notes (tenant_id, contact_id, body, created_by_user_id)
  SELECT '00000000-0000-0000-0000-00000000000a', md5('ca' || g)::uuid, 'nota ' || g, '00000000-0000-0000-0000-0000000000a1' FROM generate_series(1, 5000) g;
ANALYZE;
SQL
echo "   rows: $(own -c "SELECT (SELECT count(*) FROM conversations) || ' conversations, ' || (SELECT count(*) FROM messages) || ' messages'")"

TA=00000000-0000-0000-0000-00000000000a; HUB=00000000-0000-0000-0000-0000000000b0
MEMBER=00000000-0000-0000-0000-0000000000a1; DELEGATE=00000000-0000-0000-0000-0000000000a2
CONVA=$(own -c "SELECT md5('va7')::uuid"); CONTA=$(own -c "SELECT md5('ca7')::uuid")
Q_LIST="SELECT c.id,c.status,co.display_name,t.status,lm.created_at,left(coalesce(lm.body,''),160)
  FROM conversations c LEFT JOIN contacts co ON co.id=c.contact_id AND co.tenant_id=c.tenant_id
  LEFT JOIN LATERAL (SELECT tk.status FROM tickets tk WHERE tk.conversation_id=c.id AND tk.tenant_id=c.tenant_id AND tk.status IN ('open','in_progress','waiting') ORDER BY tk.created_at DESC LIMIT 1) t ON true
  LEFT JOIN LATERAL (SELECT m.direction,m.body,m.created_at FROM messages m WHERE m.tenant_id=c.tenant_id AND m.conversation_id=c.id ORDER BY m.created_at DESC,m.id DESC LIMIT 1) lm ON true
  WHERE c.tenant_id='$TA' AND c.status='open' ORDER BY COALESCE(lm.created_at,c.created_at) DESC,c.id DESC LIMIT 50"
Q_MSGS="SELECT id,direction,body,created_at FROM messages WHERE tenant_id='$TA' AND conversation_id='$CONVA' ORDER BY created_at DESC,id DESC LIMIT 50"
Q_CONTACT="SELECT id,display_name,phone_e164,kind FROM contacts WHERE tenant_id='$TA' AND id='$CONTA'"
Q_COUNT="SELECT count(*) FROM conversations WHERE tenant_id='$TA' AND status='open'"
Q_NOTES="SELECT id,body FROM contact_notes WHERE tenant_id='$TA' AND contact_id='$CONTA' ORDER BY created_at DESC LIMIT 200"

# bench <mode> <user> <enter 0|1> <query>  -> "median_ms buffers"
bench() {
  local mode=$1 user=$2 enter=$3 q=$4 sql="" i
  for i in 1 2 3 4 5 6; do
    sql+="BEGIN; $(ctx_begin_sql "$user") SELECT set_config('app.current_user_id','$user',true);"
    [ "$enter" = 1 ] && sql+="SELECT lock_served_tenant('$TA','$user','$HUB');"
    sql+="EXPLAIN (ANALYZE, BUFFERS, TIMING OFF, FORMAT JSON) $q; ROLLBACK;"$'\n'
  done
  printf '%s' "$sql" | app | python3 -c '
import sys,json,re
t=sys.stdin.read()
runs=[]
for m in re.finditer(r"(\[\s*\{.*?\}\s*\])\s*$", t, re.S|re.M):
    pass
# every EXPLAIN prints one JSON array; split on the closing bracket line
chunks=re.findall(r"\[\s*\{.*?\n\]", t, re.S)
for c in chunks[1:]:   # first run is warm-up
    d=json.loads(c)[0]
    b=d["Plan"].get("Shared Hit Blocks",0)+d["Plan"].get("Shared Read Blocks",0)
    runs.append((d["Execution Time"], b))
runs.sort()
print("%.2f %d" % runs[len(runs)//2])
'
}
set_scope() { # set_scope narrow|none
  if [ "$1" = narrow ]; then own -c "UPDATE hub_tenant_service_contracts SET service_scope = '{\"queue_ids\":[\"00000000-0000-0000-0001-0000000000a1\",\"00000000-0000-0000-0001-0000000000a2\"]}'::jsonb WHERE id = '00000000-0000-0000-0002-00000000000a'" >/dev/null
  else own -c "UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb WHERE id = '00000000-0000-0000-0002-00000000000a'" >/dev/null; fi
}
echo "== sanity: what each session sees (conversations of A / of B)"
for mode in "member $MEMBER 0" "delegate $DELEGATE 1"; do
  set -- $mode
  lock=""; [ "$3" = 1 ] && lock="SELECT lock_served_tenant('$TA','$2','$HUB');"
  printf '   %-9s' "$1"
  printf '%s' "BEGIN; $(ctx_begin_sql "$2") SELECT set_config('app.current_user_id','$2',true); $lock SELECT count(*) FILTER (WHERE tenant_id='$TA') || ' / ' || count(*) FILTER (WHERE tenant_id<>'$TA') FROM conversations; ROLLBACK;" | app | tail -1
done

printf '\n%-22s %-10s %12s %12s\n' query session median_ms buffers
FAIL=0
declare -A MED
for qn in LIST MSGS CONTACT COUNT NOTES; do
  q_var="Q_$qn"; q="${!q_var}"
  set_scope none
  read -r mm mb < <(bench member "$MEMBER" 0 "$q"); MED["$qn:member"]=$mm
  printf '%-22s %-10s %12s %12s\n' "$qn" member "$mm" "$mb"
  read -r dm db < <(bench delegate "$DELEGATE" 1 "$q"); MED["$qn:delegate"]=$dm
  printf '%-22s %-10s %12s %12s\n' "$qn" delegate "$dm" "$db"
  set_scope narrow
  read -r sm sb < <(bench scoped "$DELEGATE" 1 "$q"); MED["$qn:scoped"]=$sm
  printf '%-22s %-10s %12s %12s\n' "$qn" scoped "$sm" "$sb"
  for m in delegate scoped; do
    v=${MED["$qn:$m"]}
    case "$qn" in LIST|COUNT) lim=$ABS_MS ;; *) lim=${POINT_MS:-25} ;; esac
    python3 - "$qn" "$m" "$mm" "$v" "$FACTOR" "$lim" <<'PY' || FAIL=1
import sys
qn,m,base,v,factor,lim=sys.argv[1:7]; base=float(base); v=float(v)
if v > float(lim) or (base >= 20 and v > float(factor)*base):
    print("   SLOW: %s as %s: %.2f ms vs member %.2f ms (limits: %sx when the member takes >= 20 ms, %s ms)" % (qn,m,v,base,factor,lim)); sys.exit(1)
PY
  done
done
# The Hub's OWN reads (migration 119): the person outside the delegated context lists and counts hub_inbox_items, whose policy and the policy of the conversations
# behind them used to ask has_active_hub_access once per row (5.6 s the list, 8.1 s the counters at 30 000 items). Same shape as the Hub inbox endpoint (liveInbox).
HUB=00000000-0000-0000-0000-0000000000b0
Q_HUBLIST="SELECT i.id FROM (SELECT i.id, i.hub_id, i.tenant_id, i.last_activity_at, CASE WHEN c.id IS NULL THEN i.status ELSE c.status END AS status,
  CASE WHEN c.id IS NULL THEN i.assigned_user_id ELSE c.assigned_to_user_id END AS assigned_user_id
  FROM hub_inbox_items i LEFT JOIN conversations c ON c.tenant_id=i.tenant_id AND c.id=i.conversation_id) i
  WHERE i.hub_id='$HUB' AND i.status<>'closed' AND i.assigned_user_id IS NULL ORDER BY i.last_activity_at DESC, i.id DESC LIMIT 51"
Q_HUBCOUNT="SELECT count(*) FILTER (WHERE i.status<>'closed'), count(*) FILTER (WHERE i.status='closed') FROM (SELECT CASE WHEN c.id IS NULL THEN i.status ELSE c.status END AS status
  FROM hub_inbox_items i LEFT JOIN conversations c ON c.tenant_id=i.tenant_id AND c.id=i.conversation_id WHERE i.hub_id='$HUB') i"
set_scope none
for qn in HUBLIST HUBCOUNT; do
  q_var="Q_$qn"; q="${!q_var}"
  read -r hm hb < <(bench hubtab "$DELEGATE" 0 "$q")
  printf '%-22s %-10s %12s %12s\n' "$qn" hubtab "$hm" "$hb"
  case "$qn" in HUBLIST) lim=${HUB_LIST_MS:-600} ;; *) lim=${HUB_COUNT_MS:-1500} ;; esac
  python3 - "$qn" "$hm" "$lim" <<'PY' || FAIL=1
import sys
qn,v,lim=sys.argv[1],float(sys.argv[2]),float(sys.argv[3])
if v > lim:
    print("   SLOW: %s as the Hub's own tab: %.2f ms (limit %s ms)" % (qn,v,lim)); sys.exit(1)
PY
done
set_scope none
[ "$FAIL" = 0 ] && echo "PASS: delegated reads within the limits (${FACTOR}x the member's when it takes >= 20 ms; ${ABS_MS} ms lists, ${POINT_MS:-25} ms point reads)" || { echo "FAIL: a delegated read is too slow (see SLOW lines)"; exit 1; }
