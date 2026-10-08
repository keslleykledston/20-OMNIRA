# ADR-0037: Escrita pelo Hub — assumir e responder em nome da empresa do item

## Status
**Accepted para implementação local** (2026-10-08, "siga" do dono sobre a fatia proposta). Implementado em `fix/integrate-lovable-into-omnira`, **não implantado**.
Numeração 0025–0035 segue reservada aos ADRs da missão (`.agent/multitenant-service-hub/ADRS-ESSENTIAL.md`).

## Contexto
Até aqui o Hub é leitura: a RLS entrega `SELECT` delegado em `tenants`, `conversations` e `messages` (migration 095) e **nenhuma** policy de escrita.
Responder é a parte mais delicada: o operador do Hub **não é membro** do tenant (sem papel, sem `conversation.claim`), o texto sai ao cliente final
**em nome de uma empresa**, e o erro "falei como a empresa errada" é invisível depois de enviado. As rotas de envio existentes exigem
`Source==Direct` + RBAC do tenant e continuam assim.

## Decisão
1. **Capacidade por grant.** `effective_access_grants.can_reply BOOLEAN NOT NULL DEFAULT false` (migration 093, ainda não implantada). Ver ≠ responder; o padrão é
   somente leitura (menor privilégio). `omnira-hubctl grant add ... --reply` concede; renovar **sem** `--reply` remove (o valor é definido exatamente como informado).
   A mesma função SQL de acesso (`has_active_hub_access(..., p_require_reply)`) decide leitura e escrita; não existe segunda definição.
2. **Duas rotas, texto apenas**, atrás de `OMNIRA_HUB_API_ENABLED`:
   `POST /hubs/{hub}/inbox/{item}/claim` e `POST /hubs/{hub}/inbox/{item}/messages` (`Idempotency-Key` obrigatório).
   Sem anexos, templates, notas internas ou transferência: ficam para fatias próprias.
3. **Autorizar na sessão do próprio agente, executar com sessão de sistema.**
   (a) Na sessão RLS do agente: membro do hub → item persistido → fila **atual** da conversa → hub/contrato/grant/escopo/`can_reply` → `EffectiveTenantContext` (Hub).
   (b) A escrita roda em `WithSystemTenantSession` com o tenant **lido do item**, nunca da requisição, e **reconfere a delegação dentro da mesma transação**
   (`has_active_hub_access(agente, tenant, fila, hub, true, true)` com a conversa travada `FOR UPDATE`/`FOR SHARE`). Grant revogado ou `can_reply` removido entre (a) e (b) interrompe a escrita.
   **Nenhuma policy de INSERT/UPDATE foi adicionada** a `conversations`, `messages`, `outbox_events` ou idempotência: a base continua recusando escrita direta de um agente do Hub; este serviço é a única porta e audita.
4. **Assumir é explícito.** Responder sem assumir é 409 (não há claim implícito). A conversa precisa estar atribuída **ao agente**; não há bypass de gerente (agente do Hub não tem papel no tenant).
   Assumir é idempotente para quem já detém; outro agente recebe 409; dois agentes simultâneos → exatamente um vence (linha travada `FOR UPDATE`). Histórico `assignment_events` com motivo `hub_claim`.
   Não se aplicam capacidade/disponibilidade de fila do tenant (o agente não é membro de fila); o operador do hub assume por decisão própria.
5. **Execução reaproveitada.** `messages/application.DelegatedSender` usa o mesmo `OutboundStore.InsertQueued` (mensagem + job de entrega + toque na conversa numa instrução), as mesmas regras
   de janela de 24 h, canal pronto e atendimento finalizado, e `sent_by_user_id` = o agente. Idempotência por `(tenant, remetente, chave)`; mesma chave com outro texto → 422.
6. **"Respondendo como [empresa]" é verificado pelo servidor.** O corpo traz `expected_tenant_id` (a empresa que a tela mostra). Ele **não autoriza nada**: divergência com o tenant persistido do item → 409
   e nada é gravado. A resposta devolve a empresa pela qual o servidor de fato respondeu. Campos desconhecidos no corpo (ex.: `tenant_id`) → 400; `?tenant_id=` → 400.
7. **Erros.** Sem grant/hub forjado/item inexistente/fora do escopo/revogado: 404 uniforme. Grant somente leitura: 403 (o agente já enxerga o item, nada novo é revelado).
8. **Auditoria** na mesma transação: `hub.conversation.claimed` e `hub.message.sent` (ator = agente, hub, grant, contrato, id da mensagem; sem o texto).
9. **UI.** O compositor existente (`MessageComposer`) é reutilizado (sem segundo composer), com rascunho por `hub:{tenant}:{conversa}`, rótulo fixo "Respondendo como {empresa}",
   estados derivados do servidor (`access.can_reply`, `conversation.assignment` = none|me|other, sem nomear outro operador) e chave de idempotência reaproveitada só na repetição do mesmo texto.

## Alternativas rejeitadas
- **Policies de INSERT/UPDATE delegadas** em messages/conversations/outbox: ampliam a superfície da RLS em muitas tabelas e em vários caminhos de escrita; um erro numa delas é escrita cruzada.
- **Fazer o `Sender` humano aceitar `Source==Hub`**: afrouxa um invariante que hoje protege todas as escritas existentes.
- **Claim implícito ao responder**: esconde a mudança de dono da conversa e dificulta a auditoria.
- **Tenant escolhido pelo cliente**: contra a diretiva (frontend nunca é autoridade de tenant).

## Consequências
- O agente do Hub aparece como `assigned_to_user_id` de uma conversa de um tenant do qual não é membro. As telas do tenant que resolvem nome por membership podem mostrar o atribuído sem nome; **não verificado na UI do tenant** (pendência).
- Sem verificação de elegibilidade operacional (capacidade/disponibilidade) no claim do Hub.
- Cookie de sessão sem proteção CSRF dedicada nas rotas de escrita, igual às rotas de escrita existentes do tenant (postura herdada, não alterada aqui).
- Provas: `internal/hub/adapters/reply_integration_test.go` (Postgres real, HTTP), `internal/messages/application/delegated_send_test.go`, 16 mutações em `scripts/test-hub-reply-mutations.sh`, smoke com binários reais em `scripts/e2e-hub-smoke.sh`.
