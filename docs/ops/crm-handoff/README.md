# OMNIRA como motor do CRM: pacote para a equipe do CRM

Versão do pacote: 2026-10-18. Contrato: `omnira-external-v1.yaml` (OpenAPI 3.0, é a fonte da verdade; este guia explica como usá-lo).

## 1. O que muda para o CRM

O CRM deixa de falar com o Chatwoot e passa a falar com a **API externa do OMNIRA**. A troca é uma camada nova (`OmniraProvider`) por trás da fachada `/api/support/chatwoot/*` que o CRM já tem; o front do CRM não muda. O OMNIRA **não imita** o Chatwoot: o contrato é o dele (cursor, `If-Match`, `Idempotency-Key`, posse da conversa).

Duas ideias resolvem quase tudo:

1. **Quem atende é a aplicação.** A conversa tem um titular tipado. Quando o CRM faz `claim`, o titular passa a ser a *aplicação CRM*; enquanto isso o Workspace do OMNIRA e o bot não falam por cima. O atendente humano do CRM viaja só como rótulo (`external_agent_id`, não verificado, usado em auditoria).
2. **O CRM consulta, o OMNIRA não empurra.** Para esta integração não há webhook, receptor, URL pública nem assinatura no CRM: ele lê o **diário de fatos** (`GET /external/events?after=<seq>`) a cada 2 a 3 segundos. (O OMNIRA sabe entregar webhooks assinados a quem pedir; ficam desligados aqui, e o simulador só traz o comando `listen` para quem quiser experimentar.)

## 2. Endereço e credencial

| | |
|---|---|
| Base | `https://omnira.devops.k3gsolutions.com.br/api/v1/external` |
| Autenticação | `Authorization: Bearer omn_ext_<id>.<segredo>` |
| Quem cria a credencial | o administrador da empresa no OMNIRA (Configurações > Integrações > Aplicações externas). O segredo aparece **uma vez**; só um resumo fica guardado. Entregue ao CRM por canal seguro; nunca em chat, e-mail aberto ou navegador |
| Empresa | é a da credencial. Mandar `tenant_id`, `X-Tenant-ID` ou `X-Omnira-Acting-As` dá `400 tenant_not_accepted` |
| Taxa | por credencial, padrão 600 req/min (a fase de leitura usa 300). Excedeu: `429` com `Retry-After` |

`GET /external/me` (sem escopo) diz quem é a credencial, os escopos, o limite, o *lease*, as filas/linhas permitidas e as `features` do servidor. Chame na partida e registre a resposta.

## 3. Escopos que o CRM pede

| Escopo | Para quê |
|---|---|
| `conversation.read` | listar/ler conversas e `GET /queues` |
| `message.read` | listar mensagens |
| `contact.read` | nome e telefone do contato |
| `media.read` | baixar o arquivo que o cliente mandou |
| `events.read` | checkpoint e diário de fatos |
| `conversation.claim` | assumir, devolver e `heartbeat` |
| `message.send` | enviar texto, template e anexo (e subir o arquivo) |
| `conversation.transfer` | passar para uma fila (setor) |
| `conversation.finalize` | finalizar o atendimento |

`service.read` (protocolos, ocorrências, tarefas, ordens de serviço) só se o CRM quiser esses fatos. Sem o escopo, a chamada dá `403 forbidden_scope`.

## 4. Manter o CRM sincronizado (o laço de consulta)

```
1. GET /sync/checkpoint            -> { seq_head }          (leia ANTES da foto)
2. GET /conversations?status=all   -> a foto atual (pagine até has_more=false)
3. GET /events?after=<seq_head>    -> fatos depois do checkpoint
4. repita o passo 3 a cada 2-3 s, com after = next_cursor da resposta anterior
```

