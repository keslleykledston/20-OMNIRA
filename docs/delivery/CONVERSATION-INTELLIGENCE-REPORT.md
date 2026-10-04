# Conversation Intelligence (ADR-0017) — relatório final

Data: 2026-10-04 · Branch: `main` · Baseline: `b5a71fc` · HEAD ao fechar este relatório: ver `git log` (último commit de código/testes `95b2bf5`).
**Nada foi enviado (push), implantado, migrado no banco real nem reiniciado.** Todas as automações nascem desligadas.

## Commits por onda (locais)

| Onda | Commit | Resultado |
|---|---|---|
| 0 ADR-0017 | `87804b7` | PASS |
| 1 Fundação de tópicos | `86f2222` | PASS (mig. 063) |
| 2 Participantes externos/grupos/reply | `5b0063d` | PASS (mig. 064) |
| 3 Entidades + auditoria + roteador determinístico | `193daeb` | PASS (mig. 065) |
| 4 Pipeline durável de eventos | `9a1fb0b` | PASS (mig. 066) |
| 5 Contexto + resumos versionados | `e423dc6` | PASS |
| 6 Roteador de modelos + IA em sombra | `a08a944` | PASS |
| 7 Política de ticket + backfill legado | `18903e0` | PASS |
| 8 Handoff privado | `f2f64a4` | PASS (mig. 067) |
| 9 Multimodal Gemini (imagem/PDF) | `edd4970` | PASS (não ligado ao worker até a onda 10) |
| 10 Contabilização de uso de IA | `999e8f9` | PASS (mig. 068) |
| 11 Copiloto (backend) | `bd4e28c` | PASS |
| 11 Superfície web de tópicos | **não commitada** | construída e verde; aguarda aprovação visual do dono |
| 12 Gateway de ferramentas com política | `8ee2775` | PASS (mig. 069) |
| Merge / split humanos | `ef345a5` | PASS (mig. 070) |
| Avaliação + guia de operação | `e42a052` | PASS |
| Matriz final A–J + correção de escopo de entidade | `e38d422` | PASS |
| Verificação qualitativa de desempenho | `95b2bf5` | PASS |

Intervalo `b5a71fc..HEAD`: 140 arquivos, +19.076/−149 linhas (73 arquivos Go de produção, 42 de teste, 8 migrations up+down).

## Banco (tabelas novas)
`topic_threads`, `message_topic_links`, `topic_conversation_links`, `topic_ticket_links`, `topic_summaries`, `channel_participants`, `conversation_channel_participants`, `topic_entities`, `group_message_topic_links`, `topic_group_links`, `routing_decisions`, `ambiguity_cases`, `conversation_topic_focus`, `intelligence_jobs`, `topic_handoffs`, `ai_usage`, `ai_tool_calls`. Colunas novas em `messages`/`wa_group_messages` (remetente, resposta) e em `topic_threads` (merge/split). **Reaproveitado, não duplicado:** `message_media` e `message_media_analysis` (em vez de `message_attachments`/`media_analyses`). Todas com RLS forçada e FKs compostas `(tenant_id, id)`.

## Endpoints novos (todos sob `/api/v1/tenants/{tenant_id}`)
Tópicos: `GET|POST /inbox/conversations/{id}/topics`, `GET|PATCH /topics/{id}`, `GET|POST /topics/{id}/messages`, `DELETE …/messages/{mid}`, `GET /topics/{id}/tickets`, `POST …/tickets/link`, `GET /contacts/{id}/topics`, `POST /topics/{id}/merge`, `POST /topics/{id}/split`.
Ambiguidade: `GET /inbox/conversations/{id}/ambiguities`, `POST /ambiguities/{id}/resolve`.
Resumo: `GET /topics/{id}/summaries`, `POST …/summary/{generate,confirm,correct}`.
Chamado: `GET /topics/{id}/ticket-policy`, `POST …/ticket-policy/apply`, `POST /inbox/conversations/{id}/legacy-topic`.
Handoff: `POST|GET /topics/{id}/handoffs`, `POST …/handoffs/{hid}/revoke`.
Copiloto: `POST /topics/{id}/copilot/suggest-reply`.
Gateway: `GET /topics/{id}/ai/tools`, `POST …/ai/tools/invoke`, `GET …/ai/tool-calls`, `POST …/ai/tool-calls/{cid}/{approve,reject}`.
Admin: `GET /integrations/ai/usage`, `GET /intelligence/evaluation`.
Tudo no OpenAPI (`contracts/openapi/omnira-v1.yaml`).

## Eventos
`job.inbox.message_persisted.v1` (outbox, mesma transação da mensagem; só referências, sem conteúdo). Consumidor durável idempotente em `internal/worker/intelligence` (teste com entrega duplicada real no JetStream). Métricas: `topic_router_decisions_total`, `topic_router_latency_seconds`, `intelligence_jobs_total`, `topic_ai_shadow_total`, `topic_handoff_redemptions_total`.

