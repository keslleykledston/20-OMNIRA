#!/usr/bin/env bash
# Mutation test for DELEGATED ERP DIRECTORY AND TICKETS (ADR-0040 phase 04b, migration 112): weakens each rule in turn (the runtime resolver's delegated branch,
# the permission checker, the ticket service gate, DelegableAny, the account resolution, the notice, the policies and the two definer functions); the
# real-Postgres tests must go red every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubtktmut-$$; DB=omnira_test_tktmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/tickets/adapters/k3g_runtime_resolver.go internal/channels/adapters/management.go internal/tickets/application/create_external_ticket.go internal/tenancy/adapters/serving.go internal/accounts/adapters/ticket_resolver.go internal/contacts/adapters/classification_http.go internal/hub/adapters/delegated_writes.go internal/inbox/adapters/crm_handlers.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=tktmut-$$" \
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
    go test -count=1 -p 1 -run 'TestDelegatedERPDirectory|TestDelegatedTicket|TestDelegatedClassificationUsesTheInstancesERPDirectory' \
      ./internal/hub/adapters 2>&1
}
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 1 ]; }

mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -25; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"

killed() {
  if verdict_green "$2"; then echo "SURVIVED: $1"; [ -n "${REPORT:-}" ] && return 0; echo "FAIL: mutation survived"; exit 1; fi
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
# NOT mutants (equivalent by construction; the same outcome comes from a second layer, so no test could tell them apart):
#   - permission checker "any user id accepted": actor_has_permission answers false for anybody but the session's own person (second layer, tested through the
#     checker in the ticket test);
#   - delegated resolver "no credential reference" / "credential cannot be decrypted": the next check answers the SAME resolution code (credential not found /
#     invalid) and the same HTTP 503;
#   - materialize "source not validated": an unknown source would reach the CHECK constraint on account_external_links.source and fail there;
#   - tickets update "queue scope ignored": an UPDATE needs the row to be visible, and the SELECT policy (tested) already carries the scope;
#   - the explicit scope re-check in the UPDATE WITH CHECK of tickets / attempts / evidence (Codex 04b HIGH): PostgreSQL also checks the NEW row of an UPDATE
#     against the SELECT policy, which already carries the scope, so removing it changes nothing observable (the re-point tests prove the behaviour);
#   - the acting_one_instance pins on tickets / attempts / evidence: second layer on writable tables (the permissive policies hang from a pinned conversation
#     or contact), like contacts / contact_account_links in migration 111.
RS=internal/tickets/adapters/k3g_runtime_resolver.go; CK=internal/channels/adapters/management.go; TS=internal/tickets/application/create_external_ticket.go
SV=internal/tenancy/adapters/serving.go; AR=internal/accounts/adapters/ticket_resolver.go; CL=internal/contacts/adapters/classification_http.go
DW=internal/hub/adapters/delegated_writes.go; CR=internal/inbox/adapters/crm_handlers.go

mut "resolver ignores the delegated context (member path: the connections are unreadable)" $RS 'if tc.Source == tenancydomain.AccessSourceHubServe {' 'if false {'
mut "delegated resolver: two connections are not ambiguous" $RS '	case 1:
	default:
		return nil, &ports.ResolutionError{Code: ports.ResolutionAmbiguousConfiguration, Message: fmt.Sprintf("%d applicable K3G connections found, expected exactly 1", len(found))}' '	default:'
mut "permission checker: a Hub agent is a member (no delegated keys)" $CK 'if tc.Source == tenancydomain.AccessSourceHubServe {
		if userID != tc.ActorID {' 'if false {
		if userID != tc.ActorID {'
mut "ticket service refuses the delegated context" $TS 'return tc.Source == tenancydomain.AccessSourceDirect || tc.Source == tenancydomain.AccessSourceHubServe' 'return tc.Source == tenancydomain.AccessSourceDirect'
mut "DelegableAny needs ALL keys" $SV '			if ok {
				held = true
				break
			}' '			if !ok {
				held = false
				break
			}
			held = true'
mut "ticket account resolver ignores the delegated context" $AR 'if tc, err := tenancydomain.FromContext(ctx); err == nil && tc.Source == tenancydomain.AccessSourceHubServe {' 'if tc, err := tenancydomain.FromContext(ctx); err == nil && false && tc.Source == tenancydomain.AccessSourceHubServe {'
mut "classification ignores the delegated context when materializing the account" $CL '	if actingForHub(tc) {
		// a Hub agent attending the instance has no direct insert on accounts or links' '	if false {
		// a Hub agent attending the instance has no direct insert on accounts or links'
mut "notice: sent without conversation.reply" $DW '	if !mayReply {' '	if false && !mayReply {'
mut "notice: not deduplicated" $DW '	if sent, err := n.store.NoticeAlreadySent(ctx, conversationID, key); err != nil || sent {' '	if _, err := n.store.NoticeAlreadySent(ctx, conversationID, key); err != nil {'
mut "handler uses the member notifier for a Hub agent" $CR '		notifier = h.delegatedOpenNotice' '		notifier = h.openNotice'

M=000112_hub_delegated_erp_and_tickets.up.sql
sqlmut "erp connections: either key -> both keys" $M "OR public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')) THEN" "AND public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')) THEN"
sqlmut "materialize: the ticket flow needs only contact.classify" $M "WHEN 'ticket_flow' THEN 'ticket.create'" "WHEN 'ticket_flow' THEN 'contact.classify'"
sqlmut "materialize: an inactive link is not reactivated" $M "    IF v_link.status <> 'active' THEN" "    IF false THEN"
# policies: drop the conversation/contact EXISTS (the queue scope) and downgrade each write domain to read
sqlmut "tickets read: the queue scope is ignored" $M "  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'read'))
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = tickets.conversation_id AND c.tenant_id = tickets.tenant_id));" "  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'read')));"
sqlmut "tickets update: a read key may update" $M "CREATE POLICY tickets_update_delegated ON tickets FOR UPDATE
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'write'))" "CREATE POLICY tickets_update_delegated ON tickets FOR UPDATE
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'read'))"
sqlmut "attempts insert: a read key may insert" $M "  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'write'))
              AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = ticket_external_create_attempts.conversation_id" "  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'read'))
              AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = ticket_external_create_attempts.conversation_id"
