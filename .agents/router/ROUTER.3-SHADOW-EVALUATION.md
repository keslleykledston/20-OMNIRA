# ROUTER.3 — Shadow Evaluation Plan

**Status:** Draft — esperando aprovação e primeira coleta real de dados.

**Objetivo:** Usar o router em tarefas reais do desenvolvimento OMNIRA (ROUTER_MODE=shadow), coletar >= 50 decisões com outcome validado, analisar por classe, e desbloquear ativação gradual por classe (não global).

**Escopo:** `.agents/router/` + `tools/ai/` apenas. Zero impacto no backend/runtime/produto OMNIRA.

---

## 1. Classes de Tarefas Consolidadas

Mapeamento de domain_tags de policy.yaml para classes operacionais:

| Classe | Domain Tags | Candidatos | Notas |
|--------|------------|-----------|-------|
| **repo_search** | `grep_search` | TOOL | grep/find direto. |
| **summary** | `summary` | TOOL, LOCAL_LLM | Resumir logs/output. |
| **documentation** | `docs`, `changelog`, `handoff`, `source_classification` | TOOL, LOCAL_LLM | Reescrever/gerar docs. |
| **ui_design** | `ui_visual_design`, `ux_prototype` | LOVABLE | Maquetes/prototipos visuais. |
| **frontend_simple** | `frontend_simple` (sem tenancy/auth/RLS) | TOOL, LOCAL_LLM, CHEAP_LLM | Refactor/helper/CSS. |
| **frontend_complex** | `code_review_general` (frontend), `refactor_non_critical` | TOOL, LOCAL_LLM, CHEAP_LLM, FRONTIER_LLM | Feature UI, integração. |
| **backend_simple** | `code_review_general` (backend), `test_authoring` | TOOL, LOCAL_LLM, CHEAP_LLM | Helper/query simples. |
| **backend_complex** | Sem domain_tag específico → default | TOOL, LOCAL_LLM, CHEAP_LLM, FRONTIER_LLM | Query complexa, integração. |
| **debugging** | `logs` | TOOL, LOCAL_LLM | Triage de erros. |
| **testing** | `tests` | TOOL | Rodar testes direto — output só. |
| **operations** | `docker_check`, `migration_check` | TOOL | Validação determinística. |
| **security** ⚠️ HARD-FORCE | `auth`, `rls`, `tenancy_security`, `crypto`, `secret_handling` | **FRONTIER_LLM** | Nunca downgrade. |
| **critical_concurrency** ⚠️ HARD-FORCE | `critical_concurrency`, `destructive_migration`, `irreversible_architecture_decision` | **FRONTIER_LLM** | Nunca downgrade. |
| **lint** | `lint`, `formatting` | TOOL | Ferramentas direto — sem LLM. |

---

## 2. Integração Mínima com Agentes

Adicionar a **AGENTS.md** (próxima seção "Instruções para Agentes") — SEM obrigar:

```markdown
### Router Shadow (opcional)

Tarefas elegíveis para avaliação de custo podem consultar o router:

  .agents/router/route.sh --id <TASK-ID> --domains <csv> --desc "<descrição curta>"

Responde com `route_recommended` (recomendação de tier) e grava em
logs/decisions.jsonl para avaliação posterior. Em shadow mode (default),
a recomendação é informativa — você continua decidindo o executor.

Não chamar router para: grep, git, lint, compiler, tests diretos,
Docker checks, scripts determinísticos. Esses continuam TOOL diretamente.

Ao executar a tarefa, registre o resultado:

  python3 .agents/router/log_outcome.py --task-id <TASK-ID> \
    --actual-tier <TIER> --actual-model <MODEL> --tokens N \
    --cost-usd C --latency-ms MS --test-result <pass|fail|n/a>

Isso fecha o loop para análise de qualidade e economia.
```

---

## 3. Consolidação de Policy + Classes

Atualizar `policy.yaml` com `task_classes` explícito:

```yaml
# após hard_force_frontier:
task_classes:
  repo_search: { domains: [grep_search], candidates: [TOOL] }
  summary: { domains: [summary], candidates: [TOOL, LOCAL_LLM] }
  documentation: { domains: [docs, changelog, handoff, source_classification], candidates: [TOOL, LOCAL_LLM] }
  ui_design: { domains: [ui_visual_design, ux_prototype], candidates: [LOVABLE] }
  frontend_simple: { candidates: [TOOL, LOCAL_LLM, CHEAP_LLM], note: "Sem auth/tenancy" }
  # ... etc
```

E `models.yaml`:

```yaml
# após tier_order:
eval_configuration:
  min_sample_size_per_class: 10
  min_total_decisions: 50
  activation_thresholds:
    success_rate: 0.95              # 95% testes passando
    security_violations: 0           # Zero violações de policy
    human_override_rate: 0.05        # <= 5% override manual
    cost_reduction: 0.20             # >= 20% economia esperada (vs frontier direto)
    latency_increase_max_ms: 500     # Não piora latência >500ms
```

---

## 4. Log Estruturado e Análise

Estender schema de `logs/decisions.jsonl` (sem secrets):

```json
{
  "ts": "2026-09-21T12:00:00Z",
  "task_id": "OMNIRA-BACKEND-042",
  "task_class": "backend_simple",
  "domain_tags": "code_review_general",
  "allowed_routes": ["TOOL", "LOCAL_LLM", "CHEAP_LLM"],
  "jev_selected": "LOCAL_LLM",
  "jev_confidence": 0.92,
  "jev_rationale": "Query simples em tenacity — modelo local suficiente",
  "router_mode": "shadow",
  "actual_tier": "LOCAL_LLM",
  "actual_model": "hermes-3-llama-3.1-8b",
  "fallback_reason": null,
  "tokens_in": 420,
  "tokens_out": 180,
  "estimated_cost_usd": 0.0,
  "latency_ms": 890,
  "tests_passed": 14,
  "tests_failed": 0,
  "human_override": false,
  "outcome": "pass"
}
```

