# Copiloto de grupo — operação (ADR-0050)

Estado: **implementado em LAB na branch `feat/group-copilot-memory`; nada implantado, nenhuma migration aplicada em banco vivo.** Tudo nasce desligado.

## O que é
Memória por grupo de WhatsApp (itens tipados com evidência, editáveis por pessoas), importação de uma exportação da conversa (raw cifrado por 90 dias) e propostas de ocorrência/tarefa que uma pessoa aprova. A IA lê e propõe; **nunca age e nunca escreve no grupo**.

## Chaves (variáveis de ambiente)
| Variável | Padrão | Efeito |
|---|---|---|
| `OMNIRA_GROUP_MEMORY_ENABLED` | false | registra a API de IA de grupo (api) e liga o worker que mantém a memória |
| `OMNIRA_GROUP_IMPORT_ENABLED` | false | importar arquivo (precisa `OMNIRA_CREDENTIALS_KEY` e antivírus) |
| `OMNIRA_GROUP_PROPOSALS_ENABLED` | false | rascunhar/aprovar ocorrência e tarefa (precisa `OMNIRA_SERVICE_CONTEXT_ENABLED=true`) |
| `OMNIRA_GROUP_AUTO_ANALYZE` | false | o worker lê um grupo sozinho (volume ≥ 25 msgs, ou ≥ 5 msgs paradas há 6 h); desligado = só quando alguém pede |
| `OMNIRA_GROUP_AI_DAILY_CALLS` | 48 | chamadas ao modelo por grupo por dia (trava de custo) |
| `OMNIRA_GROUP_RAW_RETENTION_DAYS` | 90 | prazo do raw cifrado |
| `OMNIRA_GROUP_IMPORT_TZ` | America/Manaus | fuso em que o aparelho exportou a conversa |
| `OMNIRA_GROUP_IMPORT_ALLOW_UNSCANNED` | false | aceita import sem antivírus (não recomendado) |
| `OMNIRA_AI_MODEL_GROUP_MEMORY` / `_GROUP_CONSOLIDATE` / `_GROUP_PROPOSAL` | = `OMNIRA_AI_MODEL` | modelo de cada tarefa |

Além dessas: `OMNIRA_AI_ENABLED=true` com provedor, modelo e chave (a IA da plataforma). **O IA de cada grupo é um segundo opt-in** (tela do grupo ou `PATCH .../groups/{id}/ai`), separado de coletar o grupo.

## Ordem de ativação (quando o dono mandar)
1. Backup: `scripts/backup-omnira-db.sh`.
2. Migrations `000169`–`000171` (reconferir a numeração contra as branches): `docker compose up migrate`.
3. Rebuild/recreate **api, worker e web** (o worker sobe junto com a api; a regra do nginx do `web` vai dentro da imagem).
4. Liberar `client_max_body_size 52m` para `^/api/v1/tenants/<uuid>/groups/<uuid>/ai/imports$` no nginx do host (e do destino, após a migração de host).
5. Ligar `OMNIRA_GROUP_MEMORY_ENABLED=true` (depois `_IMPORT_`, depois `_PROPOSALS_`), recriar api+worker.
6. Num grupo de teste: habilitar a coleta, ligar a IA do grupo, "Analisar agora", conferir itens e evidências.
Smoke público depois de qualquer deploy: `POST /webhooks/v1/whatsapp/meta` deve dar 401, nunca 502.

## Conferir
```sql
-- grupos com IA ligada e onde a leitura parou
SELECT g.name, st.last_run_at, st.last_run_ok, st.runs, st.budget_calls, st.analyze_requested_at
FROM wa_groups g LEFT JOIN wa_group_memory_state st ON st.tenant_id=g.tenant_id AND st.group_id=g.id WHERE g.ai_enabled;
-- imports, cobertura e prazo do raw (metadados; o conteúdo nunca é lido por SQL de rotina)
SELECT status, msgs_total, msgs_parsed, batches_ok, batches_failed, expires_at, discard_reason FROM wa_group_imports ORDER BY created_at DESC;
-- uso de IA do copiloto
SELECT task, count(*), sum(input_tokens), sum(output_tokens), count(*) FILTER (WHERE NOT success) FROM ai_usage WHERE task LIKE 'group_%' GROUP BY task;
```

## Situações
- **Import parado em `extracting`:** se o worker caiu, volta sozinho para leitura após 30 min. Se ficar em `uploaded`, o modelo está indisponível (ele tenta de novo a cada minuto) ou a IA do grupo foi desligada.
- **`last_run_ok = false`:** provedor falhou ou respondeu algo inválido; o cursor não avança, a próxima tentativa lê as mesmas mensagens. Ver `ai_usage.reason`.
- **Limite diário atingido:** o worker para até o dia seguinte; "Analisar agora" fica registrado e é atendido depois.
- **Lote com resposta inválida:** o cursor não avança e a falha é contada; na 3ª vez seguida o lote é pulado e a execução fica `last_run_ok=false`.
- **Descartar raw antes dos 90 dias:** só com cobertura completa; senão espera o prazo. O sweep roda a cada minuto, **mesmo com `OMNIRA_GROUP_MEMORY_ENABLED=false`** e para empresa suspensa.
- **Apagar:** "apagar histórico" (ADR-0015) tira citações e raw e mantém o conhecimento; "apagar memória" (chave `group.memory.erase`) apaga tudo do grupo. Os eventos (sem conteúdo) ficam como trilha.

## Reverter
Desligar `OMNIRA_GROUP_MEMORY_ENABLED` e recriar api+worker: a API some, o worker para, nada mais lê o grupo. Os dados ficam. Para remover tudo: migrations `down` 171→169 (apagam as tabelas) — só com backup e ordem do dono.

## Testes
- Unidade (domínio: parser de exportação, extração, consolidação, descarte): `go test ./internal/groupai/domain/`.
- Banco real e RLS (25 testes: Tenant A/B, membership revogada, tabelas append-only, import, consolidação, propostas, HTTP): `scripts/test-integration.sh ./internal/groupai/adapters`.
- Contrato e superfície anônima: `go test ./internal/groupai/adapters/ -run TestOpenAPIAndRoutesAgree` e `./internal/platform/httpserver -run TestGroupAIRoutes`.
- RLS de todas as tabelas: `scripts/test-integration.sh ./internal/platform/db`.
- A suíte de integração completa roda **sem argumentos** (`scripts/test-integration.sh`); nunca rodar `scripts/e2e-inbox.sh` (usa o NATS vivo).
