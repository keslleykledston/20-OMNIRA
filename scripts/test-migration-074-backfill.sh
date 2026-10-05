#!/usr/bin/env bash
# Proves the 000074 backfill on a throwaway Postgres: roll 074 back, seed contacts of every old kind (with and without a
# manual audit trail), re-apply 074 and check nobody became a customer, agents became "other", unchosen defaults became
# "unclassified" and manual decisions kept their kind with the audited actor. Never touches the real database.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-mig-bf-$$
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT
docker run -d --name "$NAME" -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=rt postgres:16-alpine >/dev/null
for i in $(seq 1 90); do
  [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break
  sleep 1
done
run() {
  docker run --rm --network "container:$NAME" -v "$PWD/migrations:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=rt postgres:16-alpine sh /tools/migrate-sql.sh "$@"
}
sql() { docker exec -i "$NAME" psql -U omnira -d rt -v ON_ERROR_STOP=1 -At "$@"; }
run up >/dev/null
run down 000074_contact_classification >/dev/null
sql <<'SQL'
INSERT INTO tenants(id,legal_name,status) VALUES ('00000000-0000-0000-0000-0000000000a1','t','active');
INSERT INTO users(id,external_subject,email,status) VALUES ('00000000-0000-0000-0000-0000000000b1','s','a@invalid','active');
INSERT INTO contacts(id,tenant_id,display_name,phone_e164,status,kind) VALUES
 ('00000000-0000-0000-0000-000000000c01','00000000-0000-0000-0000-0000000000a1','default other','+5511900000001','active','other'),
 ('00000000-0000-0000-0000-000000000c02','00000000-0000-0000-0000-0000000000a1','manual other','+5511900000002','active','other'),
 ('00000000-0000-0000-0000-000000000c03','00000000-0000-0000-0000-0000000000a1','customer','+5511900000003','active','customer'),
 ('00000000-0000-0000-0000-000000000c04','00000000-0000-0000-0000-0000000000a1','team member','+5511900000004','active','agent'),
 ('00000000-0000-0000-0000-000000000c05','00000000-0000-0000-0000-0000000000a1','spam','+5511900000005','active','spam');
INSERT INTO audit_events(tenant_id,actor_id,action,resource_type,resource_id,outcome,metadata) VALUES
 ('00000000-0000-0000-0000-0000000000a1','00000000-0000-0000-0000-0000000000b1','contact.kind_changed','contact','00000000-0000-0000-0000-000000000c02','success','{"kind_to":"other"}'),
 ('00000000-0000-0000-0000-0000000000a1','00000000-0000-0000-0000-0000000000b1','contact.kind_changed','contact','00000000-0000-0000-0000-000000000c05','success','{"kind_to":"spam"}');
SQL
run up >/dev/null
got=$(sql -c "SELECT display_name||'|'||kind||'|'||coalesce(classification_source,'-')||'|'||(classified_by_user_id IS NOT NULL) FROM contacts ORDER BY display_name")
want='customer|unclassified|migration|false
default other|unclassified|migration|false
manual other|other|manual|true
spam|spam|manual|true
team member|other|migration|false'
if [ "$got" != "$want" ]; then echo "FAIL backfill:"; echo "$got"; exit 1; fi
n=$(sql -c "SELECT count(*) FROM contacts WHERE kind='customer'")
[ "$n" = "0" ] || { echo "FAIL: a contact was promoted to customer"; exit 1; }
echo "PASS: 000074 backfill (no customer without evidence; agents->other; unchosen->unclassified; manual kept)"