- **Mudanças na foto durante a leitura não se perdem**: o checkpoint vem primeiro, e o passo 3 repete o que for preciso.
- **"Pelo menos uma vez"**: o mesmo `event_id` pode chegar de novo. Ignore repetidos. Em produção, grave o `event_id` e o cursor **junto** do processamento do fato (no banco do CRM), para que um reinício do processo não aplique o mesmo fato duas vezes.
- **O fato traz referências, não conteúdo** (ids, estados). Para ver o texto de uma mensagem nova, busque `GET /conversations/{id}/messages`.
- **`next_cursor` avança mesmo** quando todos os fatos da janela foram filtrados para você. Sempre grave o último.
- **`410 resync_required`**: o `after` é mais antigo do que o OMNIRA guarda (padrão 30 dias) ou é um número que o diário nunca emitiu (maior que o `seq_head`). Refaça do passo 1. Já um `after` ausente, mal formado ou negativo é `400`: é erro de chamada, **não** ressincronize.
- **Atraso**: um fato é numerado em ~0,5 s. Com consulta a cada 2-3 s, a tela do CRM enxerga uma mudança em até uns 3 s. Gasta ~25 requisições por minuto.
- O fato é conferido de novo para você no momento da leitura: uma credencial limitada a uma fila nunca ouve falar de outra.

Tipos de fato de atendimento (exemplos reais, `data` resumido):

| `type` | quando | `data` |
|---|---|---|
| `conversation.created.v1` | chegou conversa nova | `{status:"open"}` |
| `message.created.v1` | mensagem nova (do cliente, sua ou de outro) | `{direction, status, type}` |
| `message.status_changed.v1` | entrega mudou | `{previous, status}` (`queued`→`sent`→`delivered`→`read`, ou `failed`/`uncertain`) |
| `conversation.control_changed.v1` | quem atende mudou | `{control:{kind,state,version}, previous:{kind,state}}`; `aggregate_version` é a versão de controle |
| `conversation.closed.v1` | atendimento finalizado | `{}` |
| `contact.updated.v1`, `ticket.linked.v1` | contato/chamado | referências |

Todo fato tem `origin: {kind, mine}`. `mine=true` quer dizer "causado por esta credencial": use para reconhecer o próprio eco. **Atenção:** em `conversation.closed.v1` causado por você, `mine` vem `false` hoje; reconheça a sua finalização pela resposta do `finalize`.

Aplique os fatos **na ordem do `seq`** (é a ordem do diário). `aggregate_version` só existe nos fatos de controle e de conversa (`control_changed`, `created`, `closed`) e é a versão de controle: use-a para decidir se um fato de controle é mais novo do que o estado que você já tem. Em `message.*` ela vem `null`.

## 5. Escrever (comandos)

Rotas (todas `POST /external/conversations/{id}/...`):

| Rota | Escopo | Corpo | Observação |
|---|---|---|---|
| `claim` | `conversation.claim` | `{external_agent_id?}` | conversa **livre**. `If-Match` opcional. Repetir o claim de quem já segura não muda nada |
| `release` | `conversation.claim` | idem | devolve ao roteador |
| `heartbeat` | `conversation.claim` | idem | renova o *lease* |
| `messages` | `message.send` | `{text}` ou `{template:{id,params}}` ou `{attachment_id, text?}` | `202` = **enfileirada**, não entregue |
| `attachments` | `message.send` | multipart, campo `file` | sobe o arquivo; devolve `attachment_id` |
| `transfer` | `conversation.transfer` | `{queue_id, user_id?}` | a aplicação deixa de ser titular |
| `finalize` | `conversation.finalize` | `{reason?, note?, notify_protocol?}` | fecha a conversa e o protocolo |

**Duas regras de ouro**

1. **`Idempotency-Key` em todo comando** (8-128 caracteres `[A-Za-z0-9._:-]`). Uma chave por *intenção* ("mandar este texto nesta conversa"), guardada junto com a intenção e **reaproveitada em cada nova tentativa**. Mesma chave + mesmo corpo devolve a mesma resposta (`Idempotent-Replay: true`, mesmo `message_id`). Mesma chave com corpo diferente é `422 idempotency_key_reused`.
2. **`If-Match: "control-N"`** com a versão que você leu (`ETag` do `GET /conversations/{id}`, ou `control.version` da última resposta). Faltou: `428`. Velha: `412 stale_control_version` (a recusa traz o `ETag` atual). Nunca repita às cegas um 412/409: **releia a conversa e decida de novo**.

**O que fazer com cada erro**

