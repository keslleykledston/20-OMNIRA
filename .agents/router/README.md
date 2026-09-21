# AI Task Router — .agents/router

Camada opcional de roteamento de tarefas de tooling/desenvolvimento para
reduzir custo/tokens, escolhendo entre:

```
TOOL  <  LOCAL_LLM  <  CHEAP_LLM  <  FRONTIER_LLM  <  HUMAN
```

**Escopo: só tooling de desenvolvimento.** Não integra ao domínio/runtime
OMNIRA, não é invocado pelo backend/produto, não decide nada em produção.
Vive inteiramente em `.agents/router/`.

## Arquitetura

```
                        ┌─────────────────────┐
 task (id, domain_tags, │  hard_filter.py      │  determinístico, sem LLM
 description)  ────────▶│  (policy.yaml)        │  SEMPRE roda primeiro
                        └──────────┬───────────┘
                                   │
                   forced_tier? ───┼─── bypass_jev=true
                   (security-critical)  │
                                   │    │
                          não forçado, >1 candidato
                                   │
                                   ▼
                        ┌─────────────────────┐
                        │  Jev (decision       │  só vê os candidatos já
                        │  router)              │  filtrados por policy.yaml
                        │  route.sh:ai_json     │  roda em LOCAL_LLM por padrão
                        └──────────┬───────────┘  (models.yaml:jev.decision_tier)
                                   │
                       confidence < 0.65? ─── escalate (nunca downgrade)
                                   │
                                   ▼
                        ┌─────────────────────┐
                        │  logs/decisions.jsonl │  route + confidence
                        │  (shadow, append-only)│  + (depois) outcome real
                        └──────────┬───────────┘
                                   │
              ROUTER_MODE=shadow: recomendação só — chamador decide e executa
              ROUTER_MODE=active: BLOQUEADO hoje (ver "Critérios de ativação")
```

### Componentes

| Arquivo | Papel |
|---|---|
| `policy.yaml` | Hard policy: domínios forçados a FRONTIER_LLM, candidatos permitidos por domínio, threshold de confidence. Fonte única — sem hardcode espalhado. |
| `models.yaml` | Registro declarativo dos tiers (endpoint/modelo/custo estimado por tier), incluindo qual tier o próprio Jev usa para decidir. |
| `hard_filter.py` | Filtro determinístico. Roda SEMPRE, antes de qualquer LLM. Decide `forced_tier`/`bypass_jev`/`candidates`. |
| `route.sh` | Entrypoint. Chama `hard_filter.py`, depois (se aplicável) o Jev via `tools/ai/lib.sh:ai_json`, grava o log shadow, imprime a recomendação. Nunca executa a tarefa. |
| `log_outcome.py` | Fecha o loop: registra o que REALMENTE foi usado (modelo, tokens, custo, latência, resultado de teste) para uma `task_id` já roteada. |
| `evals/dataset.jsonl` + `evals/run_evals.py` | Gate de ativação — ver abaixo. |
| `logs/decisions.jsonl` | Log append-only, gitignored, gerado em runtime. |

## Reuse-first

Nada disso reimplementa infraestrutura já existente:

- **Cliente de modelo local, sanitização de segredos e métricas**: reusa
  `tools/ai/lib.sh` (`ai_json`, `ai_sanitize`, `ai_metrics`) — o mesmo cliente
  já usado por `tools/ai/triage-tests.sh` e `triage-logs.sh`. O router não
  fala HTTP com o llama.cpp diretamente.
- **Filosofia "determinístico é autoritativo, LLM é pré-análise"**: já
  documentada em `triage-tests.sh` ("LIMITE DE AUTORIDADE") e no
  `AGENTS.md` raiz ("Automation First" / "Prioridade de automação"). Este
  router é a generalização dessa regra já existente, não uma nova regra.
- **Lovable como tier**: reflete a política já existente em `CLAUDE.md`
  ("Lovable MCP Policy") — Lovable só decide visual/UX, nunca segurança.

## Hard policy (nunca decidida por Jev)

Domínios em `policy.yaml:hard_force_frontier` — sempre `FRONTIER_LLM`,
`bypass_jev=true`, sem exceção e sem downgrade possível:

`auth`, `rls`, `tenancy_security`, `crypto`, `destructive_migration`,
`critical_concurrency`, `secret_handling`, `irreversible_architecture_decision`.

Isso espelha diretamente as seções "Proibido sem aprovação" do
`CLAUDE.md` do projeto e o Git Safety / Test Authority do baseline K3G —
o router não inventa uma política de segurança nova, ele automatiza a
já existente.

## Dados nunca enviados ao Jev

`policy.yaml:never_send_to_jev`: secrets, credentials, tokens, connection
strings, private keys, JWTs, PII. Reforçado por `ai_sanitize` (regex já
existente em `tools/ai/lib.sh`) aplicado à descrição antes de qualquer
chamada. Contexto é truncado a `models.yaml:jev.max_input_chars` (4000
chars) — minimizar custo e superfície de exposição.

## Uso (shadow mode)

