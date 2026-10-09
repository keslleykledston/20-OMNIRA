#!/usr/bin/env bash
# Mutation test for DELEGATED SERVING, PILOT JOURNEY (ADR-0040 phase 03, migration 109): weakens each rule in turn
# (the delegable-route list, the key check, the path variant, the delegated writes, the provisioning rules, the member predicates and the read policies); the real-Postgres tests must go red
# every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubreadsmut-$$; DB=omnira_test_readsmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/tenancy/adapters/serving.go internal/hub/adapters/delegated_writes.go internal/hub/provisioning/service.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=readsmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

mkdb() { # mkdb <migrations dir>
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
run() { # the host toolchain with its warm caches
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -p 1 -run 'TestDelegated|TestEverySecret|TestTheDoor|TestARequestActs|TestAForged|TestPermissionQuestions|TestDomainAccess|TestNothingCanBeWritten|TestOneHub|TestAMember|TestAnInstanceAdmin|TestProvisioningThe|TestAuditNames' \
      ./internal/hub/adapters 2>&1
}
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 1 ]; }

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
SV=internal/tenancy/adapters/serving.go; DW=internal/hub/adapters/delegated_writes.go; PV=internal/hub/provisioning/service.go
M=000109_hub_delegated_serving_reads.up.sql

# --- Go: the route list and the key check
mut "a route that is not delegable is admitted"             $SV '	if !delegable {' '	if !delegable && false {'
mut "the route's key is not checked"                        $SV '		if !held {' '		if !held && false {'
mut "the route is never marked delegable"                   $SV '		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), delegableKey{}, permission)))' '		next.ServeHTTP(w, r)'
mut "a malformed hub or tenant in the path is not refused"  $SV '			if herr != nil || terr != nil || hubID == uuid.Nil || tenantID == uuid.Nil {' '			if herr != nil && terr != nil {'
# --- Go: the delegated writes
mut "a member's write goes through the delegated path"      $DW '		if !delegated {
			original.ServeHTTP(w, r)
			return
		}
		t, ok := d.target(w, r, tc)
		if !ok {
			return
		}
		changed' '		if !delegated && false {
			original.ServeHTTP(w, r)
			return
		}
		t, ok := d.target(w, r, tc)
		if !ok {
			return
		}
		changed'
mut "an attachment is accepted through the delegated reply" $DW '		if req.AttachmentID != nil {' '		if req.AttachmentID != nil && false {'
mut "the item is looked up for any tenant"                  $DW 'WHERE hub_id = $1 AND tenant_id = $2 AND conversation_id = $3`, *tc.HubID, tc.TenantID, conversationID)' 'WHERE hub_id = $1 AND conversation_id = $3 AND $2::uuid IS NOT NULL`, *tc.HubID, tc.TenantID, conversationID)'
mut "the company shown is not the context's company"        $DW 'd.reply.Authorize(r.Context(), tc.ActorID, *tc.HubID, itemID, tc.TenantID, correlationOf(r))' 'd.reply.Authorize(r.Context(), tc.ActorID, *tc.HubID, itemID, uuid.Nil, correlationOf(r))'
# --- Go: provisioning
mut "a non-delegable key can enter a ceiling or a grant"    $PV '		if !ok {
			return nil, invalid("%q cannot be delegated to a hub (it is not a delegable permission)", k)
		}' '		if !ok && false {
			return nil, invalid("%q cannot be delegated to a hub (it is not a delegable permission)", k)
		}'
mut "a grant may exceed the contract ceiling"               $PV '			if !inCeiling[k] {' '			if !inCeiling[k] && false {'
mut "reply is allowed without claim"                        $PV '		if has("conversation.reply") && !has("conversation.claim") {' '		if has("conversation.reply") && !has("conversation.claim") && false {'
mut "claim or reply is allowed without read"                $PV '		if (has("conversation.claim") || has("conversation.reply")) && !has("conversation.read") {' '		if (has("conversation.claim") || has("conversation.reply")) && !has("conversation.read") && false {'
mut "can_reply does not follow the reply key"               $PV '			grant, clean, has("conversation.reply")); err != nil {' '			grant, clean, false); err != nil {'
mut "a revoked grant can be edited"                         $PV '		if status != "active" {
			return invalid("the grant is %s: give the person access first", status)
		}
		inCeiling' '		if status != "active" && false {
			return invalid("the grant is %s: give the person access first", status)
		}
		inCeiling'
