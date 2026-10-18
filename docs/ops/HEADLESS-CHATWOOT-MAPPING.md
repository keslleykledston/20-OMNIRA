# CRM K3G: do Chatwoot para o OMNIRA como motor (mapeamento)

Estado: **decisões do dono tomadas em 2026-10-17 (§7); G1, G2 e G8 implementados e em produção (api).** Entrada: `OMNIRA_CHATWOOT_API_USADA_PELO_CRM.md` (equipe do CRM, 2026-10-17). Base no OMNIRA: ADR-0048 (headless), `docs/ops/HEADLESS-P2-K3G.md`, rotas em `internal/platform/httpserver/server.go` (`RegisterExternalHandlers`, `RegisterExternalWriteHandlers`).

## 1. Decisão recomendada: o CRM troca o provedor, o OMNIRA não imita o Chatwoot

O módulo Atendimento do CRM já tem uma fachada própria (`/api/support/chatwoot/*`) e o navegador nunca fala com o Chatwoot. Então o ponto de troca é **uma camada dentro de `crm-api/src/support/chatwoot/*`**: criar um `OmniraProvider` com a mesma interface interna e apontar a fachada para ele. O front do CRM não muda.

| Opção | Prós | Contras |
|---|---|---|
| **A. CRM ganha `OmniraProvider` (recomendada)** | Usa o contrato real do OMNIRA (cursor, `If-Match`, `Idempotency-Key`, lease, journal). Nenhuma mentira de semântica. O OMNIRA não carrega um contrato legado. | Trabalho do lado do CRM (um adaptador, ~15 funções). |
| B. OMNIRA expõe `/api/v1/accounts/{id}/...` estilo Chatwoot | CRM só troca URL/token. | O OMNIRA teria de fingir `assignee_id` por agente, `labels`, `page`, `echo_id`, `message_type` 0/1/2. Semânticas diferentes (ver §3) quebrariam em silêncio. Segunda API para manter, auditar e testar com duas empresas. |

Recomendação: A. Se a equipe do CRM insistir em B, ela vira um ADR próprio (fachada de compatibilidade, só leitura e as escritas do §2 já cobertas) e **não** entra antes de A estar provada.

## 2. Mapa rota a rota

Legenda: **OK** = existe hoje na API externa (ADR-0048). **ADAPTA** = existe com outra semântica. **LACUNA** = não existe, precisa de decisão.

Base OMNIRA: `https://<host>/api/v1/external`, `Authorization: Bearer omn_ext_<id>.<segredo>`. O **tenant vem da instalação** (não existe `accountId` na URL) e o `inbox_id` vira `channel_connection_id`.