| Resposta | Significa | Ação |
|---|---|---|
| `401 unauthenticated` | credencial inválida, expirada, suspensa ou revogada (a causa nunca é dita) | pare e avise o operador |
| `403 forbidden_scope` / `queue_not_allowed` / `capability_disabled` | falta escopo; fila fora da lista; anexos desligados na empresa | corrija a configuração; não repita |
| `404` | não existe **ou** não é visível para esta credencial (igual) | trate como inexistente |
| `409 not_holder` | você não segura a conversa | faça `claim` antes (se `can.claim`) |
| `409 held_by_other` | outro titular (pessoa, Hub, outra aplicação) | releia; não tome |
| `409 conversation_closed` | já finalizada | atualize a tela |
| `409 outside_hours` | fora do horário da fila e a credencial não tem permissão | avise o atendente |
| `409 channel_unavailable`, `too_many_attachments`, `attachment_quota` | linha fora do ar; limite de anexos pendentes | tente depois / envie os pendentes |
| `412 stale_control_version` / `428` | versão velha / faltou `If-Match` | releia e refaça com a versão nova |
| `422 window_closed` | passou a janela de 24 h do WhatsApp oficial: só **template** | envie template |
| `422 invalid_message` / `invalid_template` / `invalid_target` / `invalid_caption` / `invalid_request` | corpo ou destino inválido | corrija |
| `422 attachment_unavailable` | anexo inexistente, já enviado, de outra conversa ou de outra credencial | suba de novo |
| `422 file_rejected` / `file_blocked` / `media_unsupported` | arquivo recusado pelo OMNIRA (tipo, estrutura, antivírus) | não repita o mesmo arquivo |
| `503 scanner_unavailable` | o antivírus está fora do ar; **o arquivo não foi aceito** | espere um pouco e suba de novo o mesmo arquivo |
| `413 file_too_large` | acima de 16 MiB | reduza |
| `429` (`rate_limited`, `upload_rate`) | limite de taxa | espere o `Retry-After` |
| `5xx` / falha de rede | transitório | repita com a **mesma** `Idempotency-Key` |

**Lease (titularidade com prazo).** Se o administrador definiu `lease_seconds` para a credencial (ver `/me`), toda conversa que o CRM segura expira quando o CRM fica calado por *lease + 120 s*: o OMNIRA devolve a conversa ao roteador (`reason lease_expired`) e o CRM descobre pela versão (`412`/`409` no próximo comando e o fato `conversation.control_changed.v1`). Enquanto houver atendente com a conversa aberta na tela, mande `heartbeat` (por exemplo a cada `lease/3`). Pessoa do OMNIRA nunca perde a conversa por prazo.

**Supervisor pode retomar.** Um supervisor do OMNIRA com a permissão certa pode retomar uma conversa que o CRM segura (com motivo). O CRM descobre pela versão e pelo fato de controle; trate como `412`/`409`.

## 6. Setores (filas) e etiquetas

`GET /queues` devolve `{id, name}` das filas que a credencial pode usar. O CRM mantém uma tabela de configuração `etiqueta -> queue_id` e usa `transfer {queue_id}`:

| Etiqueta do CRM | Fila no OMNIRA |
|---|---|
| `financeiro` | Financeiro |
| `suporte_ti` | Suporte (ou Suporte TI, a confirmar) |
| `provedor_telecom` | Provedores Telecom |
| `servidores` | Servidores |

Transferir encerra a titularidade do CRM no mesmo passo e entrega a conversa ao roteador do setor (horário, elegibilidade, capacidade). Com `user_id`, a pessoa precisa estar elegível **agora** na fila, senão `422 invalid_target` e nada muda.

## 7. Anexos

Enviar: `POST .../attachments` (multipart, campo `file`, até 16 MiB, **você precisa segurar a conversa**) -> `attachment_id`; depois `POST .../messages {attachment_id, text?}` (o texto vira legenda, até 1024 caracteres). O arquivo passa pelo mesmo caminho do operador: tipo e tamanho pelos bytes reais, metadados removidos, estrutura conferida, antivírus (falha do antivírus = recusa). Limites por credencial: 5 arquivos pendentes por conversa, 64 MiB pendentes, 30 envios por minuto. Um upload não enviado expira em 24 h. Áudio: converta para MP3 como o CRM já faz.

Receber: a mensagem do cliente traz `media {id, kind, mime, size_bytes, status}`. Só `status=clean` baixa: `GET /media/{id}` (`409 media_pending` enquanto confere; `403 media_blocked`; `410 media_gone`). Nunca vem a URL do provedor.

