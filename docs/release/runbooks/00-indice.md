# Runbooks do OMNIRA Release Engineering (F10)

Procedimentos operacionais para o sistema implementado em F0–F9 (`docs/release/spec/OMNIRA_RELEASE_ENGINEERING_PACOTE_EXECUCAO.md`, `docs/adr/0054-platform-release-update-engine.md`). Cada runbook assume que quem o executa já leu `docs/release/IMPLEMENTATION-STATUS.md` (seção da fase correspondente) e tem acesso ao host real da instalação (SSH, `docker`/`sudo -n`, credenciais do schema owner do Postgres).

Nenhum destes runbooks assume produção já em operação sob este sistema: nenhuma automação deste programa instalou o systemd unit ou a Update Manager API em produção — isso continua sendo uma ativação explícita do operador (ver `01-primeira-instalacao-agente.md`).

| Runbook | Quando usar |
|---|---|
| [01-primeira-instalacao-agente.md](01-primeira-instalacao-agente.md) | Instalar o Update Agent pela primeira vez numa instalação (bootstrap). |
| [02-release-e-promocao.md](02-release-e-promocao.md) | Construir uma release, promovê-la de canal e aplicá-la numa instalação. |
| [03-rollback-e-recovery.md](03-rollback-e-recovery.md) | Uma release falhou, ou a API/backend está indisponível — recuperar sem depender dela. |
| [04-restore-e-pitr.md](04-restore-e-pitr.md) | Restaurar dados após um `RECOVERY_REQUIRED` (R3); limites reais de RPO/RTO/PITR. |
| [05-ci-ghcr.md](05-ci-ghcr.md) | Fluxo de CI/build/push/assinatura e o que está `BLOCKED_EXTERNAL` nesta instalação. |
| [06-aprovacao-producao.md](06-aprovacao-producao.md) | Quem pode aprovar uma instalação/promoção real e como a autorização é verificada. |
| [07-revogacao.md](07-revogacao.md) | Revogar uma release já publicada/instalada. |
| [08-troubleshooting.md](08-troubleshooting.md) | Sintomas comuns (`RECOVERY_REQUIRED`, lock preso, standalone mal configurado, etc.) e como diagnosticar. |

## Terminologia mínima

- **Instalação** (`platform_installations` / `--installation-id`): hoje existe uma só (esta SaaS compartilhada), nunca hardcoded no código.
- **Ledger local**: SQLite em `--ledger-path` (default `/var/lib/omnira-update-agent/ledger.db`), a única autoridade sobre "o que fazer agora" com uma instalação — o Postgres (`platform_*`) espelha histórico e serve a view administrativa, nunca decide por si só.
- **Run**: uma tentativa de aplicar um manifesto de release, identificada por `run_id`, com estado em `internal/platformrelease/domain` (ex.: `LOCKED`, `MIGRATING_EXPAND`, `SUCCEEDED`, `ROLLED_BACK`, `RECOVERY_REQUIRED`).
- **Standalone**: uma instalação explicitamente marcada (`OMNIRA_AGENT_STANDALONE=true` / `--standalone`) como nunca tendo a Update Manager API sincronizando revogações — ver `03-rollback-e-recovery.md` e `08-troubleshooting.md`.
