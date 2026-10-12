# Hub — guia do operador

Estado: `LAB` (piloto interno supervisionado; **não** é produção geral). Sinais e alertas: `HUB-ALERTS.md`. Roteiro de teste ao vivo: `HUB-LIVE-TEST-SCRIPT.md`. Implantação/reversão: `docs/architecture/HUB-MIGRATION-GUIDE.md`.

## 1. Chaves de funcionamento (`.env` do compose)

| Variável | Valor em produção | Efeito |
|---|---|---|
| `OMNIRA_HUB_API_ENABLED` | true | rotas do agente (`/hubs/...`, caixa agregada, sinal de mudança) |
| `OMNIRA_HUB_ADMIN_API_ENABLED` | true | painel de instâncias/empresas (operador/administrador) |
| `OMNIRA_HUB_ACCESS_API_ENABLED` | true | painel `/acessos` (somente `hub_admin`) |
| `OMNIRA_HUB_SERVE_ENABLED` | true | atender uma empresa pelo Hub (`X-Omnira-Acting-As`) |
| `OMNIRA_HUB_PROJECTOR_ENABLED` / `_INTERVAL_SECONDS` | true / 60 | projeção da caixa agregada (worker) |
| `OMNIRA_HUB_DISTRIBUTOR_ENABLED` / `_INTERVAL_SECONDS` | true / 15 | distribuição automática dos pools (worker) |
| `OMNIRA_SESSION_CTX_KEY` | (segredo, arquivo 600) | chave do contexto de sessão assinado (ADR-0042) |
| `OMNIRA_SESSION_CTX_REQUIRE_ENFORCE` | true | o processo recusa subir se o banco não estiver em `enforce` |
| `OMNIRA_ORIGIN_CHECK`, `OMNIRA_ALLOWED_ORIGINS` | padrão ligado | checagem de mesma origem nas escritas por cookie |

Desligar uma flag some a rota (404 uniforme) sem tocar nos dados.

## 2. Rotina

- **Saúde:** cron `*/5` roda `scripts/hub-health-check.sh` (estado e último resultado em `/var/log/omnira/hub-health-check.log`). `OK reason=hub_signals_fresh` é o normal; `WARN`/`CRIT` trazem o motivo (projetor atrasado, contexto assinado recusado, enforce desligado).
- **Provisionar:** `omnira-hubctl` (`hub create|status`, `member add|remove`, `contract create|status`, `grant add|revoke`, `serving ceiling|grant`, `platform-operator add|revoke|list`, `reconcile`, `distribute`, `show`). Dia a dia pelo painel `/acessos`; `hubctl` é o caminho de emergência/auditoria.
- **Quando algo para de aparecer na aba "Todas":** (1) `hub-health-check`; (2) o projetor está vivo? (`omnira_hub_projector_last_success_timestamp_seconds`); (3) contrato/concessão vigentes? (`hubctl show`); (4) a empresa está suspensa? (suspensa = nenhum caminho a serve).
- **Revogar:** desativar a concessão/contrato corta o acesso em até ~15 s nos fluxos abertos (vigia do SSE) e imediatamente nas próximas requisições.

## 3. O que NÃO fazer

Desligar RLS, usar o papel dono para atender, editar `app.*` à mão, publicar segredo em fila/audit, mexer em Keycloak/host/ACL de NATS/Valkey sem ordem do dono (ver a lista de pendências do dono em `HUB-VERIFICATION-STATUS.md`).

## 4. Backup e restauração

Backup antes de toda migration. Restauração provada em cluster novo (`scripts/backup-restore-hub-check.sh`). Procedimento completo em `docs/ops/BACKUP-CLOUD.md` e `HUB-MIGRATION-GUIDE.md`.
