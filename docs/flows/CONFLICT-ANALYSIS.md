# Flow Builder — análise de conflito com as frentes paralelas

Data: 2026-10-06. Base: `main` @ `8af6d54`. Branch: `feat/flow-builder` (worktree `omnira-flow-builder`).
Regra: nada existente muda de comportamento com `OMNIRA_FLOWS_ENABLED=false` (padrão).

## O que está em andamento nas outras frentes (verificado, não suposto)

| Frente | Evidência | Estado |
|---|---|---|
| RBAC / agentes | 7 commits em `main` desde `5ef2903`: `internal/rbac/domain/role.go` (+`ResourceAgent`), `web/src/pages/AgentsPage.tsx`, `SettingsShell.tsx`, `Sidebar.tsx`, `App.tsx`, `TeamPage.tsx`, docs de operação | commitado |
| WhatsApp multi-canal | `240ad4f`/`08902fd` (linhas de canal, filtro por canal, abrir conversa em outra linha, janela 24h Meta), `c5fb246` (Meta Cloud, credenciais por conexão) | já em `main`; é parte da base que integro |
| Correção de build antigo | worktree `20-OMNIRA-buildfix` (`fix/backend-build-regressions`, 2026-09-20, 915 arquivos de diferença) | obsoleta; não contém `password_handler.go` |
| Árvore `main` | `git status` limpo; sem stash; sem outras branches locais ativas | limpa |

## RBAC: sem conflito de modelo
Existem **dois** mecanismos: (a) `internal/rbac` (papéis/recursos em código, doc `docs/RBAC.md` R0.2), usado só pelo próprio pacote e testes; (b) as tabelas `permissions` / `role_permissions` + `memberships`, que **todos os handlers reais** usam (`ticket.*`, `group.*`, `agent.*`, `conversation.*`). O Flow Builder segue (b), igual a `ticket.create`/`group.manage`: nova migration só **insere** as chaves `flow.*`, `flow_template.*`, `flow_run.*` e concede a `tenant_admin`/`tenant_supervisor`. `internal/rbac` não é tocado.
Risco residual: a UI de papéis/equipe (frente RBAC) pode listar chaves de permissão; `web/` só ganha chaves novas, sem alterar as existentes.

## WhatsApp multi-canal: pontos de contato e como evito quebrar
- `conversations.channel_connection_id` já identifica a linha; o flow filtra por `connection_ids`/`providers` em `trigger_filter`. Nenhuma mudança em `channels/`.
- Envio: o `messages.Sender` é só para operador humano. O bot usa `SystemSender` **novo** (arquivos novos) que reaproveita `LoadSendContext`, `SessionWindow` e o job `job.channel.send_text.v1`. O índice de idempotência atual `(tenant, sent_by_user_id, key)` não protege remetente NULL; migration aditiva cria `UNIQUE(tenant_id, idempotency_key) WHERE sent_by_user_id IS NULL`.
- Janela de 24h da Meta é respeitada (`window_closed` vira saída roteável do node, nunca bypass).

## Arquivos existentes que serão tocados (todos de forma aditiva e mínima)
| Arquivo | Mudança | Conflito potencial |
|---|---|---|
| `internal/inbox/application/inbound.go` | opção `WithFlowGate(...)` + 2 chamadas protegidas por `if s.flows != nil` e savepoint; nil = idêntico a hoje | nenhum commit recente toca; baixo |
| `internal/platform/httpserver/server.go` | nova `RegisterFlowHandlers` (função nova + 1 import; nenhum corpo existente editado) | baixo |
| `contracts/openapi/omnira-v1.yaml` | **não alterado**: a API fica em `flows-v1.yaml` com guarda de deriva próprio (o guarda genérico exige registrar tudo no servidor de teste, cujo arquivo hoje não compila) | nenhum |
| `apps/api/cmd/omnira-api/main.go`, `apps/worker/cmd/omnira-worker/main.go` | um bloco novo cada, atrás da flag | baixo |
| `internal/platform/config` | variável `OMNIRA_FLOWS_ENABLED` (+ limites) | baixo |
| `web/src/App.tsx`, `web/src/components/Sidebar.tsx` | 1 rota e 1 item de menu | **médio**: a frente RBAC/agentes editou exatamente esses arquivos (6 e 7 linhas). Mitigação: hunks de poucas linhas, resolvo sobre o `main` mais recente antes do relatório final |
| `docs/adr/README.md`, `CHANGELOG.md`, `docs/delivery/HANDOFF-NEXT-AGENT.md` | linhas de índice | baixo |
Todo o resto é **arquivo novo**: `internal/flows/**`, `internal/messages/{adapters,application}/*system*`, `migrations/000082+`, `web/src/pages/flows/**`.

## Migrations
Novas: `000082` (tabelas), `000083` (permissões), `000084` (`conversations.automation_mode` + índice de idempotência do bot). Só aditivas (`ADD COLUMN ... DEFAULT`, tabelas novas, `INSERT ... ON CONFLICT DO NOTHING`). Hoje nenhuma outra frente tem migration pendente (`main` termina em `000081`). **Antes do merge, reconferir o último número em `main` e renumerar se houver colisão.** Nenhuma migration foi aplicada em banco vivo; testes usam `omnira_test_flows` descartável.

## Quebra preexistente em `main` (não causada por esta feature)
`internal/platform/authn/password_handler.go:16`: import `platformdb` sem uso (vem do IAM5). Impede compilar `authn`, `httpserver`, `api`, `worker` e vários pacotes de teste. Registrada na baseline (`docs/flows/STATUS.md`). Será corrigida em **commit separado e identificado** (remoção do import), para poder ser cherry-pickada ou descartada sem afetar a feature; um conserto idêntico feito pela outra frente mescla sem conflito.

## Salvaguardas operacionais
- Backup antes de começar: `git bundle` (fora do repo) + branch `backup/flow-builder-before-rebuild-*`; branch antiga preservada como `feat/flow-builder-discarded-scaffold`. Dump horário do banco vivo confirmado (não tocado).
- Testes só em `omnira_test_flows`; **não** apontados para o NATS vivo; nenhuma tag, nenhum push, nenhum merge, nenhum deploy.
- Cada fase: build + testes do que mudou + suíte existente relevante contra a baseline, depois commit.
