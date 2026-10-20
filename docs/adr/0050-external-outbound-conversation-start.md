# ADR-0050: Abrir conversa outbound por telefone na API externa (Headless)

## Status
**Aceito e implementado (2026-10-20).** Reabre a "fase D" de `docs/ops/HEADLESS-CHATWOOT-MAPPING.md` §4-G5 e §7.1, a pedido explícito do dono do produto nesta sessão.

Implementado como desenhado: `POST /api/v1/external/conversations` (scopes `contact.write` + `conversation.open`), migration `000172`, novo método `conversationcontrol.Service.CreateHeldByApplication` (o único lugar do sistema em que o controle de uma conversa é fixado na própria inserção, não por uma transição de UPDATE — porque não existe holder anterior a tomar). Testes Tenant A/B, allowlist de canal/fila, reuso com e sem posse, idempotência e canal ambíguo, todos contra PostgreSQL real. Detalhes no CHANGELOG.

O dono do produto decidiu destravar a implementação sem esperar a resposta do CRM sobre o número do Gateway, porque essa resposta **não muda o desenho técnico** deste ADR (seção "Decisão", item 1 já condiciona `channel_id` à allowlist da instalação chamadora — o mecanismo é o mesmo esteja o número compartilhado com o atendimento ou não). O que a resposta pendente muda é só:
- a **urgência operacional** de uma migração separada do inbox de alerta para dentro do OMNIRA, caso o número seja de fato compartilhado com o atendimento (sessão única do WhatsApp oficial) — isso é decisão de operação/infra, não de arquitetura da API, e fica registrado como follow-up em aberto, não como bloqueio de engenharia;
- nada no contrato do endpoint, nos scopes ou nos testes de tenancy.

Questões em aberto 2 e 3 (catálogo de template e auto-claim) resolvidas abaixo.

## Contexto
O pacote `OMNIRA_API_PENDENCIAS_CRM.md` (equipe CRM K3G, out/2026) pede um endpoint que crie ou reutilize uma conversa a partir de um telefone E.164, usado hoje pelo Gateway de monitoramento do CRM para abrir alertas via WhatsApp quando não há conversa aberta. Essa mesma lacuna já tinha sido mapeada como **G5** em `HEADLESS-CHATWOOT-MAPPING.md` §4 e §7.1 em 2026-10-17, quando **o dono não soube decidir** e a recomendação registrada foi adiar para uma "fase D", por ser "a parte mais arriscada" (envio ativo a terceiro, exige template aprovado pela Meta fora da janela de 24h, exige consentimento).

Hoje, confirmado por inspeção de código nesta sessão:
- Não existe `POST /api/v1/external/conversations` nem qualquer rota que aceite `phone_e164` — só `POST .../conversations/{id}/...`, que exige uma conversa já existente (`internal/platform/httpserver/server.go`, `internal/external/adapters/write_http.go`).
- Não existe nenhuma função `StartConversation`/`CreateOrReuse` em nenhuma camada do domínio (Workspace, Hub ou external) — a capacidade de abrir conversa por telefone **não existe no OMNIRA**, não é só ausência na API external.
- O catálogo de scopes (`internal/external/domain/scope.go`) é fechado e não tem `contact.write` nem `conversation.open`.
- `422 window_closed` (janela de 24h) já existe e já é retornado em `POST .../messages` quando a conversa está fora da janela — a regra de negócio da Meta já é conhecida pelo motor.

A pergunta que ficou sem resposta em 2026-10-17 (§7.1) permanece em aberto e é condição para dimensionar o risco deste ADR: **o inbox dos alertas do Gateway usa o mesmo número de WhatsApp do atendimento, ou um número diferente?** Se for o mesmo número, o Chatwoot não pode continuar ligado a ele ao mesmo tempo que o OMNIRA (sessão única do WhatsApp oficial), e este ADR sobe de prioridade/urgência; se for número diferente, nada muda na prioridade atual.

## Decisão
1. **Novo caso de uso de domínio `StartConversation`** (fora do pipeline de ingestão inbound), que:
   - aceita `phone_e164` (E.164 obrigatório), `queue_id` e/ou `channel_id` opcionais (respeitando a allowlist da instalação chamadora), `external_agent_id` informativo;
   - **cria ou reusa** `Contact` + `Conversation` do tenant, nunca cruzando tenant (`TenantContext` obrigatório, igual a toda query tenant-owned);
   - é **idempotente** por `Idempotency-Key` + telefone, usando o mesmo mecanismo de `external_command_receipts` já existente para os outros comandos externos (`internal/external/adapters/write_http.go`);
   - fora da janela de 24h, **exige** `template_name`/`language`/`components` no mesmo payload (create+template numa só chamada) — nunca abre conversa e manda texto livre fora da janela;
   - devolve `conversation_id`, `contact_id`, `control.version`, `status`.
