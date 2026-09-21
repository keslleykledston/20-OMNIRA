#!/bin/bash
# cleanup-test-data.sh — remove fixtures deixadas por testes contra Postgres real.
#
# Os testes de isolamento/RLS precisam de dados que atravessem conexões e
# transações, então não podem usar rollback: eles fazem INSERT de verdade e nem
# todos limpam. Num banco de dev que roda a suíte com frequência isso acumula
# centenas de tenants e users mortos, e a partir de certo ponto qualquer
# inspeção manual do banco vira garimpo.
#
# Fixture é reconhecida por como o código a cria, não por idade nem por nome
# "parecer de teste":
#
#   tenants  — todo seed intencional usa UUID literal e fixo (tools/seed-dev.sql,
#              web/e2e/fixtures.sql); todo teste Go usa uuid.New(). Logo, tenant
#              cujo id não está na allowlist abaixo veio de teste.
#   users    — external_subject é um UUID puro, 'oidc-test-<uuid>', ou uma
#              identidade de IdP em domínio .test. Os domínios .test e .invalid
#              são reservados pela RFC 2606 e nunca aparecem em dado real.
#
# Um user com membership em tenant da allowlist nunca é removido, mesmo que o
# external_subject case com um dos padrões.
#
# Uso:
#   tools/cleanup-test-data.sh              # lista o que removeria (padrão)
#   tools/cleanup-test-data.sh --apply      # remove
#
# Conexão: DATABASE_URL, ou as libpq padrão (PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE).
# Precisa do owner do schema: as policies de RLS não permitem esta varredura à
# role de aplicação.
set -euo pipefail

APPLY=0
for arg in "$@"; do
  case "$arg" in
    --apply)   APPLY=1 ;;
    --dry-run) APPLY=0 ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "argumento desconhecido: $arg" >&2; exit 2 ;;
  esac
done

: "${DATABASE_URL:=postgres://omnira:omnira@localhost:55434/omnira_dev?sslmode=disable}"

psql_run() { psql "$DATABASE_URL" -X -q -v ON_ERROR_STOP=1 "$@"; }
q() { psql_run -tA -c "$1"; }

# Guarda de ambiente. A ferramenta apaga dados em massa; nunca deve poder rodar
# contra algo que não seja um banco de desenvolvimento ou de teste.
if [ "${OMNIRA_ENV:-}" = "production" ]; then
  echo "recusado: OMNIRA_ENV=production" >&2
  exit 1
fi
DB_NAME=$(q "SELECT current_database()")
case "$DB_NAME" in
  *prod*|*production*)
    echo "recusado: banco '$DB_NAME' parece de produção" >&2
    exit 1 ;;
  *dev*|*test*) ;;
  *)
    echo "recusado: banco '$DB_NAME' não parece de dev/test (esperado nome contendo 'dev' ou 'test')" >&2
    exit 1 ;;
esac

# UUIDs determinísticos dos seeds — a fronteira entre dado intencional e fixture.
SEED_TENANTS="'11111111-1111-1111-1111-111111111111','22222222-aaaa-aaaa-aaaa-aaaaaaaaaaaa'"

FIXTURE_TENANTS="SELECT id FROM tenants WHERE id NOT IN ($SEED_TENANTS)"

FIXTURE_USERS="
  SELECT u.id FROM users u
  WHERE (
      u.external_subject ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\$'
   OR u.external_subject LIKE 'oidc-test-%'
   OR u.external_subject ~ '^https?://[^|]*\.test(/|\|)'
   OR u.email LIKE '%@example.com'
   OR u.email LIKE '%@invalid'
  )
  AND NOT EXISTS (
    SELECT 1 FROM memberships m
    WHERE m.user_id = u.id AND m.tenant_id IN ($SEED_TENANTS)
  )"

tenant_count=$(q "SELECT count(*) FROM ($FIXTURE_TENANTS) t")
user_count=$(q "SELECT count(*) FROM ($FIXTURE_USERS) u")
total_tenants=$(q "SELECT count(*) FROM tenants")
total_users=$(q "SELECT count(*) FROM users")

echo "banco: $DB_NAME"
echo "antes:  tenants=$total_tenants  users=$total_users"
echo "fixtures identificadas: tenants=$tenant_count  users=$user_count"
echo

if [ "$tenant_count" = "0" ] && [ "$user_count" = "0" ]; then
  echo "nada a remover."
  exit 0
fi

echo "amostra dos tenants (até 10):"
psql_run -c "SELECT id, legal_name, created_at FROM ($FIXTURE_TENANTS) t JOIN tenants USING (id) ORDER BY created_at LIMIT 10"
echo "amostra dos users (até 10):"
psql_run -c "SELECT id, external_subject, email FROM ($FIXTURE_USERS) u JOIN users USING (id) ORDER BY created_at LIMIT 10"

if [ "$APPLY" != "1" ]; then
  echo
  echo "dry-run: nada foi removido. Use --apply para executar."
  exit 0
fi

echo
echo "removendo..."
# Uma transação só: as FKs das tabelas tenant-owned são ON DELETE CASCADE, e os
# users saem depois porque memberships depende deles.
psql_run -1 <<SQL
DELETE FROM tenants WHERE id IN ($FIXTURE_TENANTS);
DELETE FROM users   WHERE id IN ($FIXTURE_USERS);
SQL

after_tenants=$(q "SELECT count(*) FROM tenants")
after_users=$(q "SELECT count(*) FROM users")
echo "depois: tenants=$after_tenants  users=$after_users"
echo "removidos: tenants=$((total_tenants - after_tenants))  users=$((total_users - after_users))"
