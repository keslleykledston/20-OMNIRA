# ADR-0052: Listar templates aprovados na API externa (`template.read`)

## Status
**Aceito e implementado (2026-10-20).** Decisão de contrato/segurança (CLAUDE.md: novo scope exige ADR) tomada pelo dono do produto nesta sessão, em resposta à pendência §2.2 de `OMNIRA_API_PENDENCIAS_CRM.md`.

## Contexto
`docs/ops/HEADLESS-CHATWOOT-MAPPING.md` §4-G2/§7 e `docs/ops/crm-handoff/README.md` §83/§158 já registravam: envio de template em mensagem **já funciona** (`messagePayload.Template{ID, Params}`, `internal/messages/application/external_send.go`), mas **listar** templates aprovados não existia na API external — o CRM só conseguia enviar template se o dono passasse o `template.id` (UUID interno) manualmente, workaround aceito como limitação conhecida, não como decisão definitiva.

A listagem já existe, mas só para o Workspace (usuário humano logado, `GET /tenants/{t}/channels/lines/{connection_id}/templates`, sem token de integração, `internal/channels/adapters/templates_http.go`), lendo de `channel_message_templates` (sincronizada do Meta Cloud API por `channel.manage`).

Catálogo de scopes da API external (`internal/external/domain/scope.go`) é fechado — não tem nada de templates.

## Decisão
1. **Novo scope `template.read`**, read-only, mínimo privilégio — não concede `channel.manage` nem permite sincronizar/alterar templates, só ler os já aprovados e sincronizados.
2. **Endpoint `GET /api/v1/external/templates`**: templates com `status = 'APPROVED'` e `sendable = true` de todas as linhas (`channel_connections`) do tenant, **restrito à allowlist de canais da instalação** quando o administrador limitou (mesma regra que já vale para conversas e filas — `Visibility.ChannelIDs`). Não lê templates fora dessas condições (rascunho, rejeitado, pausado, sem suporte).
3. **Campos expostos**: `id` (o mesmo UUID que `message.template_id` espera), `channel_connection_id`, `name`, `language`, `body`, `variable_count`. Nunca o `provider_template_id` (Meta) nem metadados de sincronização.
4. **Sem escrita**: nenhuma aplicação externa sincroniza, cria ou edita template — isso continua exclusivo de `channel.manage` no Workspace.
5. **Tenancy**: tenant_id sempre na query, teste obrigatório Tenant A/B (instalação de uma empresa nunca vê template de outra, mesmo com IDs coincidentes por acaso), allowlist de canal respeitada, credencial revogada perde acesso.

## Consequências
- Ganho: o CRM deixa de depender do dono repassar UUIDs de template manualmente; fecha a lacuna §2.2 da pendência sem tocar no fluxo de envio já existente.
- Custo: mais um scope no catálogo fechado e mais uma tela a manter (checkbox na UI de Aplicações externas); nenhuma migration (reaproveita `channel_message_templates`, já existente).
- Não resolve nem antecipa ADR-0050 (outbound/criar conversa) — é só leitura do catálogo, continua exigindo `conversation.id` existente para enviar.

## Alternativas
- **Reusar `conversation.read` ou `message.send` para liberar a listagem**: misturaria autorização de capacidades não relacionadas (ver conversa vs. ver catálogo de templates); rejeitada por consistência com o resto do catálogo (um scope por capability).
- **Expor também templates não aprovados/pausados com o motivo**: útil para debug do CRM, mas aumenta a superfície sem pedido concreto; adiado — pode entrar depois se o CRM pedir.
