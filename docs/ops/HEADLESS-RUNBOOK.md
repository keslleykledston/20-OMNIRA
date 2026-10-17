# Runbook — API headless para CRM externo (ADR-0048)

Estado: **LAB**. Nada disto vai para produção sem o gate formal e o aceite do dono. Este documento descreve como operar, não autoriza deploy.
Quem lê: o responsável pela plataforma (liga/desliga) e o administrador da empresa (instala aplicações).

## 1. O que existe

| Peça | O que faz | Liga com |
|---|---|---|
| API externa de leitura e eventos | `GET /api/v1/external/{me,conversations,messages,contacts,media,sync/checkpoint,events}` | `OMNIRA_HEADLESS_ENABLED=true` |
| Webhooks assinados | O worker entrega cada fato por HTTPS, com repetição por 72 h e fila de erros | `OMNIRA_HEADLESS_ENABLED=true` (no worker) |
| API externa de escrita | `POST …/conversations/{id}/{claim,release,transfer,messages,finalize,heartbeat}` | `OMNIRA_HEADLESS_WRITE_ENABLED=true` (exige a anterior) |
| Administração | Configurações → Integrações → Aplicações externas (instalar, escopos, filas/linhas, tokens, endpoints, entregas, fila de erros) | junto com a primeira flag; permissão `integration.read` / `integration.manage` |

Com as flags desligadas (padrão) as rotas **não existem** (404) e o worker não roda sequenciador/entrega/lease. **O banco, porém, já tem as mudanças das migrations 137–141**: o gatilho-guarda de `conversations` (deriva o controle tipado de toda atribuição legada e incrementa `control_version`) e os produtores do journal (que, sem nenhuma instalação viva, só fazem uma consulta de existência e retornam). Ou seja: a *funcionalidade* fica inerte, mas a plataforma **não** é byte a byte a de antes — há um custo pequeno por escrita e a coluna `control_version` passa a existir/mudar. Medir no ambiente alvo antes de aplicar as migrations (o benchmark mostra a ordem de grandeza).
Variáveis: `OMNIRA_HEADLESS_ENABLED`, `OMNIRA_HEADLESS_WRITE_ENABLED`, `OMNIRA_HEADLESS_EVENT_RETENTION_DAYS` (30), `OMNIRA_HEADLESS_WEBHOOK_RETRY_HOURS` (72). A chave `OMNIRA_CREDENTIALS_KEY` precisa estar válida (sela os segredos de webhook).

Modelo de segurança em uma linha: o **tenant vem sempre da credencial** (nunca do pedido); uma aplicação não é usuário, nem membro, nem agente do Hub; cada escrita reconfere a instalação **dentro da transação** (revogar/suspender espera a escrita em andamento ou a recusa); o Postgres (RLS) continua sendo a segunda barreira.

## 2. Antes de ligar (checklist)

1. Backup recente do banco e `vibe`/restore testado (seção 7).
2. Migrations **137 a 141** aplicadas pelo serviço `migrate` (`tools/migrate-sql.sh`). A 138 reescreve o controle de `conversations` (uma coluna nova + gatilho): em produção com ~170 conversas leva segundos; em tabelas muito maiores faça fora do horário (o backfill e o índice não são feitos em lotes).
3. `OMNIRA_CREDENTIALS_KEY` presente nos serviços api e worker.
4. Nginx do host: `/api/v1/external/*` passa pelo `location /api/` (já existe); `/internal/` continua `return 404` (não expor). O corpo é JSON pequeno; o cabeçalho `Authorization` deve ser repassado (é por padrão). Nada de cache nessas rotas.
5. Revisão das flags: começar com **somente** `OMNIRA_HEADLESS_ENABLED=true` (leitura + eventos + administração). Escrita é um segundo passo, com a aplicação já lendo corretamente.
6. Dashboards/alertas da seção 4 criados.

## 3. Piloto: uma empresa, um CRM, uma linha

1. Administrador da empresa → Configurações → Integrações → Aplicações externas → **Nova aplicação**. Escopos de **leitura** primeiro (`conversation.read`, `message.read`, `contact.read`, `events.read`). Filtre pelas filas e pela linha do piloto. O token aparece **uma vez**: entregue ao integrador por canal seguro (não por chat/e-mail).
2. O integrador roda o simulador contra o ambiente: `OMNIRA_EXTERNAL_URL=https://…/api/v1/external OMNIRA_EXTERNAL_TOKEN=… crm-sim bootstrap` (`apps/crm-sim`). Deve listar só as conversas permitidas.
3. Criar o endpoint de eventos (https, porta 443/8443, endereço público). O segredo de assinatura aparece uma vez. Conferir no simulador (`crm-sim listen`) que a assinatura confere e que a repetição não duplica.
4. Só então habilitar a escrita: `OMNIRA_HEADLESS_WRITE_ENABLED=true` (reiniciar api) e emitir um **novo token** com `conversation.claim`, `message.send` (e `transfer`/`finalize` se o piloto precisar). Revogar o token de leitura antigo.
5. Definir, se desejado, o **lease** da instalação (`lease_seconds`, 60 a 86400): se a aplicação ficar muda esse tempo + 120 s, a conversa volta para a fila (`lease_expired`). Sem lease, a aplicação segura até devolver.
6. Acompanhar por 24 h com um supervisor presente. Toda conversa assumida pelo CRM aparece no Inbox como «CRM», sem campo de resposta; o supervisor com `conversation.takeover_external` pode retomar (motivo + confirmação).