sqlmut "attempts insert: the queue scope is ignored" $M "  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'write'))
              AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = ticket_external_create_attempts.conversation_id AND c.tenant_id = ticket_external_create_attempts.tenant_id));" "  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'write')));"
sqlmut "evidence insert: a read key may insert" $M "CREATE POLICY crm_contact_company_evidence_insert_delegated ON crm_contact_company_evidence FOR INSERT
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'write'))" "CREATE POLICY crm_contact_company_evidence_insert_delegated ON crm_contact_company_evidence FOR INSERT
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'read'))"
sqlmut "evidence insert: the contact scope is ignored" $M "  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'write'))
              AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = crm_contact_company_evidence.contact_id AND ct.tenant_id = crm_contact_company_evidence.tenant_id));" "  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'write')));"
sqlmut "evidence read: removed" $M "CREATE POLICY crm_contact_company_evidence_read_delegated ON crm_contact_company_evidence FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read'))" "CREATE POLICY crm_contact_company_evidence_read_delegated ON crm_contact_company_evidence FOR SELECT
  USING (false AND tenant_id IN (SELECT delegated_tenants('contact', 'read'))"
sqlmut "account links read: removed" $M "CREATE POLICY account_external_links_read_delegated ON account_external_links FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read')));" "CREATE POLICY account_external_links_read_delegated ON account_external_links FOR SELECT
  USING (false);"
sqlmut "account links: the one-instance pin is removed" $M "CREATE POLICY account_external_links_acting_one_instance ON account_external_links AS RESTRICTIVE
  USING (acting_hub() IS NULL OR tenant_id = acting_tenant());" "CREATE POLICY account_external_links_acting_one_instance ON account_external_links AS RESTRICTIVE
  USING (true);"
echo "PASS: all $N mutants killed"
