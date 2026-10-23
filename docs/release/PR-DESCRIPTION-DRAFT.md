<!--
Rascunho LOCAL da descrição de PR — NÃO enviado, NENHUM `git push`/`gh pr create` foi executado.
Preparado em F11 para uso quando o usuário autorizar a abertura do PR de
feat/omnira-release-update-engine (92 commits sobre main, baseline 748528a) para main.
-->

## Resumo

Implementa o Sistema de Release Engineering e Atualização Autônoma do OMNIRA (ADR-0054,
`docs/release/spec/OMNIRA_RELEASE_ENGINEERING_PACOTE_EXECUCAO.md`), fases F0–F11:

- **Update Manager API** (`internal/platformrelease/updatemgr`, `/api/v1/platform/*`, atrás de
  `OMNIRA_PLATFORM_RELEASE_API_ENABLED`, autorizada só por `platform_operator`/ADR-0038, nunca
  por `tenant_id`/membership) — ingestão/plano/execução de releases, revogação (append-only,
  anti-replay, fail-closed), monitoramento agregado, retenção/GC segura do catálogo.
- **Update Agent independente** (`apps/update-agent`, systemd) + CLI local (`apps/updatectl`):
  opera inteiramente sobre um ledger SQLite local — nunca depende da API/Postgres estarem
  disponíveis para decidir seu próximo passo seguro. Máquina de estados com classes de
  recuperação R0–R4 (nunca rollback automático de DADO, só de código).
- **Builder de releases e cadeia de suprimentos** (`apps/releasectl`, workflows em
  `.github/workflows/`) com verificação de assinatura fail-closed por padrão.
- **Frontend real** (`web/src/pages/PlatformReleasesPage.tsx` e módulos relacionados) consumindo
  a API real, sem dados falsos, sem copiar protótipo (ADR-0036).
- **Staging isolado com fault injection real** (`scripts/release/test-f8-staging.sh`,
  `scripts/release/f8harness`) medindo RTO/RPO reais contra os binários de produção.
- **Runbooks operacionais completos** (`docs/release/runbooks/`), Makefile com targets seguros
  de dry-run/status/test, exemplos de configuração sem segredos, changelog e dev onboarding.
- Migrations `000168`–`000175` (12 tabelas `platform_*` com `FORCE ROW LEVEL SECURITY`).

Ver `docs/release/IMPLEMENTATION-STATUS.md` para o detalhamento fase a fase (F0–F11, cada uma
com Veredito) e `docs/release/ACCEPTANCE-REPORT.md` para a bateria final de aceitação.

## Comportamento

- Nenhuma mudança de comportamento para tenants/usuários finais existentes — tudo atrás de
  `OMNIRA_PLATFORM_RELEASE_API_ENABLED` (default desligado) e de rotas novas (`/api/v1/platform/*`),
  nunca substituindo rota existente.
- Hub/tenant/membership/RLS inalterados — nenhum enforcement de tenancy existente foi tocado.

## Segurança

- 12 tabelas novas com `FORCE ROW LEVEL SECURITY`, restritas a `is_platform_operator()`.
- `SignatureVerifier` fail-closed por padrão (rejeita por padrão até configuração explícita).
- Revogação append-only + anti-replay (fail-closed por padrão; só `MarkStandalone` explícito
  isenta uma instalação sem API).
- Revisão independente Codex (`codex exec review`) convergida (2 rodadas consecutivas limpas)
  em TODAS as fatias de TODAS as fases F6–F11 — total de 101+ rodadas no programa, 90+ achados
  reais corrigidos e retestados no mesmo ciclo.
- Auditoria de segurança de GitHub Actions/artifacts/SSRF/path-traversal/injection/segredo em
  logs feita em F9, sem achados CRITICAL/HIGH não corrigidos.

## Testes

- `go build ./...` limpo.
- `go test ./...`: 108 pacotes `ok`, 0 `FAIL` (ver ACCEPTANCE-REPORT.md §2.2).
- Integração real contra Postgres 16 descartável (RLS/tenancy, nunca contra `omnira_dev`):
  `internal/platformrelease/updatemgr`, `internal/platformrelease/agent`,
  `internal/tenancy/adapters` PASS; `internal/hub/adapters` reproduz 1 falha pré-existente e não
  relacionada (`TestDelegatedRealtimeStreams`), já documentada antes deste programa.
- Web (Vitest): 1208 testes, 0 falhas reais em isolamento (19 falhas sob contenção de recursos
  neste sandbox, todas confirmadas flaky/pré-existentes e não ligadas a este programa — ver
  ACCEPTANCE-REPORT.md §2.4).
- Staging/fault injection real (F8): Cenários 0/1 PASS pós-correção de sequenciamento de
  migrations; Cenários 2–5 BLOCKED_EXTERNAL por uma incompatibilidade de registry do harness de
  teste com uma nova checagem de proveniência de F9 — lógica coberta por 6 testes dedicados
  reais, nunca simulado como E2E passado (ver ACCEPTANCE-REPORT.md §2.6).
- Contratos OpenAPI 3.0.0 + AsyncAPI 2.6 validados (`tools/validate-specs.sh`).

## Migration

- `000168`–`000175`, todas aditivas, aplicadas e verificadas (`migrate-sql verify`) contra um
  Postgres 16 real nesta sessão. `down.sql` nunca autoriza rollback de dado real de produção.

## Observabilidade

- `agent_heartbeat` (liveness do agente), monitoramento agregado
  (`GET /api/v1/platform/monitoring/status`: agente, backup, divergência de versão/digest,
  update estagnado, erros pós-release), telemetria em operações críticas do Update Manager.

## Pendências (BLOCKED_EXTERNAL — nenhuma bloqueia merge, todas documentadas com o passo exato
para um operador autorizado em `docs/release/runbooks/` e `docs/release/ACCEPTANCE-REPORT.md` §3)

1. Publicação real de imagens no GHCR + assinatura/attestation (Cosign) — sem `gh` CLI/credenciais
   nesta sessão.
2. GitHub Environment com reviewers obrigatórios para aprovação de produção — ação de admin do
   GitHub via web UI.
3. Assinatura real da lista de revogação — mesma causa de (1).
4. Regra de sudoers mínima e exata do backup em produção — runbook documenta o comando exato;
   precisa de validação `sudo -n -l` num host de produção real.
5. F8 Cenários 2–5 ponta a ponta pós-F9 — ver ACCEPTANCE-REPORT.md §2.6 (não exige credencial;
   exigiria DNS/daemon Docker do host, fora do alcance desta sessão).

**Nada em produção foi tocado, criado ou modificado por este PR.** Nenhum systemd unit do Update
Agent foi instalado fora do worktree de desenvolvimento.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01QhrofCAmwxeJ9jnYvtFX1M