### Canário e critérios de abortar
Piloto → um segundo CRM só depois de 7 dias limpos. **Abortar** (seção 7) se qualquer um: (a) `omnira_external_webhook_dead` crescendo sem que o CRM esteja fora do ar; (b) `omnira_domain_events_oldest_unsequenced_seconds` > 60 sustentado; (c) qualquer conversa com dono duplo ou mensagem enfileirada depois da perda do controle; (d) erro 5xx > 1 % das chamadas externas por 10 min; (e) aumento de bloqueios/deadlock no Postgres atribuível às tabelas `conversations`, `domain_events`, `webhook_*`.

## 4. Observabilidade

Endpoint de métricas do api/worker (`/internal/metrics`, nunca público). Séries:

| Série | Significado | Alerta |
|---|---|---|
| `omnira_external_requests_total{outcome}` | chamadas externas por resultado (`served`, `unauthenticated`, `rate_limited`, `auth_throttled`, `forbidden_scope`, `not_found`, `conflict`, `precondition_failed`, `unprocessable`, `unavailable`, `error`) | `error`/`unavailable` > 1 %; `auth_throttled` > 0 sustentado (token errado em loop) |
| `omnira_domain_events_unsequenced` / `…_oldest_unsequenced_seconds` | fatos sem número / idade do mais velho (transação longa segura o horizonte) | > 60 s |
| `omnira_external_webhook_pending` / `…_dead` / `…_oldest_due_seconds` | fila de entregas, fila de erros, atraso | `dead` > 0; `oldest_due` > 120 s |
| `omnira_external_webhook_attempts_total{outcome}` | tentativas por resultado (`delivered`, `retry`, `dead`, `timeout`, `connect`, `tls`, `blocked_address`, `http_status`, `redirect`, `not_visible`, `revoked`) | `blocked_address` > 0 (endereço mudou para rede privada) |
| `omnira_external_lease_expired_total{outcome}` | conversas devolvidas por lease | `freed` alto = CRM instável |

Logs: cada pedido externo carrega `X-Request-ID` (aceita o do cliente) e a auditoria correlaciona (`audit_events.correlation_id`); a ação é `external_app.command` com `actor_kind=crm_application` (o ator **não** é um usuário). Nenhum token, segredo ou texto de mensagem vai para log ou auditoria.
Tracing/OpenTelemetry: os spans de HTTP/DB existentes cobrem as rotas novas; não há instrumentação própria adicional além das séries acima.

## 5. Rotinas

- **Girar token de acesso**: gerar novo na tela (convive com o antigo), trocar no CRM, revogar o antigo. Validade padrão 90 dias.
- **Girar segredo de webhook**: «Trocar segredo» → o novo aparece uma vez; durante a janela (24 h por padrão) cada chamada leva **duas assinaturas** (`v1=novo,v1=antigo`) e o CRM aceita qualquer uma. Depois só o novo.
- **Suspender**: a aplicação perde o acesso na hora e **devolve** as conversas que segurava (voltam ao roteador). Reativar é possível.
- **Revogar**: definitivo; tokens deixam de valer, endpoints param, entregas pendentes são canceladas, conversas voltam. Uma escrita já iniciada termina antes; nenhuma depois.
- **Fila de erros**: «Entregas» → filtro «Com falha» → **Reenviar** (nova janela de 72 h). Fato expurgado vira `expired` e não volta.
- **Retenção**: o journal guarda 30 dias (`OMNIRA_HEADLESS_EVENT_RETENTION_DAYS`); o worker apaga de hora em hora. Cursor mais antigo ⇒ `410 resync_required` e o CRM refaz do snapshot. Recibos de comando: 7 dias.

## 6. Problemas comuns