```bash
.agents/router/route.sh --id IAM3-001 \
  --domains rls \
  --desc "nova policy de SELECT para role_permissions"
# -> {"route_recommended":"FRONTIER_LLM","mode":"shadow",...}
# grava logs/decisions.jsonl, NÃO executa nada.

# depois de executar de verdade (ex: você mesmo rodou no Opus):
python3 .agents/router/log_outcome.py --task-id IAM3-001 \
  --actual-tier FRONTIER_LLM --actual-model claude-opus-5 \
  --tokens 5200 --cost-usd 0.41 --latency-ms 9100 --test-result pass
```

`ROUTER_MODE` default é `shadow`. `active` é recusado incondicionalmente
por `route.sh` hoje — cai em shadow com um aviso — até os critérios abaixo
serem atendidos.

## Métricas registradas (shadow)

Cada linha de `logs/decisions.jsonl` é uma de duas formas:

**Decisão** (gravada por `route.sh`):
`ts, task_id, router_mode, domains, candidates, forced_tier, bypass_jev, jev_tier_raw, jev_confidence, jev_rationale, route_recommended`

**Outcome** (gravada por `log_outcome.py`, `kind:"outcome"`):
`ts, task_id, actual_tier, actual_model_used, tokens, cost_usd, latency_ms, test_result`

Join por `task_id` dá a base para avaliar precisão/economia real. Nunca
grava o conteúdo do prompt — só metadados (mesmo padrão de `ai_metrics`
em `tools/ai/lib.sh`).

## Eval dataset

`evals/dataset.jsonl` — 27 casos cobrindo:

- 8 casos `security_critical` (um por domínio hard-force) — devem SEMPRE
  resultar em `FRONTIER_LLM` + `bypass_jev=true`.
- 6 casos `deterministic` — devem resultar em `candidates=["TOOL"]`.
- 6 casos `low_risk` (summary/logs/docs/changelog/handoff/classification)
  — `candidates=["TOOL","LOCAL_LLM"]`.
- 2 casos `design` — `candidates=["LOVABLE"]`.
- 2 casos `general` — `candidates=["TOOL","LOCAL_LLM","CHEAP_LLM"]`.
- 1 caso `unclassified` — cai no `default` (permite escalar até frontier).
- 2 casos `security_critical_priority` — domain_tags combinando um
  hard-force com outro não-crítico (ex: `auth,lint`) devem priorizar
  segurança, nunca "lint" vencer "auth".

Rodar:

```bash
python3 .agents/router/evals/run_evals.py
```

Isso avalia só o **filtro determinístico** (`hard_filter.py`) — a parte que
importa mais, porque roda sempre, mesmo com Jev fora do ar. Qualquer falha
em caso `security_critical*` é gate incondicional (bloqueia ativação
mesmo com 1 caso). A qualidade das decisões do Jev em si (quando ele
decide, nos domínios não-forçados) é avaliada separadamente, a partir do
log real acumulado — não dá para fazer eval offline de um modelo cujo
comportamento você quer medir em produção.

## Critérios de ativação (ROUTER_MODE=active)

Todos obrigatórios, nenhum dispensável:

1. `evals/run_evals.py` retorna exit 0 (gate de segurança determinístico).
2. Mínimo de **50 decisões shadow com outcome registrado** (`log_outcome.py`
   chamado para a task), cobrindo pelo menos 5 tasks de cada categoria do
   dataset que apareça em uso real.
3. **Zero casos** em `logs/decisions.jsonl` onde `route_recommended` tenha
   sido um tier abaixo do que a task realmente precisou (`test_result:fail`
   atribuível ao tier escolhido, ou correção humana registrando um tier
   mais alto). Qualquer downgrade incorreto observado reinicia a contagem
   do item 2.
4. Revisão humana explícita do log acumulado — não é um threshold
   numérico automático. Alguém lê as 50+ linhas e aprova.
5. Nenhum caso de dado sensível vazado para Jev encontrado ao auditar
   `logs/decisions.jsonl` (task_id + domains + rationale, nunca prompt
   bruto — auditável só pela ausência de campos de segredo/PII no
   próprio log e pela conformidade da regex `ai_sanitize`).

Até isso, `route.sh` recusa `ROUTER_MODE=active` incondicionalmente — não
há flag de bypass.

## Rollback

Trivial por construção: o router **nunca controla execução** em shadow
mode, então "desligar" é:

```bash
rm -rf .agents/router   # ou simplesmente parar de chamar route.sh
```

Nenhum outro componente do OMNIRA (backend, CI, migrations, deploy)
referencia `.agents/router/` — remover a pasta não quebra nada, porque
nada depende dela para funcionar. Se `active` mode for implementado no
futuro e precisar de rollback, o mecanismo será o mesmo: `ROUTER_MODE=shadow`
(ou remover a env var, que é o default) volta ao estado atual
imediatamente, sem migração de dados ou estado a desfazer.

## O que ainda NÃO existe (intencional)

- Execução automática (`ROUTER_MODE=active` real) — bloqueada até os
  critérios acima.
- Chamada real a `CHEAP_LLM`/`FRONTIER_LLM` a partir do router — hoje
  `route.sh` só recomenda; quem chama o modelo de fato continua sendo o
  agente/humano que pediu a rota.
- Qualquer gancho no backend/produto OMNIRA. Isso é proibido pelo escopo
  desta feature e não está implementado em lugar nenhum fora de
  `.agents/router/`.
