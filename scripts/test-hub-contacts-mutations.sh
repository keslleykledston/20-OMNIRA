#!/usr/bin/env bash
# Mutation test for DELEGATED CONTACT WRITES (ADR-0040 phase 04a, migration 110): weakens each rule in turn
# (the per-key handler checks, the delegated side effects, the refusals of the directory and of the internal kind, /me/access, the policies and the two
# definer functions); the real-Postgres tests must go red
# every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubcontactsmut-$$; DB=omnira_test_contactsmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/contacts/adapters/kind.go internal/contacts/adapters/classification.go internal/contacts/adapters/classification_http.go internal/accounts/adapters/http.go internal/tenancy/adapters/team_http.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=contactsmut-$$" \
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
    go test -count=1 -p 1 -run 'TestDelegatedContactWrites|TestTheDelegatedReclassification|TestDelegatedClassification|TestAPersonServedByTwoHubs' \
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
KI=internal/contacts/adapters/kind.go; CL=internal/contacts/adapters/classification.go; CH=internal/contacts/adapters/classification_http.go
AC=internal/accounts/adapters/http.go; TM=internal/tenancy/adapters/team_http.go
M=000110_hub_delegated_contact_writes.up.sql

# --- Go: the handlers
mut "a delegate is held to the member's key (claim), not to contact.classify" $KI '	if actingForHub(tc) {
		needed = permClassify
	}' '	if actingForHub(tc) && false {
		needed = permClassify
	}'
mut "the contact permission question ignores the delegated context"        $KI 'tc.TenantID, tc.ActorID, key)
}' 'tc.TenantID, uuid.Nil, key)
}'
mut "marking spam through the hub writes the conversations directly"       $KI '	if actingForHub(tc) {
		var n int64' '	if actingForHub(tc) && false {
		var n int64'
mut "the classification permission question ignores the delegated context" $CH 'tc.TenantID, tc.ActorID, key)
}' 'tc.TenantID, uuid.Nil, key)
}'
mut "a directory (ERP) company is reachable through the hub"               $CH '	if actingForHub(tc) {
		// the company directory' '	if actingForHub(tc) && false {
		// the company directory'
mut "a hub agent can mark someone internal"                                 $CH '	if kind == domain.KindInternal && actingForHub(tc) {' '	if kind == domain.KindInternal && actingForHub(tc) && false {'
mut "a hub agent can undo what the instance declared internal"          $CL '	if tc, terr := tenancydomain.FromContext(ctx); terr == nil && actingForHub(tc) && prev == domain.KindInternal {' '	if tc, terr := tenancydomain.FromContext(ctx); terr == nil && actingForHub(tc) && prev == domain.KindInternal && false {'
mut "the derived conversation kind is not recomputed through the hub"      $CL '	if tc, err := tenancydomain.FromContext(ctx); err == nil && actingForHub(tc) {' '	if tc, err := tenancydomain.FromContext(ctx); err == nil && actingForHub(tc) && false {'
mut "the account permission question ignores the delegated context"        $AC 'tc.TenantID, tc.ActorID, permission)' 'tc.TenantID, uuid.Nil, permission)'
mut "/me/access gives a delegate the membership's permissions"             $TM '	if tc.Source == domain.AccessSourceHubServe {' '	if tc.Source == domain.AccessSourceHubServe && false {'
# --- SQL: one hub at a time (Codex HIGH-1) and the scope guard of the definer functions (Codex HIGH-2)
sqlmut "conversations are visible through another hub's grant while acting"  $M "         OR public.has_active_hub_access(public.current_user_id(), tenant_id, queue_id, public.acting_hub(), true));
CREATE POLICY messages_acting_hub_only" "         OR true);
CREATE POLICY messages_acting_hub_only"
sqlmut "recompute can be pointed at a contact outside the hub's scope"       $M "  IF public.acting_hub() IS NULL OR NOT public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')
     OR NOT EXISTS (SELECT 1 FROM public.conversations c
                    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact
                      AND public.has_active_hub_access(public.current_user_id(), p_tenant, c.queue_id, public.acting_hub(), true)) THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  RETURN QUERY" "  IF public.acting_hub() IS NULL OR NOT public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify') THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  RETURN QUERY"
sqlmut "dequeue can be pointed at a contact outside the hub's scope"         $M "  IF public.acting_hub() IS NULL OR NOT public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')
     OR NOT EXISTS (SELECT 1 FROM public.conversations c
                    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact
                      AND public.has_active_hub_access(public.current_user_id(), p_tenant, c.queue_id, public.acting_hub(), true)) THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  UPDATE" "  IF public.acting_hub() IS NULL OR NOT public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify') THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  UPDATE"
