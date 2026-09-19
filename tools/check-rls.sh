#!/bin/bash
# check-rls.sh — varredura de completude de RLS.
#
# Roda TestRLSCompleteness (internal/platform/db/rls_completeness_test.go):
# toda tabela em `public` com coluna `tenant_id` precisa ter
# relrowsecurity=true, relforcerowsecurity=true, e pelo menos uma policy.
# Sem isso o teste falha nomeando a tabela e o motivo exato.
#
# Padrão inspirado em tests/invariants/rls-completude-varredura.test.ts do
# DeskcommCRM (donor project — ver docs/research/deskcomm/REUSE-AUDIT.md):
# uma varredura de catálogo pega automaticamente a classe de bug que uma
# lista mantida à mão sempre deixa passar (nova tabela tenant-owned criada
# sem RLS, ou RLS "ligada" mas sem FORCE).
#
# Use antes de qualquer merge que adicione uma tabela tenant-owned nova, e
# como parte do CI.

set -e

: "${OMNIRA_DATABASE_URL:=postgres://omnira:omnira@localhost:55434/omnira_dev}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "Rodando TestRLSCompleteness contra: ${OMNIRA_DATABASE_URL%%@*}@***"

docker run --rm --network host \
  -v "$REPO_ROOT":/src -w /src \
  -e GOCACHE=/tmp/gocache \
  -e OMNIRA_DATABASE_URL="$OMNIRA_DATABASE_URL" \
  golang:1.25 \
  go test -buildvcs=false ./internal/platform/db/... -run TestRLSCompleteness -v