| Sintoma | Causa provável | O que fazer |
|---|---|---|
| CRM recebe **404** em conversa que existia | saiu da visão (fila fora do filtro, contato virou interno/spam) | normal; reler a lista |
| **409 held_by_other / not_holder** | outro (Workspace, Hub, roteador) segura; ou o CRM não segura | reler (`GET …/{id}`), não repetir cego |
| **412 stale_control_version** | a conversa mudou (transferência, takeover, lease) | o `ETag` da resposta traz a versão atual; reler e decidir |
| **428** | faltou `If-Match: "control-N"` | enviar a versão lida |
| **422 window_closed** | passaram 24 h do último contato (linha oficial) | enviar template aprovado |
| **409 outside_hours** | fila fechada e a instalação não tem «fora do horário» | permitir na instalação ou esperar |
| **410 resync_required** | cursor mais velho que a retenção | `GET /sync/checkpoint` e refazer o snapshot |
| webhook «bloqueado» (`blocked_address`) | o nome passou a apontar para rede privada/metadados | corrigir o DNS do CRM; o OMNIRA recusa por segurança |
| 429 | limite por minuto da instalação ou throttle de token inválido | aguardar `Retry-After`; ajustar `rate_limit_per_min` |

## 7. Desligar, reverter, backup

**Desligar sem perder dados** (ordem): 1) `OMNIRA_HEADLESS_WRITE_ENABLED=false`; 2) suspender as aplicações na tela (devolve as conversas); 3) `OMNIRA_HEADLESS_ENABLED=false` (rotas somem; o worker para o journal; as tabelas ficam). Reativar é o inverso.
**Reverter migrations** (só com backup e aprovação do dono; **perde dados** das tabelas novas): `down` de 141 → 140 → 139 → 138 → 137, nessa ordem. A 137 recusa se houver instalação viva (revogue antes). A 141 apaga fechamentos feitos por aplicação externa. O roundtrip foi provado em Postgres descartável (`scripts/test-migration-roundtrip.sh`).
**Backup/restore**: tabelas novas — `external_app_installations`, `external_app_credentials`, `external_app_installation_queues/channels`, `conversation_control_events`, `domain_events`, `domain_event_cursors`, `webhook_subscriptions`, `webhook_deliveries`, `external_command_receipts`, `external_conversation_leases`, e as colunas `control_*` de `conversations` e `messages.sent_by_installation_id`. Segredos de webhook estão **selados** com `OMNIRA_CREDENTIALS_KEY`: restaurar sem a mesma chave torna os segredos ilegíveis (gerar novos). Tokens de acesso só existem como digest: nunca são recuperáveis.

## 8. Limites conhecidos (declarados, não escondidos)

- **NOT WIRED**: anexos/mídia de **saída** pelo CRM (só texto e template); identidade humana verificada do operador do CRM (`crm_agent_verified` existe no vocabulário e não é produzido; `external_agent_id` é só rótulo não verificado); mutação de chamado ERP; alta disponibilidade/distribuído.
- **Não exercido com canal real**: nenhum teste enviou mensagem por WhatsApp real. A entrega até o worker de canal é provada por job enfileirado; o envio ao provedor segue o caminho já em produção do Workspace. **REAL-PROVIDER BLOCKED** até haver número de teste autorizado.
- **Semântica do que já está em voo**: ao **revogar/suspender** uma aplicação, uma entrega de webhook que o worker já iniciou pode concluir (a chamada HTTP não é abortada no meio); nada **novo** é iniciado depois que a revogação é confirmada. As mensagens que a aplicação enfileirou e o worker de canal ainda não enviou viram `failed/control_lost` quando o controle é tirado dela contra a vontade (takeover do supervisor, suspensão/revogação, lease vencido, escopo ou allow-list reduzidos, último token revogado ou expirado); quando é **ela mesma** que devolve, transfere ou finaliza, o que enfileirou antes sai normalmente.
- **Rate limit por instância**: o limite por minuto da instalação e o throttle de token inválido são aplicados por processo da API; com N réplicas o limite efetivo pode chegar a N× (use o limite do nginx para teto global).
- **Permissões da administração**: quem gerencia aplicações (`integration.manage`) precisa também de `conversation.takeover_external` (o `tenant_admin` tem os dois): suspender/revogar libera as conversas que a aplicação segura, e esse caminho exige a segunda permissão; sem ela a operação falha fechada em vez de liberar.
- **Pré-existente, fora desta branch**: o worker de canal mantém o `FOR UPDATE` da mensagem durante a chamada ao provedor (um provedor lento segura a linha); a API externa não muda isso.
- A suspensão de uma **empresa** inteira espera a escrita externa em andamento (trava de linha do tenant); sob tráfego contínuo extremo pode demorar (a revogação de uma instalação usa um portão justo e não sofre disso).
- Estado de produção permitido: LAB / INTERNAL_PILOT. Nada aqui declara PRODUCTION_READY.
