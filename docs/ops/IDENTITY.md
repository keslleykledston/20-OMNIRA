# Identidade, classificação e empresas — operação (ADR-0018)

Usuários internos × contatos externos × empresas atendidas. O código está pronto; nada disto foi aplicado em produção.

## Flags (variáveis de ambiente do api)

| Variável | Padrão | O que faz |
|---|---|---|
| `OMNIRA_CONTACT_CLASSIFICATION_ENABLED` | **true** | API/UI de classificação e vínculos de empresa (desligada: 404, o `PATCH kind` antigo segue) |
| `OMNIRA_CUSTOMER_ACCOUNTS_ENABLED` | **true** | API de empresas e a projeção `tickets.customer_account_id` |
| `OMNIRA_INTERNAL_CHANNEL_IDENTITY_ENABLED` | **true** | o resolvedor de entrada usa identidades internas **verificadas** (nada casa até alguém verificar uma) |
| `OMNIRA_CONVERSATION_KIND_ENABLED` | **true** | deriva `conversation_kind` na criação da conversa |
| `OMNIRA_AI_AUTO_CLASSIFICATION_ENABLED` | false | uma IA poderia **confirmar** uma classificação sozinha (não implementado: IA só sugere) |
| `OMNIRA_CRM_AUTO_CONTACT_CREATION_ENABLED` | false | criar contato no CRM na primeira mensagem (escrita no provider sem chave de idempotência provada) |
| `OMNIRA_AUTO_CUSTOMER_TICKET_UNCLASSIFIED_ENABLED` | false | reservada: ticket de cliente automático para contato não classificado |

## Como ligar em produção (só com autorização do dono)
1. Backup (`scripts/backup-omnira-db.sh`), depois aplicar as migrations 000073–000078 (cada `down` provada com `scripts/test-migration-roundtrip.sh`; o backfill da 074 com `scripts/test-migration-074-backfill.sh`).
2. Efeito imediato do backfill: contatos `other` que ninguém escolheu viram `unclassified`; `agent` vira `other`; **nenhum** vira cliente. O painel mostra o backlog em "Não classificados".
3. Cadastrar a equipe: `POST /identities` (pendente) → `POST /identities/{id}/verify` (`admin`). Verificar uma identidade que já é de um contato abre um **conflito** (`GET /identity-conflicts`): resolver como `confirmed_internal` ou `identity_revoked`. Só `identity.manage` (administrador) faz isso.

## Garantias a conferir
- Um remetente com identidade **verificada** vira conversa `internal` (sem Contato, sem fila, sem ticket); pendente/revogada/de outro tenant/membership inativa nunca casa.
- Conflito aberto ⇒ a conversa do contato fica `unclassified` até um humano decidir.
- Só `customer_service` recebe automação de cliente (ticket automático do assunto); `internal`, `external_other` e `unclassified` não.
- Responder numa conversa `internal` ainda não é suportado (404 no envio): lacuna P1 documentada.

## Métricas (OpenTelemetry, sem dado pessoal nos atributos)
`sender_resolution_total{outcome=internal|conflict|external}`, `contact_classification_changes_total{from,to,source}`, `identity_conflict_events_total{event=opened|resolved_confirmed_internal|resolved_identity_revoked}`.

## Auditoria
`account.created|updated`, `contact.classified|reclassified|account_linked|account_unlinked|primary_account_changed`, `conversation.kind_changed` (com contagens), `user.identity_created|verified|revoked`, `identity.conflict_found|resolved`, `topic.account_linked|unlinked`.