| Chatwoot (CRM usa hoje) | OMNIRA | Situação | Nota |
|---|---|---|---|
| `GET /agents` (email → assignee_id) | nenhuma | **ADAPTA** | O titular no OMNIRA é a **aplicação**; o atendente humano do CRM vai em `external_agent_id` (informativo). Não existe mapa e-mail → id. Ver §3.1 |
| `GET /conversations` (status, sort, page, inbox_id) | `GET /conversations?status=open\|closed\|all&control=free\|held\|mine&channel_connection_id=&queue_id=&sort=created\|updated&limit=&cursor=` | **OK** | Paginação por cursor, não `page=N`. Máx 200 por página |
| `GET /conversations/{id}` | `GET /conversations/{id}` | **OK** | Traz `control` (holder, versão, `mine`) e `can` (o que esta aplicação pode fazer agora) |
| contato (`meta.sender`: nome, telefone) | `GET /contacts/{id}` | **OK** | `phone_e164`, `name`, `whatsapp_name`, `kind` |
| `GET /conversations/{id}/messages?before/after` | `GET /conversations/{id}/messages?order=asc\|desc&limit=&cursor=` | **OK** | Keyset por `(created_at, id)`; `before/after` vira cursor |
| campos da mensagem: `message_type` 0/1/2 | `direction` + `sender.kind` (`contact\|agent\|crm_application\|automation`) | **ADAPTA** | `activity` (2) não é mensagem: vem como evento (`conversation.control_changed`, `conversation.closed`) |
| `echo_id` (dedupe de eco) | `Idempotency-Key` no `POST` + `sender.mine` | **ADAPTA** | O eco do CRM se reconhece por `sender.kind = crm_application` e `mine = true`, sem prefixo `crm-` |
| `POST /conversations/{id}/messages` (texto) | `POST /conversations/{id}/messages` `{text}` ou `{template}` | **OK** | Mesma janela de 24 h e mesmos templates do Workspace. `Idempotency-Key` obrigatório. `409 not_holder` se a aplicação não segura a conversa (assuma antes), `409 held_by_other` se outro titular |
| `POST .../messages` multipart (anexos) | `POST /conversations/{id}/attachments` (multipart) e depois `POST /messages` com `attachment_id` | **ADAPTA** | Dois passos (subir, enviar). Ver §4-G1 |
| `private: true` (nota interna) | nenhuma | **LACUNA** | Ver §4-G3 |
| `POST /conversations/{id}/assignments` `{assignee_id}` | `POST /conversations/{id}/claim` / `release` (+`heartbeat`) | **ADAPTA** | Assumir/soltar é da aplicação, com lease e `If-Match`. Ver §3.2 |
| `POST /conversations/{id}/labels` (equipe) | `POST /conversations/{id}/transfer` `{queue_id}` | **ADAPTA** | Fila = setor (ADR-0043). Falta listar filas (§4-G2) |
| labels livres | nenhuma | **LACUNA (não recomendo)** | O CRM guarda no próprio banco |
| `POST .../close-attendance` (msg + unassign + limpar labels) | `POST /conversations/{id}/finalize` | **OK** | Corpo `{reason, note, external_agent_id}`. Finalizar encerra conversa **e** protocolo (ADR-0049); é melhor que o "unassign + limpar" do CRM: fecha de verdade. `notify_protocol` opcional (§4-G8) |
| `POST .../update_last_seen`, `/notifications` | nenhuma | **LACUNA** | Ver §4-G4 |
| `GET /contacts/search`, `POST /contacts`, `contact_inboxes`, `GET /contacts/{id}/conversations` | nenhuma | **LACUNA** | Ver §4-G5 |
| `POST /conversations` (abrir outbound) | nenhuma | **LACUNA** | Ver §4-G5 |
| webhook `message_created` / `message_updated` | **o CRM consulta** `GET /events?after=<seq>` (partida em `GET /sync/checkpoint`) | **ADAPTA** | **Decisão do dono (2026-10-17): sem webhook.** O OMNIRA não empurra nada para o CRM; o CRM puxa o diário. Eventos levam **referências, não o texto**: ao ver `message.created.v1` o CRM busca a mensagem por `GET`. O diário é durável: o que o CRM não leu fica esperando |
| Platform API (`/users`, `account_users`) | tela Integrações → Aplicações | **n/a** | Instalação, credencial e escopos são criados no OMNIRA pelo dono |
| `POST /upload` (imagem inline) | nenhuma | **n/a** | Fica no CRM |
| grupos WhatsApp (`@g.us`) | não verificado | **VERIFICAR** | A visibilidade externa exclui interna/spam. Falta confirmar se o grupo aparece e como se distingue (§4-G6) |

Eventos do journal hoje: `conversation.created.v1`, `conversation.control_changed.v1`, `conversation.closed.v1`, `message.created.v1`, `message.status_changed.v1`, `contact.updated.v1`, `ticket.linked.v1` (mais os de serviço, somente leitura).

## 3. Diferenças de modelo que o adaptador precisa absorver