## 8. Finalizar

`finalize` fecha a conversa, fecha os tickets locais (os ligados ao ERP ficam como estão) e **encerra o protocolo do atendimento** (cria um antes, se o atendimento nunca teve). Nas empresas com o **aviso de protocolo** ligado, o cliente recebe o número como mensagem da aplicação. Se o CRM já manda a sua mensagem de encerramento, envie `notify_protocol: false`. Motivos: `resolved`, `no_response`, `duplicate`, `spam`, `transferred`, `other`.

## 9. Mensagens: campos que importam

- `direction` (`inbound`/`outbound`) e `sender.kind`: `contact`, `agent` (pessoa do OMNIRA), `crm_application` (`mine` diz se foi esta credencial), `automation` (bot/fluxo).
- `status`: `received`, `queued`, `sent`, `delivered`, `read`, `failed`, `uncertain`. **`queued` nunca significa entregue.**
- Para reconhecer o eco da própria mensagem, use `sender.kind=crm_application` + `mine=true` (ou `origin.mine` no fato), sem prefixo no texto.
- Não existe `message_type` 0/1/2 do Chatwoot: "atividade" aparece como **fato** (`control_changed`, `closed`).

## 10. O que o OMNIRA ainda NÃO faz (planejado para o CRM)

| Necessidade | Situação |
|---|---|
| Abrir conversa nova por telefone (alertas do Gateway) | **não existe**; continua no Chatwoot até haver ADR e template aprovado |
| Buscar contato por nome/telefone, criar contato | não existe (o `contact_id` vem na conversa) |
| Nota interna (`private`) | fica no CRM |
| Lido/não lido, "digitando" | o CRM calcula pelo diário (inbound depois da última leitura dele) |
| Grupos de WhatsApp | a API **nunca devolve grupo**; só contatos |
| Listar templates aprovados | não há rota; peça os `template.id` ao administrador do OMNIRA |
| Etiquetas livres | não; setor = fila |
| Identidade verificada do atendente do CRM | não; só rótulo (`external_agent_id`) |

## 11. Como testar (sem esperar o adaptador pronto)

1. **Simulador de referência** (`bin/crm-sim-<sistema>`): executa cada chamada deste guia pela linha de comando, inclusive o laço de consulta (`poll`). Comandos: `me`, `queues`, `poll`, `bootstrap`, `list`, `show`, `claim`, `heartbeat`, `send`, `attach`, `media`, `transfer`, `release`, `finalize`. Variáveis: `OMNIRA_EXTERNAL_URL`, `OMNIRA_EXTERNAL_TOKEN`; `SIM_KEY` repete um comando com a mesma chave; `SIM_NOTIFY_PROTOCOL=false` finaliza sem avisar o protocolo.
2. **Cliente TypeScript de exemplo** (`exemplo-typescript/`): `OmniraClient` (comandos com chave e versão, anexo em dois passos, erros tipados) e `JournalFeed` (o laço de consulta com dedupe e 410), com testes. Copie o que servir; não é biblioteca.
3. **`ROTEIRO-DE-TESTE.md`**: 18 passos com resultado esperado e as evidências que devem voltar para nós.

## 12. Por onde começar (ordem sugerida)

1. Rodar `me`, `queues` e `poll` com credencial **só de leitura** durante um dia, comparando com o Chatwoot em modo sombra (sem escrever).
2. Implementar `OmniraProvider` com leitura + o laço de consulta.
3. Ligar a escrita numa fila só (Suporte): `claim`, texto, `transfer`, `finalize`.
4. Anexos, depois da prova no `ROTEIRO-DE-TESTE.md` (passos 10-13).
5. Desligar o Chatwoot por fila, nunca de uma vez.

Desfazer a qualquer momento: o administrador suspende a aplicação na tela (corta a credencial na hora e devolve as conversas ao roteador). O OMNIRA continua atendendo sozinho.

## 13. Mapa e referência

`referencia/MAPEAMENTO-CHATWOOT-OMNIRA.md`: rota a rota do Chatwoot que o CRM usa hoje e o equivalente no OMNIRA, diferenças de modelo e lacunas.