2. **Dois scopes novos**, mínimo privilégio, closed-list como os demais:
   - `contact.write`: achar/criar contato por telefone (não edita campos arbitrários do contato, só o necessário para abrir conversa);
   - `conversation.open`: abrir/reusar conversa via este fluxo (não substitui `conversation.claim`; a aplicação que abriu ainda precisa `claim` para responder, exceto se a mesma chamada já assumir — a definir na implementação).
3. **Endpoint**: `POST /api/v1/external/conversations`, hoje recusado com 405 (`Allow: GET, HEAD`) por não existir — o roteador está correto, a capacidade é que falta.
4. **Erros classificados** (consistente com o padrão já usado pelos outros endpoints external): `400/422 invalid_phone`, `403 queue_not_allowed`/`channel_not_allowed`, `409 held_by_other`, `422 window_closed` (sem template), `429` (rate limit já existente).
5. **Consentimento/LGPD**: a allowlist de queue/channel da instalação não substitui consentimento do destinatário — a aplicação chamadora (CRM) é responsável por só acionar números com opt-in válido para alerta; o OMNIRA documenta essa responsabilidade no contrato, não a impõe tecnicamente nesta fase (igual à postura de ADR-0024 para anexos de saída).
6. **Tenancy**: mesmos testes obrigatórios de sempre (CLAUDE.md) — Tenant A/B, tentativa de acesso cruzado, leitura e escrita, membership/credencial revogada, contra PostgreSQL real.

## Consequências
- Ganho: o Gateway de alertas do CRM deixa de depender do Chatwoot para o fluxo de notificação outbound; o OMNIRA passa a ter, pela primeira vez, uma capacidade de conversa **iniciada pela empresa** (hoje só existe iniciada pelo contato).
- Custo: nova superfície de risco (envio ativo a terceiros) — exige disciplina de template aprovado e consentimento, cujo controle técnico fica limitado nesta fase (ver item 5).
- Isso NÃO altera `ScopeConversationClaim`/`transfer`/`finalize` existentes nem o pipeline de ingestão inbound — é aditivo.
- Bloqueia a implementação até a pergunta da seção "Questões em aberto" ser respondida pelo CRM/dono, porque ela muda a urgência e pode mudar o desenho (se o número for compartilhado com o Chatwoot, pode ser necessário migrar o inbox de alerta junto com o atendimento, não só adicionar o endpoint).

## Alternativas
- **Manter no Chatwoot indefinidamente** (status quo de 2026-10-17): evita o risco agora, mas perpetua duas ferramentas vivas e não resolve a pendência P0 do CRM. Rejeitada como solução permanente; aceitável só como ponte até este ADR ser aceito.
- **Expor outbound sem exigir template fora da janela**: mais simples, mas viola a política da Meta e arrisca o número oficial (ban). Rejeitada.
- **CRM mantém a lógica de outbound no próprio Chatwoot-replacement e o OMNIRA só expõe `contact.search` + `conversation.create` genérico sem template embutido**: separa as duas chamadas (cria contato/conversa já dentro da janela só se houver conversa anterior; senão falha e obriga o CRM a resolver template por fora). Mais simples do lado do OMNIRA, mas empurra a decisão de compliance para o CRM sem dar a ele o catálogo de templates (depende de ADR-0051... tratado em separado, ver pendência de listagem de templates). Considerar se o esforço do item "Decisão" acima for grande demais para uma primeira fatia.

## Questões em aberto
1. **(Não bloqueante, follow-up operacional)** O inbox de alertas do Gateway usa o mesmo número de WhatsApp do atendimento, ou outro número? Ainda sem resposta do time CRM K3G desde 17/10 — não bloqueia este ADR (ver "Status"), mas decide se é preciso um projeto separado de migração do inbox de alerta para dentro do OMNIRA.
2. **Resolvido:** catálogo de template é livre por tipo de alerta — a aplicação chamadora escolhe entre os templates que `GET /api/v1/external/templates` (ADR-0052, já implementado) já retorna para a instalação (aprovados, `sendable=true`, dentro da allowlist de canal); não há catálogo fixo adicional específico de "alerta" nesta fatia.
3. **Resolvido:** `conversation.open` assume (`claim`) automaticamente a conversa **somente quando ela é criada por esta chamada** (não havia controlador anterior, não é "tomar" de ninguém). Se a chamada **reusa** uma conversa já existente e já controlada por outra parte (Workspace ou outra instalação), não há claim automático — a chamada falha com `409 held_by_other`, igual ao restante do padrão de controle de conversa; a aplicação chamadora precisa de um `conversation.claim` explícito (fora de escopo desta fatia) para assumir uma conversa pré-existente.
