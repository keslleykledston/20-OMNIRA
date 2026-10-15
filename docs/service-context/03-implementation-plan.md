# 03 — Plano de implementação

Ordem: cada fase termina em **gate** executado (testes em Postgres real, `go build ./...`, `go vet`, revisão Codex quando disponível). Nenhum PASS sem rodar. Commits pequenos por fase na branch `feat/omnira-service-context-operations`; **sem push, sem merge, sem tag**.

| Fase | Entrega | Migration | Gate (evidência) |
|---|---|---|---|
| 0 | Auditoria, arquitetura, matriz, plano, ADR-0049 | — | Codex lê os documentos; achados CRITICAL/HIGH corrigidos |
| 1 | `service_sessions`, protocolo, catálogos, estados, encerramento, `EnsureActive`, hook no `Finalize`, rotas REST, permissões, RLS | 000150 | atendimento informativo completo (protocolo → mensagens por intervalo → classificação → encerramento); concorrência de protocolo; idempotência; reabertura; N sessões/conversa; RLS A/B; Hub sem/ com concessão |
| 2 | `context_events` (imutável), timeline paginada, visão `customer-context` | 000151 | ordenação/paginação keyset, isolamento, sem duplicar mensagens, imutabilidade, evento idempotente |
| 3 | `service_cases`, `service_case_sessions`, `service_tasks` | 000152 | os 5 cenários do Gate 3 do pedido; concorrência otimista (`If-Match`); cancelar exige motivo |
| 4 | `external_records`, vínculos, `external_record_requests`, porta `ServiceOrderGateway` + fake, contratos OpenAPI/AsyncAPI, envelope de eventos | 000153 | timeout→`outcome_unknown` bloqueia retry; duplicação; evento fora de ordem (versão); reconciliação; revogação de acesso |
| 5 | Delegação do Hub, UI do Workspace (painel + Hub) | 000154 | permissão negativa para delegado (sem concessão, só leitura, revogada, contrato inativo, outra instância); vitest; build web |
| 6 | Nós de fluxo, ferramentas de IA, métricas | — | execução dos nós sob RLS e idempotência de efeito; IA só `pending_approval`; sem PII em métricas |
| 7 | Docs finais, evidências, deploy/rollback, relatório | — | `13-final-report.md` com status honesto |

## Estratégia de testes

- Banco descartável `omnira_test_svc` criado por script de scratchpad (copia `tn-mkdb.sh`), **nunca** `omnira_dev`. Go roda em `golang:1.25` via docker com `OMNIRA_INTEGRATION_TEST=1`.
- Suites por pacote: unit (domínio: máquina de estados, formato de protocolo), integração (Postgres real, via middleware de tenant), concorrência (`-race`, N goroutines abrindo sessões), RLS (A/B, Hub), regressão (`internal/attendance`, `internal/inbox`, `internal/flows`, `internal/platform/db`).
- Mutação: para cada defesa crítica (trava de conversa, contador, savepoint, `If-Match`, `tenant_id` no SQL) um teste que **falha** quando a defesa é removida (verificado removendo-a).

## Estratégia de migration

*Expand* apenas (tabelas novas). *Migrate*: nenhum dado histórico é convertido; as 180 conversas fechadas ficam sem sessão (não se inventam protocolos). *Contract*: nada é removido. Deploy: aplicar migrations (idempotente, `tools/migrate-sql.sh up`) **antes** de subir o binário; flags desligadas; ligar por tenant/ambiente depois do gate de staging.

## Decisões que dependem do dono (não bloqueiam a implementação)

1. ~~Prefixo e número inicial do protocolo~~ **Decidido (2026-10-15):** prefixo por empresa (3 letras do nome, sem dígitos, editável até o 1º protocolo), início em 1001.
2. ~~Janela de reabertura~~ **Decidido:** 24 h.
3. Quais papéis recebem `service.request_external` (padrão: admin e supervisor).
4. ~~Abertura automática ao assumir conversa~~ **Aprovado:** implementado atrás de `OMNIRA_SERVICE_AUTO_OPEN_ON_CLAIM_ENABLED` (ver 14).

## Dependências externas registradas

- Entrega de webhooks a terceiros e autenticação por aplicação externa: **branch headless** (precisa renumerar migrations e estender CHECKs de tipos/escopos).
- Adaptador real de OS do ERP: depende de contrato do provedor.
