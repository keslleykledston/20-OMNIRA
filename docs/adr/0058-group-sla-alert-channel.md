# ADR-0058: Alerta de SLA estourado enviado a um grupo de confiança (exceção única e controlada)

## Status
Aceito (2026-11-10), fase 2 do alerta de SLA (ADR-0057), a pedido do dono do produto: um grupo "eleito,
específico e de confiança da Supervisão" recebe uma mensagem de verdade quando o SLA estoura, "apenas se for
viável ou fizer sentido". É viável — a infraestrutura de envio já existe e já é genérica o suficiente — e faz
sentido com as salvaguardas abaixo.

Esta é a **primeira e única** exceção à regra "nada é enviado a grupo", em vigor desde a ADR-0015 e reafirmada nas
ADR-0055/0056. Ela não abre um caminho geral de automação de grupo — continua tudo mais read-only.

## Contexto
`internal/channels/application.ChannelService.SendText(ctx, connID, OutboundTextMessage{ProviderChatID, Text,
IdempotencyKey})` já é genérico: `ProviderChatID` tem precedência sobre `ToE164` e endereça qualquer chat do
provedor, grupo ou 1:1, sem exigir uma linha em `conversations`/`messages`. A capacidade técnica de mandar
mensagem a um grupo sempre existiu nessa camada; o que nunca existiu foi uma decisão de produto que a usasse para
grupos — e por design (ADR-0015 "somente leitura").

O pipeline de entrega hoje em uso (`internal/worker/delivery`, subject `job.channel.send_text.v1`) é acoplado ao
modelo de conversa: "job carries only a message reference; text, recipient and connection are read from the
database inside a tenant session derived from that persisted message" (comentário do pacote). Grupos não são
conversas (ADR-0015 os mantém deliberadamente fora desse modelo), então esse pipeline não serve aqui sem forçar
um grupo a virar uma pseudo-conversa — o mesmo erro que a ADR-0055 já evitou ao não reusar `internal/intelligence`.

O evento `job.routing.sla_breached.v1` já existe e já é publicado de forma durável (ADR-0044, usado pelo toast da
ADR-0057).

## Decisão
1. **Exatamente um grupo por tenant, opt-in explícito e auditado.** `wa_groups` ganha `is_alert_channel BOOLEAN
   NOT NULL DEFAULT false`, `alert_channel_set_by`, `alert_channel_set_at` — mesmo padrão de
   `ai_observer_enabled` (ADR-0055). Um índice único parcial (`WHERE is_alert_channel`) garante no máximo um por
   tenant no banco, não só na UI. Exige `enabled=true` (o grupo precisa estar sendo lido) e `connection_id`
   presente, pela mesma regra "sem leitura, sem recurso dependente" já usada pelo observador de IA.
2. **Nunca um grupo de cliente — reforçado por `CHECK`, não por confiança.** `CHECK (NOT is_alert_channel OR
   account_id IS NULL)`. Um grupo só pode virar canal de alerta se não estiver vinculado a nenhum
   `customer_account` (ADR-0018/migração 000179), e a mesma constraint impede vincular um cliente a um grupo que
   já é o canal de alerta. Nas duas direções, o banco recusa a combinação, não só a tela.
3. **Só SLA estourado (`sla_breach`), nunca "em risco".** O risco já tem o toast in-app (ADR-0057); mensagem de
   WhatsApp é mais intrusiva e fica reservada ao caso mais urgente, reduzindo ruído no grupo.
4. **Consumidor próprio, dedicado, não reusa o pipeline de entrega de conversas.** Novo consumidor JetStream
   (subject `job.routing.sla_breached.v1`, mesmo evento que já existe), que busca se o tenant tem um grupo
   marcado como canal de alerta e, se sim, chama `ChannelService.SendText` diretamente — sem criar linha em
   `messages`/`conversations`. O eco da própria mensagem enviada chega de volta pelo webhook do WAHA como
   qualquer mensagem de grupo (`wa_group_messages`, `from_me=true`) — nenhuma tabela nova para registrar o envio;
   o histórico do grupo já é a prova de que foi mandado.
5. **Texto fixo, nunca conteúdo de cliente.** Mensagem gerada pelo servidor com só a métrica e, no máximo, uma
   referência curta e não reversível (nunca nome de cliente, telefone ou corpo de mensagem) — mesma régua de
   privacidade da ADR-0057.
6. **Idempotência.** `IdempotencyKey` derivado da mesma chave de decisão que a ADR-0044 já usa para deduplicar o
   cruzamento de SLA (`tenant+conversation+episode+step+trigger`) — uma redelivery do JetStream nunca manda a
   mesma mensagem duas vezes.
7. **Falha nunca propaga.** Um envio que falha (WAHA fora, grupo desvinculado, rate limit) é logado e descartado
   — nunca bloqueia nem afeta o motor de roteamento. Efeito colateral best-effort, igual ao toast da ADR-0057.
8. **Auditoria.** Ligar/desligar o canal de alerta é auditado (ação nova `group.alert_channel_enabled` /
   `_disabled`, mesmo padrão do observador de IA); cada envio bem-sucedido ou falho conta num contador de
   observabilidade (nunca o texto da mensagem nos logs).

## Consequências
- Ganho: primeiro canal de alerta de verdade, fechando o ciclo que a ADR-0057 deixou em aberto, sem reabrir a
  regra geral de "nada é enviado a grupo" — a exceção é única, nomeada e tecnicamente contida.
- Custo: uma migration pequena, um consumidor novo e pequeno (mesmo tamanho do observador da ADR-0055), nenhuma
  mudança na UI de grupos além de mais um toggle.
- **Tenancy:** o consumidor roda em sessão de sistema (como o observador), mas nunca cruza tenant — a busca do
  grupo-canal já é por `tenant_id` do evento.
- **Reversibilidade:** migration com `down`; desligar o toggle do grupo ou cancelar a conexão para o envio de
  mensagens a interrompe imediatamente; nenhum dado novo é armazenado além das três colunas em `wa_groups`.
- **Risco aceito:** se o único grupo-canal perder a conexão ou for desabilitado, alertas de SLA estourado
  silenciosamente param de ser enviados ao WhatsApp (o toast da ADR-0057 continua funcionando independente disso)
  — aceito porque o alternativa (alertar sobre a falta de canal de alerta) é recursão desnecessária para a v1.

## Alternativas
- **Reusar o pipeline de entrega de conversas (`job.channel.send_text.v1`), fabricando uma pseudo-conversa para o
  grupo:** rejeitada — forçaria um grupo a existir no modelo de conversa que a ADR-0015 deliberadamente evitou.
- **Permitir mais de um grupo-canal por tenant:** rejeitada por agora — "um grupo eleito e específico" foi a
  descrição original; múltiplos canais é complexidade sem pedido concreto ainda.
- **Alertar também em `sla_at_risk`:** rejeitada — o toast já cobre isso; WhatsApp reservado ao caso mais sério.

## Questões em aberto
- Se o dono do produto quiser, no futuro, mais de um tipo de evento indo para esse canal (ex: escalonamento de
  grupo da categoria H/N do mapeamento de tratativas), isso é decisão nova — este ADR autoriza só `sla_breach`.
- Texto exato da mensagem (string fixa vs. pequena variação por métrica) é detalhe de implementação, não decisão
  de arquitetura; fica registrado no código, não aqui.