# --- SQL: the policies
sqlmut "contacts can be updated with a read key"                  $M "CREATE POLICY contacts_update_delegated ON contacts FOR UPDATE
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'write'))" "CREATE POLICY contacts_update_delegated ON contacts FOR UPDATE
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read'))"
sqlmut "links are readable without a contact domain"              $M "CREATE POLICY contact_account_links_read_delegated ON contact_account_links FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read'))" "CREATE POLICY contact_account_links_read_delegated ON contact_account_links FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('conversation', 'read'))"
sqlmut "links can be added with a read key"                       $M "CREATE POLICY contact_account_links_insert_delegated ON contact_account_links FOR INSERT
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('contact', 'write'))" "CREATE POLICY contact_account_links_insert_delegated ON contact_account_links FOR INSERT
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('contact', 'read'))"
sqlmut "links can be added to a contact the contract does not reach" $M "              AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_account_links.contact_id AND ct.tenant_id = contact_account_links.tenant_id));
CREATE POLICY contact_account_links_update_delegated" "              AND true);
CREATE POLICY contact_account_links_update_delegated"
sqlmut "links can be changed with a read key"                     $M "CREATE POLICY contact_account_links_update_delegated ON contact_account_links FOR UPDATE
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'write'))" "CREATE POLICY contact_account_links_update_delegated ON contact_account_links FOR UPDATE
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read'))"
sqlmut "accounts are readable without a contact domain"           $M "CREATE POLICY customer_accounts_read_delegated ON customer_accounts FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read')))" "CREATE POLICY customer_accounts_read_delegated ON customer_accounts FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('conversation', 'read')))"
sqlmut "accounts can be written too"                              $M "CREATE POLICY customer_accounts_read_delegated ON customer_accounts FOR SELECT" "CREATE POLICY customer_accounts_read_delegated ON customer_accounts FOR ALL"
sqlmut "contacts can be inserted and deleted too"                 $M "CREATE POLICY contacts_update_delegated ON contacts FOR UPDATE" "CREATE POLICY contacts_update_delegated ON contacts FOR ALL"
# --- SQL: the two definer functions
sqlmut "recompute does not check the key" $M "  IF public.acting_hub() IS NULL OR NOT public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')
     OR NOT EXISTS (SELECT 1 FROM public.conversations c
                    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact
                      AND public.has_active_hub_access(public.current_user_id(), p_tenant, c.queue_id, public.acting_hub(), true)) THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  RETURN QUERY" "  IF public.acting_hub() IS NULL OR false
     OR NOT EXISTS (SELECT 1 FROM public.conversations c
                    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact
                      AND public.has_active_hub_access(public.current_user_id(), p_tenant, c.queue_id, public.acting_hub(), true)) THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  RETURN QUERY"
sqlmut "recompute answers outside the delegated context" $M "  IF public.acting_hub() IS NULL OR NOT public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')
     OR NOT EXISTS (SELECT 1 FROM public.conversations c
                    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact
                      AND public.has_active_hub_access(public.current_user_id(), p_tenant, c.queue_id, public.acting_hub(), true)) THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  RETURN QUERY" "  IF NOT public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')
     OR NOT EXISTS (SELECT 1 FROM public.conversations c
                    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact
                      AND public.has_active_hub_access(public.current_user_id(), p_tenant, c.queue_id, public.acting_hub(), true)) THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  RETURN QUERY"
sqlmut "dequeue does not check the key" $M "  IF public.acting_hub() IS NULL OR NOT public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')
     OR NOT EXISTS (SELECT 1 FROM public.conversations c
                    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact
                      AND public.has_active_hub_access(public.current_user_id(), p_tenant, c.queue_id, public.acting_hub(), true)) THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  UPDATE" "  IF public.acting_hub() IS NULL OR false
     OR NOT EXISTS (SELECT 1 FROM public.conversations c
                    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact
                      AND public.has_active_hub_access(public.current_user_id(), p_tenant, c.queue_id, public.acting_hub(), true)) THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  UPDATE"
sqlmut "dequeue answers outside the delegated context" $M "  IF public.acting_hub() IS NULL OR NOT public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')
     OR NOT EXISTS (SELECT 1 FROM public.conversations c
                    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact
                      AND public.has_active_hub_access(public.current_user_id(), p_tenant, c.queue_id, public.acting_hub(), true)) THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  UPDATE" "  IF NOT public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')
     OR NOT EXISTS (SELECT 1 FROM public.conversations c
                    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact
                      AND public.has_active_hub_access(public.current_user_id(), p_tenant, c.queue_id, public.acting_hub(), true)) THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  UPDATE"
sqlmut "dequeue also empties the queue of a contact that is not spam" $M "    AND EXISTS (SELECT 1 FROM public.contacts ct WHERE ct.tenant_id = p_tenant AND ct.id = p_contact AND ct.kind = 'spam');" "    AND true;"
sqlmut "dequeue takes conversations somebody holds"               $M "AND c.status = 'open' AND c.assigned_to_user_id IS NULL AND c.queue_id IS NOT NULL" "AND c.status = 'open' AND c.queue_id IS NOT NULL"
sqlmut "dequeue takes closed conversations"                       $M "AND c.status = 'open' AND c.assigned_to_user_id IS NULL AND c.queue_id IS NOT NULL" "AND c.assigned_to_user_id IS NULL AND c.queue_id IS NOT NULL"
# NOT mutants (documented): (00) messages_acting_hub_only: a message is also visible only if its conversation is (the legacy messages policy has that EXISTS under the caller's RLS),
# and conversations_acting_hub_only already limits those to the acting hub's contract and scope, so the message policy only repeats "the acting hub's grant is live" (kept as a second barrier);
# (0) the conversation EXISTS inside contacts_update_delegated: an UPDATE also needs the row visible through the SELECT policies, and
# contacts_read_delegated (000109) carries the same condition, so removing it from the update policy changes nothing while that policy exists (kept as a second barrier);
# (0b) the contacts' WITH CHECK tenant condition: a contact the policy lets through always has conversations (the USING clause requires one), and the conversations'
# composite foreign key (tenant_id, contact_id) refuses a contact whose tenant changes (the test still asserts the move is refused);
# (1) the tenant condition inside the links policies' EXISTS clauses and the links' WITH CHECK tenant condition: the composite
# foreign keys (tenant_id, contact_id) and (tenant_id, account_id) already refuse a row that mixes instances; (2) `tenant_id` of customer_accounts
# is the only condition of its policy (there is no conversation to inherit from): removing it would be a different, absent policy, and the test of
# another instance's account (count = 0) kills it as "accounts are readable without a contact domain" does for the domain.
echo "PASS: every mutation was killed by a failing real-Postgres test ($N mutants)"