### 3.1 Quem é o "atendente"
No Chatwoot cada atendente é um usuário (`assignee_id`). No OMNIRA a conversa tem **um titular tipado**: pessoa do OMNIRA, Hub, ou **a aplicação CRM** (`crm_application`). Os atendentes do CRM ficam atrás da aplicação. Consequências:
- O CRM deixa de precisar de `GET /agents` e do casamento por e-mail. Ele manda `external_agent_id` (o id do atendente no CRM) em claim, transfer, message e finalize; vale para auditoria e relatório, **não** é autorização.
- O OMNIRA não sabe "quem" no CRM; se o dono quiser identidade verificada, é `crm_agent_verified` (existe no domínio, **NOT WIRED**, exige federação real).

### 3.2 Assumir = lease, não atribuição eterna
`claim` devolve posse com **lease** (recomendado 900 s) e `heartbeat`. O CRM que parar de responder devolve a conversa ao roteador sozinho. O adaptador precisa: enviar `heartbeat` enquanto o atendente está com a tela aberta; mandar `If-Match: "control-N"` (a versão de controle que veio no `ETag` do `GET`; a falta dá `428 if_match_required`, versão velha dá `412 stale_control_version`); tratar `409` (`not_holder`, `held_by_other`, `conversation_closed`, `outside_hours`); e reler `can` antes de habilitar botões.

### 3.3 Etiqueta de equipe = fila
`provedor_telecom`, `suporte_ti`, `servidores`, `financeiro` viram **filas** (ADR-0043: fila = setor). `transfer {queue_id}` move a conversa para a fila e a devolve ao roteador do setor (horário, elegibilidade, sobrecarga). O CRM mantém uma tabela `label → queue_id` (configuração).

### 3.4 Escrita compartilhada
Enquanto o CRM segura a conversa, o Workspace recusa a atribuição/resposta (ADR-0048 §2.4) e o bot não fala (ADR-0048 §2.4). Supervisor com `conversation.takeover_external` retoma com motivo. Isso substitui o "último a escrever ganha" do Chatwoot.

## 4. Lacunas e proposta (em ordem de valor)

| # | Lacuna | Proposta | Esforço / cuidado |
|---|---|---|---|
| G1 | **Enviar anexo** (imagem, áudio, vídeo, documento) | **FEITO (2026-10-18), migration 167:** `POST /external/conversations/{id}/attachments` (multipart, campo `file`, até 16 MiB, escopo `message.send`; a instalação precisa **deter** a conversa) devolve `attachment_id`; depois `POST /messages` com `attachment_id` (o `text` vira a legenda, até 1024 caracteres), com `Idempotency-Key` e `If-Match` como o texto. Mesmo pipeline do operador: tipo pelos bytes, metadados removidos, estrutura, ClamAV (falha = recusa), cotas **por instalação** (5 pendentes por conversa, 64 MiB, 30 por minuto), chave "Anexos de saída" da empresa (ligada por padrão) e janela de 24 h na linha oficial. O arquivo pertence à instalação; outra instalação, outra conversa ou outra empresa recebe o mesmo `422 attachment_unavailable`. Áudio: o CRM converte para MP3 como hoje. **Falta a prova ao vivo** (número de teste: foto, PDF, nota de voz OGG, MP4; a prova do pipeline de saída ainda está pendente desde R-4) | — |
| G2 | **Listar filas** (para o mapa label → fila) | **FEITO**: `GET /external/queues` (`id`, `name`), escopo `conversation.read`, filtrado pela allowlist da instalação; teste com duas empresas | — |
| G3 | **Nota interna** (`private`) | Não expor agora. O CRM guarda a nota no próprio ticket. Se for essencial: nota de conversa (hoje as notas são do contato, sem vínculo com conversa) | Alto para o ganho; recomendo adiar |
| G4 | **Lido / não lido** | O CRM calcula não lido pelo journal (`message.created.v1` inbound após o último `seen` dele). Sem endpoint | Zero no OMNIRA |
| G5 | **Abrir conversa por telefone** (alertas Gateway, notificação WhatsApp) | O maior item. Precisa: achar/criar contato (`contact.write`), abrir conversa na linha oficial com **template aprovado** (fora da janela de 24 h), respeitar opt-in e a janela. Escopos novos `contact.write` e `conversation.open`, ADR próprio | Alto. Decisão do dono: o alerta do Gateway continua pelo Chatwoot até existir, ou usa o canal atual do OMNIRA (aviso de sistema) |
| G6 | **Grupos** | Verificar na API externa como o grupo é classificado e expor um `kind` (`direct\|group`) em `GET /conversations`, sem identidade de participantes | Pequeno, depois de confirmar o comportamento atual |
| G8 | **Avisar o protocolo ao cliente na finalização** | **FEITO**: `notify_protocol` (opcional, padrão sim) no corpo de `finalize`. Ao fazer isso achei e corrigi um **defeito**: o `finalize` externo fechava a conversa mas **não encerrava o protocolo** nem avisava o cliente (a regra "Finalizar encerra tudo" valia só para pessoas). Agora encerra o protocolo (criando um se o atendimento nunca teve) e, onde o aviso de protocolo está ligado (K3G_Solutions), o cliente recebe o número como mensagem da própria aplicação, a menos que o CRM mande `notify_protocol: false` (recomendado, já que o CRM envia sua mensagem de encerramento) | — |
| G7 | **Busca por telefone/nome** | `contact_id` já vem na conversa. Busca livre só se G5 for aprovado | Junto com G5 |