## Provedores de IA
- Implementados: OpenAI (texto, já existente, agora com tokens), Gemini (imagem/PDF, chave do tenant), Whisper local (áudio, já existente).
- **Chamadas externas reais feitas nesta entrega: NENHUMA.** Todos os testes usam geradores falsos / `httptest`. Não há smoke real de Gemini (exige chave do tenant) nem de OpenAI.
- Flags (padrão): `topic_threads` **ligada**; `topic_auto_routing`, `topic_ai_routing`, `topic_summaries`, `auto_ticket_policy`, `private_handoff`, `multimodal_analysis`, `copilot`, `ai_tool_gateway` **desligadas**.

## Gates
| Gate | Resultado |
|---|---|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test ./...` (unitários) | PASS |
| Integração Go com Postgres/NATS reais (`scripts/test-integration.sh`, 25 pacotes, 1 banco por pacote) | PASS 25/25 |
| Migration em banco novo (70 migrations + validação de schema) | PASS |
| Round trip up→down→up das migrations 063–070 (`scripts/test-migration-roundtrip.sh`) | PASS (8/8) |
| RLS / cross-tenant (testes por onda + cenário J) | PASS (mutações verificadas) |
| `git diff --check` e varredura de segredos em `b5a71fc..HEAD` | PASS |
| Frontend: vitest | PASS 508/508 (inclui 19 novos) |
| Frontend: `tsc` | PASS |
| Frontend: `vite build` | PASS |
| Playwright (API mockada, 3 cenários) | PASS |
| Playwright em stack real (`e2e-inbox.sh`) | **NÃO EXECUTADO** — esse script põe um worker descartável no NATS real |

Observação de ferramenta: o gate de integração deve rodar **sem argumentos**. Com `./...` tudo roda num banco só e o teste pré-existente do publisher (publica todo evento pendente) falha por eventos de outros pacotes; não é falha de produto.

## Matriz final A–J
A grupo multi-assunto — PASS (a mensagem do Pedro, sem evidência, vira ambiguidade e uma pessoa decide). B mesmo contato alternando — PASS parcial por desenho: a IA em sombra propõe os 3 destinos certos, mas **nunca aplica**; uma pessoa confirma (avaliação mede 3/3). C resposta direta — PASS. D uma mensagem, dois assuntos — PASS. E handoff grupo→privado→resumo→confirmação→chamado — PASS (confirmação do cliente é no serviço; não há canal ainda). F dois chamados do mesmo contato — PASS (conversas distintas). G omnichannel — PASS, e exigiu corrigir uma falha real (abaixo). H IA fora do ar — PASS. I evento duplicado — PASS (sem duplicar tópico, vínculo, resumo, chamado, handoff). J ataque cross-tenant — PASS.

**Falha real encontrada e corrigida:** a correspondência por entidade era do tenant inteiro; outro cliente digitando o mesmo número de pedido seria colado no tópico do primeiro. Agora só vale no mesmo contêiner ou contato (mutação confirmada). Também: número de série deixou de abrir assunto novo.

## Desempenho (qualitativo, conversa com 3.000 mensagens / 40 tópicos)
listar tópicos 46 ms · página de mensagens do tópico 2 ms · contexto do tópico 7 ms · uma decisão do roteador 7 ms · relatório de avaliação 3 ms. Latência do webhook **não** medida de ponta a ponta (a mudança é 1 insert de outbox por mensagem recebida, na mesma transação).

## Lacunas
- **P0:** nenhuma no código.
- **P1:** UI de tópicos aguardando aprovação visual (não commitada); confirmação do resumo pelo cliente sem canal próprio; promoção da IA além da sombra (decisão após medir `ai_shadow.agreement_rate`); mensagens só-mídia não são reroteadas depois da leitura; UI de merge/split e "desfazer"; segundo chamado simultâneo na mesma conversa exige decisão de ADR (`tickets_active_conversation_uq`).
- **P2:** renomear tópico pela UI; acesso ao painel no celular não verificado; sem contadores Prometheus de resumo/copiloto/gateway (há `ai_usage` e a avaliação); teste do publisher não hermético em banco compartilhado; migrations 055–059 sem prova de `down`; unificação automática de identidade (pendência K3G do PRODUCT.7B).
- **FORA DO ESCOPO / NÃO AUTORIZADO:** push, deploy, migration no banco real, reinício de serviços, chamadas reais a provedores.
- **BLOQUEADO EXTERNAMENTE:** cliente OAuth do Google Drive (só o dono cria); chave Gemini por tenant (administrador do tenant); passar as variáveis de flag no `docker-compose.yml` (arquivo do dono).