---

## 5. Script de Análise (novo)

Criar `.agents/router/analyze_shadow.py` para:

```python
# Ler logs/decisions.jsonl e gerar relatório:

A. Distribuição de rotas recomendadas vs executadas
B. Taxa de sucesso (outcome=pass) por classe
C. Taxa de escalation (fallback_reason != null)
D. Taxa de human override
E. Economia real: sum(cost_usd) vs sum(if tier=FRONTIER then frontier_cost else actual_cost)
F. Latência mediana por classe
G. Categorizar per-classe: pronta para active? Continuar shadow?

Saída: JSON estruturado + markdown summary.
```

---

## 6. Roadmap de Ativação Gradual

Não ligar `ROUTER_MODE=active` globalmente.

**Fase Alpha** (10-20 tarefas por classe):
- Coletar baseline
- Identificar padrões de override
- Detectar falsos positivos do Jev

**Fase Beta** (50+ tarefas totais):
- Analisar per classe
- Aplicar critérios de ativação
- **Desbloquear classes específicas** (não global)

**Ativação Gradual** (candidatos primeiro):

```yaml
# config.yaml - conceitual
active_classes:
  - repo_search        # grep sempre TOOL — sem ambiguidade
  - testing            # testes sempre TOOL — determinístico
  - lint               # linter sempre TOOL — determinístico

shadow_classes:        # Jev decide, mas ainda shadow
  - summary
  - documentation
  - debugging
  - frontend_simple
  - backend_simple

forced_frontier:       # Nunca entra em active, nunca downgrade
  - security
  - critical_concurrency
  - ui_design          # Lovable, não Jev

human_escalation_only:
  - operations         # Backup/restore/deploy — precisa de humano
```

---

## 7. Critérios de Ativação por Classe

Para cada classe, ANTES de ativar:

✅ **Determinístico (TOOL apenas)**
- repo_search
- testing
- lint
- operations
→ Podem ser active = true imediatamente — zero risco, Jev nunca entra.

✅ **Simples (TOOL + LOCAL_LLM)**
- summary
- documentation
- debugging
→ Ativar quando: >= 95% sucesso, <= 5% override, zero security violations.

⚠️ **Ambíguo (múltiplos tiers, Jev decide)**
- frontend_simple
- backend_simple
- frontend_complex
→ Ativar quando: >= 95% sucesso, <= 5% override, revisão humana aprovada, nenhuma regressão em testes.

🛑 **Nunca Auto:**
- security
- critical_concurrency
- ui_design (Lovable)
→ Esses SEMPRE controlados por humano ou FRONTIER_LLM.

---

## 8. Métricas de Saída (após 50+ decisões)

Relatório deve incluir:

```
=== ROUTER.3 SHADOW EVALUATION ===

Total decisions: 127
Decisions with outcome: 108 (85%)

Per-class breakdown:
  repo_search: 8/8 PASS (100%) → READY FOR ACTIVE
  summary: 12/13 PASS (92%)    → READY (threshold 95% missed by 3%)
  testing: 14/14 PASS (100%)   → READY FOR ACTIVE
  frontend_simple: 9/11 PASS (82%) → CONTINUE SHADOW
  backend_simple: 7/10 PASS (70%)  → CONTINUE SHADOW / HIGH OVERRIDE RATE

Override patterns:
  - Tier selected by human: LOCAL vs CHEAP 2x (cost optimization)
  - Fallback (local unavailable): 3x (latency +150ms, still acceptable)

Cost analysis:
  - If all candidates were FRONTIER: $12.40 estimated
  - Actual (mixed): $0.38 + $2.80 = $3.18
  - Savings: 74%

Security violations: 0 (hard gates held)

Frontier overrides: 0 (policy enforcement working)

Recommendations:
  - Activate: repo_search, testing, lint
  - Evaluate further: summary, documentation
  - Investigate: frontend_simple, backend_simple (high override rate)
  - Continue forced: security, critical_concurrency

Next steps:
  - Collect 20 more frontend_simple tasks
  - Audit why backend_simple has 70% success (Jev recommendations off?)
  - Schedule next eval in 2 weeks
```

---

## 9. Data Collection (Próximos Passos)

1. Adicionar instruções a AGENTS.md (próxima seção)
2. Estender policy.yaml e models.yaml com task_classes e thresholds
3. Implementar `analyze_shadow.py`
4. Começar a chamar `route.sh` em tarefas reais (voluntário)
5. Chamar `log_outcome.py` ao completar
6. Após 50+ decisões: rodar `analyze_shadow.py`
7. Compilar relatório e decidir próximas classes a ativar

---

## 10. Preservação de Segurança

**Hard gates continuam sempre ativas:**

- `hard_force_frontier` em policy.yaml NÃO tem threshold — é 100% ou nada
- Security violations em log = instant block (não conta como sucesso)
- Jev nunca recomenda downgrade de security-critical

**Fallback testing:**

- Desabilitar local LLM temporariamente
- Confirmar que escalation acontece corretamente
- Confirmar que tasks não falham silenciosamente

---

## 11. Próximas Fases (Não Neste Commit)

- **ROUTER.4**: Análise de 50+ tarefas, relatório de ativação
- **ROUTER.5**: Ativação de classes determinísticas (repo_search, testing, lint)
- **ROUTER.6**: Ativação gradual de classes simples se critérios atingidos
- **ROUTER.7**: Revisão de policy case-by-case (class-by-class, nunca global)
