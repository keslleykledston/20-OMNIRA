# 09 — Workspace e Hub (fase 5)

## Delegação do Hub (migration 000154)

O agente do Hub **não é membro** da instância. Ele alcança o módulo só pela cadeia do ADR-0040 (hub → contrato → concessão), com as chaves que a concessão **e** o teto do contrato têm, dentro do escopo de filas do contrato, **uma instância por vez**.

| Item | Regra |
|---|---|
| Domínio de dados | novo domínio `service` em `permission_domains`: `service.read` = leitura; `service.create/update/close/assign/link_external` = escrita |
| `service.request_external` | **fora** de `permission_domains`: nunca delegável, seja qual for o contrato ou a concessão (pedir algo a um ERP é ato da própria empresa) |
| Leitura | políticas `*_read_delegated` por `delegated_tenants('service','read')` |
| Escopo | políticas RESTRICTIVE `*_acting_scope`: instância fixada (`acting_tenant()`) + visibilidade: sessão sem conversa visível, ocorrência sem sessão visível, tarefa/evento sem sessão ou ocorrência visível, registro/vínculo pendurado em nada visível = invisíveis para o delegado. **Tarefa/evento ligado só a um contato não é visível** (ver o contato por uma conversa do escopo não diz nada sobre trabalho feito em outra fila) |
| Pedidos ao ERP | `external_record_requests` fechada ao delegado (leitura e escrita); as rotas de leitura também são só de membro |
| Membro e delegado ao mesmo tempo | o contexto delegado **não** une permissões de membro (o middleware e `actor_has_permission` usam só `delegated_permissions`) |
| Abertura vs finalização | `service_conversation_open_locked` (definer, `KEY SHARE` na conversa): o delegado não tem UPDATE em `conversations` e por isso não pode travar a linha como o membro; a abertura espera uma finalização em curso e é recusada depois dela. Aberturas simultâneas de delegados = uma sessão (savepoint + `ErrAlreadyActive`) |
| Auditoria | `audit.go` grava `via=hub`, `hub_id`, `contract_id`, `grant_id` e o agente como ator |
| Helpers `SECURITY DEFINER` | `service_integration_system` (só o provedor da integração, só para quem tem `service.read` na instância servida) e `service_conversation_open_locked` (só true/false/NULL); `search_path` fixo, `REVOKE ... FROM PUBLIC` |

Matriz atualizada em `docs/architecture/HUB-DELEGATION-TABLE-MATRIX.md` (seção "Service Context").

## Workspace (web)

- `web/src/lib/serviceContext.ts`: cliente da API (`If-Match` com a versão que a tela viu, `Idempotency-Key` por montagem; 404/501 = módulo desligado).
- `web/src/components/service/ServiceSessionPanel.tsx`: seção **"Protocolo e tarefas"** em `ContextPane` (membro) e `DelegatedContextPane` (Hub): protocolo `<PREFIXO>-n`, situação, aguardando/em andamento, encerrar com resultado (a tela diz que **a conversa continua aberta**; "Finalizar atendimento" não mudou), tarefas do atendimento (criar/concluir).
- O painel só oferece o que a chave do usuário permite (`useAccess`); o servidor continua sendo a autoridade (um 403/412/409 vira mensagem simples e recarrega).
- Cache do React Query separado por **membro/delegado e por Hub** (`delegated:<hubId>`), e `me-access` também por Hub.
- Sem flag no web (padrão do projeto): o painel some quando a API responde 404/501.
- Rótulos das 7 permissões em `permissions.ts` (tela de equipe).

Fora desta entrega (não inventado): telas de ocorrências, de OS externas e de linha do tempo no Workspace; a API já as serve (`service-context-v1.yaml`). Um protótipo do Lovable **não** foi usado; nada foi enviado ao Lovable.

## Evidência

`internal/hub/adapters/servicectx_delegated_integration_test.go` (Postgres real, middleware real): escopo de fila, somente leitura, não delegáveis, uma instância por vez, membro+delegado, finalização concorrente, delegados em paralelo, tarefa só de contato. `web/src/__tests__/ServiceSessionPanel.test.tsx` (11 testes) e `ContextPane.test.tsx` (allowlist de leituras).
