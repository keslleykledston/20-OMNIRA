# Conversation Intelligence — operação (ADR-0017)

Tópicos (assuntos), roteamento, resumos, política de ticket, handoff privado, leitura de imagem/PDF, copiloto e gateway de
ferramentas. **Tudo que age sozinho nasce desligado.** Nada disto roda em produção sem autorização explícita do dono.

## Flags (variáveis de ambiente do api e do worker)

| Variável | Padrão | O que liga |
|---|---|---|
| `OMNIRA_TOPIC_THREADS_ENABLED` | **true** | tópicos e API manual (sem IA, sem automação) |
| `OMNIRA_TOPIC_AUTO_ROUTING_ENABLED` | false | o roteador determinístico passa a vincular mensagens (desligado: só registra o que faria — modo simulação) |
| `OMNIRA_TOPIC_AI_ROUTING_ENABLED` | false | IA propõe tópico **em sombra** (nunca aplica) |
| `OMNIRA_TOPIC_SUMMARIES_ENABLED` | false | geração automática de resumo por tópico (a API manual existe sempre que houver modelo) |
| `OMNIRA_AUTO_TICKET_POLICY_ENABLED` | false | adota o ticket ativo sem dono / abre ticket quando a conversa não tem (nunca um segundo simultâneo) |
| `OMNIRA_PRIVATE_HANDOFF_ENABLED` | false | convites de handoff privado e o resgate no pipeline |
| `OMNIRA_MULTIMODAL_ANALYSIS_ENABLED` | false | leitura de imagem/PDF com a chave Gemini **do próprio tenant** (opt-in dele) |
| `OMNIRA_COPILOT_ENABLED` | false | rascunho de resposta (só sugere; nunca envia) |
| `OMNIRA_AI_TOOL_GATEWAY_ENABLED` | false | gateway de ferramentas com política e aprovação humana |
| `OMNIRA_AI_MODEL_TOPIC_CLASSIFY` / `_TOPIC_SUMMARY` / `_COPILOT` | `OMNIRA_AI_MODEL` | modelo por tarefa |

O `docker-compose.yml` precisa repassar as variáveis ao api e ao worker (decisão do dono); sem isso valem os padrões seguros.
Falha de provedor, ausência de modelo ou worker parado **nunca** impede ingestão, resposta, ticket manual ou tópico manual.

## Ordem sugerida de ativação (cada passo só com evidência do passo anterior)

1. Aplicar migrations 000063–000070 (backup antes; cada `down` foi provada com `scripts/test-migration-roundtrip.sh`).
2. `TOPIC_AUTO_ROUTING` ligado **depois** de olhar a simulação: `GET /api/v1/tenants/{t}/intelligence/evaluation?days=7` mostra, sem
   conteúdo, decisões e a taxa de sobrescrita por pessoas (`router.override_rate`).
3. `TOPIC_AI_ROUTING` (sombra): acompanhar `ai_shadow.agreement_rate` por faixa de confiança antes de qualquer promoção.
4. Resumos, política de ticket, handoff, copiloto, gateway: um de cada vez, observando `summaries.correction_rate`, `tool_calls_by_status`.
5. Multimodal: só depois que o administrador do tenant cola a chave Gemini e aceita o consentimento em *Configurações*.

## Observabilidade

- Prometheus (worker, `/metrics`): `topic_router_decisions_total{status,applied,source}`, `topic_router_latency_seconds`,
  `intelligence_jobs_total{state}`, `topic_ai_shadow_total{outcome}`, `topic_handoff_redemptions_total{outcome}`, mais `media_*` (stage `vision`).
- Banco: `ai_usage` (uma linha por chamada a modelo, com tokens e custo estimado), `routing_decisions` (toda decisão, aplicada ou só proposta, com sinais),
  `ai_tool_calls` (trilha e fila de aprovação), `intelligence_jobs` (fila durável; `state='dead'` pede olhar humano).
- API: `GET /integrations/ai/usage` (uso e orçamento) e `GET /intelligence/evaluation` (qualidade das automações).

## Segurança que não muda com flags

Tenant sempre da sessão; todo conteúdo de cliente/anexo é **não confiável** (zonas separadas no contexto); a IA propõe, o OMNIRA decide;
ferramentas rodam com a permissão de quem pediu e escrita pedida pela IA espera aprovação; token de handoff é opaco, só o hash é guardado e vale uma vez.