## 5. O que o CRM faz do lado dele (adaptador)

Interface sugerida de `OmniraProvider` (espelha as funções que a fachada já chama):

| Função da fachada | Chamada OMNIRA |
|---|---|
| `listConversations` | `GET /conversations?status=open&sort=updated` (cursor até esgotar) + `GET /events?after=N` para manter fresco |
| `getConversation` | `GET /conversations/{id}` + `GET /contacts/{contact_id}` |
| `listMessages` | `GET /conversations/{id}/messages?order=desc&cursor=` |
| `sendText` | `POST .../messages` com `Idempotency-Key` e `external_agent_id` |
| `assign` | `POST .../claim` (`If-Match`) e `heartbeat`; `null` = `release` |
| `setTeamLabel` | `POST .../transfer {queue_id}` pelo mapa label → fila |
| `closeAttendance` | `POST .../finalize` (com `notify_protocol` conforme a tela) |
| `markRead` | local (journal) |
| tempo real | laço de consulta: `GET /events?after=<último seq>&limit=200` a cada 2–3 s (≈ 20–30 req/min), gravando o último `seq` processado; para cada fato, `GET` do que mudou. `410 resync_required` ⇒ `GET /sync/checkpoint` e refazer o snapshot. Descartar `event_id` repetido (idempotência do lado do CRM). **Não existe receptor, URL pública nem assinatura no CRM** |

Regras do lado do CRM (já pedidas no P2): backoff exponencial em `429`/`5xx`, `Idempotency-Key` em todo `POST`, `If-Match` com a versão de controle, no máximo 5 chamadas simultâneas.

## 6. Plano de migração (sem derrubar o Chatwoot)

1. **Fase A, 24 h só leitura** (já planejada em `HEADLESS-P2-K3G.md`): o CRM liga o `OmniraProvider` em modo sombra, compara lista e mensagens com o Chatwoot, não escreve.
2. **Fase B, escrita em UMA fila (Suporte)**: claim, enviar texto, transferir, finalizar. Chatwoot segue para as outras filas.
3. **Fase B inclui anexos (G1)**, depois da prova ao vivo com número de teste. **Fase C**: G6 (grupos) e ampliar filas. (G2, listar filas, já existe.)
4. **Fase D**: G5 só com ADR e decisão do dono.
5. Desligar o Chatwoot por fila, nunca de uma vez. Desfazer: suspender a aplicação na tela, `OMNIRA_HEADLESS_WRITE_ENABLED=false`, revogar.