# --- SQL: one context at a time, in the data layer
sqlmut "a member predicate answers while acting for a hub"  $M "  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND NULLIF(current_setting('app.acting_hub', true), '') IS NULL
     AND EXISTS (
       SELECT 1 FROM public.memberships
       WHERE tenant_id" "  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND EXISTS (
       SELECT 1 FROM public.memberships
       WHERE tenant_id"
sqlmut "an admin predicate answers while acting for a hub"  $M "     AND NULLIF(current_setting('app.acting_hub', true), '') IS NULL
     AND EXISTS (
       SELECT 1 FROM public.memberships m" "     AND EXISTS (
       SELECT 1 FROM public.memberships m"
# --- SQL: the read policies
sqlmut "contacts are readable without a contact domain"     $M "USING (tenant_id IN (SELECT delegated_tenants('contact', 'read'))" "USING (tenant_id IN (SELECT delegated_tenants('conversation', 'read'))"
sqlmut "contacts ignore the conversation's visibility"      $M "         AND EXISTS (SELECT 1 FROM conversations c WHERE c.contact_id = contacts.id AND c.tenant_id = contacts.tenant_id));" "         AND true);"
sqlmut "files are readable without a media domain"          $M "CREATE POLICY message_media_read_delegated ON message_media FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('media', 'read'))" "CREATE POLICY message_media_read_delegated ON message_media FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read'))"
sqlmut "inbound files ignore the message's visibility"      $M "         AND EXISTS (SELECT 1 FROM messages m WHERE m.id = message_media.message_id AND m.tenant_id = message_media.tenant_id));" "         AND true);"
sqlmut "outbound files ignore the message's visibility"     $M "         AND EXISTS (SELECT 1 FROM messages m WHERE m.id = message_outbound_media.message_id AND m.tenant_id = message_outbound_media.tenant_id));" "         AND true);"
sqlmut "transcripts ignore the message's visibility"        $M "         AND EXISTS (SELECT 1 FROM messages m WHERE m.id = message_media_analysis.message_id AND m.tenant_id = message_media_analysis.tenant_id));" "         AND true);"
sqlmut "outbound files are readable without a media domain" $M "CREATE POLICY message_outbound_media_read_delegated ON message_outbound_media FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('media', 'read'))" "CREATE POLICY message_outbound_media_read_delegated ON message_outbound_media FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read'))"
sqlmut "transcripts are readable without a media domain"    $M "CREATE POLICY message_media_analysis_read_delegated ON message_media_analysis FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('media', 'read'))" "CREATE POLICY message_media_analysis_read_delegated ON message_media_analysis FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read'))"
sqlmut "the contacts policy also allows writing"            $M "CREATE POLICY contacts_read_delegated ON contacts FOR SELECT" "CREATE POLICY contacts_read_delegated ON contacts FOR ALL"
sqlmut "the files policy also allows writing"              $M "CREATE POLICY message_media_read_delegated ON message_media FOR SELECT" "CREATE POLICY message_media_read_delegated ON message_media FOR ALL"
# NOT mutants (redundant layers, documented): (1) `acting IS NOT NULL` in delegated_tenants: with no acting hub the comparison with NULL is never true;
# (2) `c.hub_id = acting hub` in delegated_tenants: has_delegated_access, called in the same row, already asks about the ACTING hub only, so
# another hub's contract on the same company adds nothing; (3) the tenant condition inside the policies' EXISTS clauses: the message / conversation
# is looked up by id and the row security of those tables decides what is visible.
echo "PASS: every mutation was killed by a failing real-Postgres test ($N mutants)"
