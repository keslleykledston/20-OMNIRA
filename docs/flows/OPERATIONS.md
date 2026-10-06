# Flow Builder — operação

## Ativar / desativar
`OMNIRA_FLOWS_ENABLED=true` liga a API de controle (`/api/v1/tenants/{tenant_id}/flows...`). Desligada (padrão) as rotas **não existem** e nada muda no comportamento atual. As migrations `000082–000084` são aditivas e podem ser aplicadas com a flag desligada.

## Como a feature se encaixa no fluxo de mensagens
```
webhook -> InboundService.Ingest (1 transação) -> conversa nova?  -> Gate.Engage: há flow publicado que atende?
                                                                     sim: conversa fica retida (automation_mode=bot), sem RouteNew
                                                                     não: RouteNew como sempre
                                      -> mensagem gravada -> Gate.OnInbound -> outbox job.flow.inbound.v1 (só ids)
publisher -> JetStream (OMNIRA_JOBS, job.>) -> worker-flows -> Engine.OnInbound (1 transação, conversa travada)
Sweeper (15 s): timeouts vencidos | conversa retida sem run > 2 min | runs de conversas fechadas
```
Garantias: o bot só fala se `automation_mode='bot'` **e** a conversa não tem responsável; qualquer fim sem handoff devolve a conversa às filas; falha do gate nunca perde mensagem.

## Permissões (tabela `permissions`/`role_permissions`)
| Chave | tenant_admin | tenant_supervisor | tenant_agent |
|---|---|---|---|
| `flow.view`, `flow_template.view`, `flow_run.view` | sim | sim | não |
| `flow.test` | sim | sim | não |
| `flow.create`, `flow.edit`, `flow.publish`, `flow.archive`, `flow_template.install` | sim | não | não |
Editar e publicar são permissões separadas. Para conceder a outro papel, `INSERT INTO role_permissions` (mesmo padrão de `ticket.*`).

## Ciclo de vida de um flow
`draft` (nunca publicado) → `published` (versão ativa) → `archived`. Publicar cria uma **versão imutável** (trigger no banco bloqueia UPDATE; `omnira_app` só tem SELECT/INSERT). **Rollback** = `POST .../versions/{n}/activate`: só move o ponteiro; runs em andamento ficam na versão com que começaram. Publicar um rascunho sem mudanças reativa a última versão (não cria duplicata). Subflows são fixados (`subflow_pins`) no momento da publicação.

## Backup / restore
Os dados de flow estão no mesmo PostgreSQL (`omnira_dev`); o backup horário existente (`backups/omnira_dev/*.dump`) já os cobre. Restore: seguir `docs/ops/` do projeto, em banco temporário. Definições são JSONB versionado; nenhum segredo é armazenado nelas (o validador rejeita campos com cara de segredo).

## Verificação (Docker; banco descartável)
Ver `docs/flows/STATUS.md` ("Como verificar"). Nunca apontar testes para `omnira_dev` nem para o NATS vivo.

## Sintomas e diagnóstico
| Sintoma | Onde olhar |
|---|---|
| Conversa nova não recebe o bot | flow `published` e `flow_type='INBOUND'`? filtro de linha (`trigger_filter`) bate? contato com conflito de identidade aberto? log `flows: gate engage failed` |
| Conversa sem fila após mensagem | `automation_mode='bot'` sem run: o sweeper libera em ≤ ~2 min; log `flows sweeper: ... released` |
| Mensagem do bot não sai | `outbox_events` `job.channel.send_text.v1` + janela de 24 h da Meta (`window_closed` é saída do node); conversa atribuída = bot em silêncio |
| Run parado | `flow_runs.status` / `wait_until` (`waiting_input`), timeline em `flow_node_executions` |
Logs do runtime não contêm texto de mensagem; trazem `tenant`, `flow`, `conversation` e `run`.

## Rollback da feature
1. `OMNIRA_FLOWS_ENABLED=false` e reiniciar API/worker: a feature para imediatamente.
2. Se necessário reverter schema: `migrations/000084*.down.sql`, `000083*.down.sql`, `000082*.down.sql` (nessa ordem; os `down` foram testados em banco descartável).