Pré-requisitos (`HEADLESS-P2-K3G.md` §4, corrigido): lease, destinatário do token e criação da aplicação na K3G_Solutions. **Receptor e assinatura de webhook não se aplicam** (o CRM consulta). Orçamento de chamadas: o laço de eventos gasta ~30 req/min; a taxa de 300/min da fase A sobra, e na fase B vale 600/min.

## 7. Decisões do dono (2026-10-17) e o que decorre delas

1. **Adaptador no CRM: aprovado.**
2. **Anexos entram já na fase de escrita:** G1 implementado (ver §4-G1); falta só a prova ao vivo com número de teste.
3. **Alertas do Gateway por WhatsApp (G5):** o dono não soube decidir. Explicação e recomendação abaixo.
4. **Nota interna fica no CRM:** G3 encerrado.
5. **Setores reais, ainda não preparados no OMNIRA:** ver lista abaixo.
6. **Correção de premissa:** **não há webhook**. O CRM consulta o diário (`GET /events`); §2 e `HEADLESS-P2-K3G.md` foram corrigidos.

### 7.1 Gateway (G5): o que é e o que recomendo
Hoje o CRM, quando o Gateway de monitoramento dispara um alerta, **abre uma conversa nova com um número de telefone** no Chatwoot (acha ou cria o contato, liga ao inbox, cria a conversa e manda a mensagem). É um envio **iniciado pela empresa**, sem o cliente ter escrito antes.

No OMNIRA isso é mais delicado: fora da janela de 24 h do WhatsApp oficial só vai **template aprovado pela Meta**, e é preciso respeitar o consentimento do destinatário. Hoje a API externa só responde a conversas que já existem; não cria contato nem abre conversa. Fazer isso bem é um ADR e escopos novos (`contact.write`, `conversation.open`), com teste de duas empresas.

**Recomendação: manter os alertas do Gateway no Chatwoot por enquanto** e tratá-los numa fase própria (D), depois de a escrita estar provada. Motivos: (a) é a parte mais arriscada (envio ativo a terceiros, template, consentimento); (b) não bloqueia a troca do atendimento, que é o grosso; (c) o Chatwoot continua existindo só para esse inbox, sem atendimento humano nele. Para isso funcionar sem conflito, **uma pergunta ao CRM**: o inbox dos alertas do Gateway usa o **mesmo número de WhatsApp** do atendimento? Se for o mesmo número, ele não pode ficar ligado ao Chatwoot e ao OMNIRA ao mesmo tempo (uma sessão só); então o alerta teria de migrar junto, e G5 sobe de prioridade. Se for outro número, a recomendação vale como está.

### 7.2 Setores da K3G_Solutions: o que falta preparar (só leitura feita hoje)
Filas existentes na K3G_Solutions: Comercial, Default, Financeiro, Suporte, Pilot Round Robin.

| Label do CRM | Setor CRM | Fila no OMNIRA |
|---|---|---|
| `financeiro` | FINANCEIRO | **existe** (Financeiro) |
| `suporte_ti` | SUPORTE_TI | **a confirmar**: a fila "Suporte" é a mesma coisa? Se sim, vira o alvo; senão criar "Suporte TI" |
| `provedor_telecom` | PROVEDORES_TELECOM | **criar** ("Provedores Telecom") |
| `servidores` | SERVIDORES | **criar** ("Servidores") |

Cada fila nova precisa de: nome, **horário** (Setores e horários), **membros** (quem atende, mesmo que o CRM seja o titular, para o caso de retomada por supervisor) e a aplicação do CRM com a fila na lista permitida. Eu não gravo no banco de produção sem ordem sua com backup e auditoria; o caminho normal é você criar pela tela **Setores e horários** (ou me mande a ordem e eu faço). Depois o CRM lê os UUIDs em `GET /external/queues`.
