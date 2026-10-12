# Sinais operacionais do Hub (fase 12 — observabilidade)

Estado: `LAB`. Código e testes em `internal/platform/opsmetrics`, `internal/worker/hubprojector`, `internal/worker/hubdistributor`, `internal/tenancy/adapters/serving.go`, `internal/platform/sessionctx`. O que **não** está provado: alerta disparando de verdade no celular (depende do cron e do ntfy do dono — ver §4).

## 1. O que é medido (e o que nunca é)

Os rótulos são **conjuntos fechados escritos no código** (um desfecho, um tipo). Nunca empresa, pessoa, Hub, conversa ou qualquer id vindo de requisição: isso vai para log e rastro (`docs/architecture/OBSERVABILITY.md`, "Cardinalidade"). Um valor de rótulo fora do conjunto é descartado e contado em `<métrica>_dropped_total`. `GET /metrics` do `api` (porta 8080) e do `worker` (porta 9090) — o nginx público só encaminha `/api/`, então não saem do host.

| Métrica | Onde | O que significa |
|---|---|---|
| `omnira_hub_projector_last_success_timestamp_seconds` | worker | Quando terminou a última passada **sem nenhum par falhando**. É o sinal que importa: um projetor parado deixa a aba "Todas" atrás das conversas sem erro algum |
| `omnira_hub_projector_interval_seconds` | worker | Intervalo configurado (`OMNIRA_HUB_PROJECTOR_INTERVAL_SECONDS`) |
| `omnira_hub_projector_runs_total{outcome="ok\|error"}` | worker | Passadas completas; `error` = ao menos um par (Hub, empresa) falhou |
| `omnira_hub_projector_changes_total{kind="upserted\|removed"}` | worker | Linhas escritas/removidas |
| `omnira_hub_projector_last_duration_seconds` | worker | Duração da última passada (cresce com o volume) |
| `omnira_hub_distributor_*` | worker | Mesmo desenho para a distribuição dos pools de trabalho |
| `omnira_hub_delegation_requests_total{outcome}` | api | Desfecho dos pedidos `X-Omnira-Acting-As`: `served`, `rate_limited`, `disabled`, `not_delegable`, `refused` (não encontrado/sem concessão), `forbidden_key` (concessão viva sem a chave da rota), `error` |
| `omnira_session_ctx_rejections_total` | api, worker | Contextos de sessão assinados que **o banco recusou** (chave diferente entre o processo e o banco). Em modo `enforce` essas sessões ficam sem acesso e **não há erro HTTP para ver** |
| `omnira_hub_events_streams_total{outcome}` / `omnira_hub_events_signals_total` | api | Fluxos do sinal de mudança do Hub (abertos, recusados por limite/acesso, encerrados por perda de acesso/duração) e sinais escritos |
| `omnira_realtime_bridge_dropped_total{reason}` | worker | Notificações que o bridge de tempo real não publicou (par empresa/conversa não verificado, erro de verificação, limite de consultas, malformada); um salto em `unverified_pair` é alguém forjando `NOTIFY` |
| `omnira_session_ctx_enforce` | api, worker | `1` quando o banco, na partida do processo, estava em `enforce` |

## 2. Regras (Prometheus/Alertmanager — a pilha-alvo de `OBSERVABILITY.md`)

```yaml
groups:
- name: omnira-hub
  rules:
  - alert: HubProjectorStale          # a aba "Todas" está atrás das conversas
    expr: time() - omnira_hub_projector_last_success_timestamp_seconds > 3 * omnira_hub_projector_interval_seconds
    for: 2m
    labels: {severity: critical}
  - alert: HubProjectorFailing        # há par (Hub, empresa) falhando, mesmo que a última passada com sucesso seja recente
    expr: increase(omnira_hub_projector_runs_total{outcome="error"}[10m]) > 0
    labels: {severity: warning}
  - alert: SessionContextRefused      # chave do processo ≠ chave do banco: sessões sem acesso
    expr: increase(omnira_session_ctx_rejections_total[5m]) > 0
    labels: {severity: critical}
  - alert: SessionContextNotEnforced
    expr: omnira_session_ctx_enforce == 0
    labels: {severity: critical}
  - alert: HubDelegationRefusalsJumped   # revogação em massa, ou alguém sondando outra empresa
    expr: sum(increase(omnira_hub_delegation_requests_total{outcome=~"refused|forbidden_key"}[10m])) > 50
    labels: {severity: warning}
```

## 3. Sem Prometheus: `scripts/hub-health-check.sh`

A pilha de hoje usa cron + `scripts/run-check-with-alert.sh` (ntfy), não Prometheus. O mesmo critério está em `scripts/hub-health-check.sh` (contrato de saída igual ao dos outros checks; prova em `scripts/test-hub-health-check.sh`, 9 casos, sem tocar em nada vivo): `FAIL` se o projetor não termina uma passada boa por 3 intervalos (ou nunca reportou), se o banco recusou um contexto assinado, se um processo vê o banco fora de `enforce`; `WARN` se o projetor está atrasado (2 intervalos) ou as métricas não puderam ser lidas (nunca vira alarme falso da plataforma).

Linha de cron proposta (**não instalada**; entra depois da implantação deste código, porque a imagem em produção ainda não expõe as métricas):

```
*/5 * * * * STATE_FILE=/var/log/omnira/hub-health-check.log /home/suporte/projects/20-OMNIRA/scripts/run-check-with-alert.sh hub-health /home/suporte/projects/20-OMNIRA/scripts/hub-health-check.sh >/dev/null 2>&1
```

## 4. O que ainda não foi provado

- O alerta chegando ao celular (precisa da linha de cron acima e do tópico ntfy já configurado pelo dono).
- Carga: a duração da passada do projetor com dezenas de milhares de conversas (a política de leitura foi medida — ver `scripts/bench-hub-policies.sh` — mas o laço de escrita do projetor não).
