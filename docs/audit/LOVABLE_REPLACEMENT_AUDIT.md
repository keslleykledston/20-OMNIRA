# LOVABLE_REPLACEMENT_AUDIT

Auditoria de `feat/multitenant-service-hub` (HEAD `8d3bf83`) contra o OMNIRA saudável.

| Termo | Valor |
|---|---|
| BASELINE_OMNIRA | `6d36f67` (merge-base com `main`) |
| main atual | `94250f4` (só adiciona 2 fixes Gemini sobre o baseline) |
| CURRENT_HEAD auditado | `8d3bf83`, preservado em `backup/lovable-rebuild-attempt` |
| Branch corretiva | `fix/integrate-lovable-into-omnira` (base `main`) |
| Commits auditados | 41 (`git log main..8d3bf83`) |

## Conclusão

Não houve reconstrução do frontend dentro do repositório. Em `main..HEAD`:

- `web/` recebeu 1 arquivo novo: `web/src/components/hub/HubWorkspace.tsx`, órfão (sem rota, sem dados, não importado em `App.tsx`).
- Nenhum arquivo foi deletado.
- Roteamento, autenticação, estado, API client, design system e build do OMNIRA não foram tocados.

O desvio foi de outra natureza:

1. Foi tratado o Hub como site separado fora do repo: redirect nginx para `customer-space-central.lovable.app` e escrita em `/var/www/omnira-frontend`. Ambos desfeitos ou fora do caminho de produção.
2. O backend do Hub foi declarado "PRODUCTION READY" sem evidência (detalhes abaixo).

Fora do repo:

- Redirect nginx: **removido**. `/etc/nginx/sites-available/omnira-hub*.conf` ficaram como arquivos soltos, não habilitados.
- `/var/www/omnira-frontend`: a produção real é o container `omnira-web` (porta 3000); esse diretório nunca foi servido por ela.
- Projeto Lovable "OmniFlow Hub" (`07c08567-…`): foi publicado em `customer-space-central.lovable.app` com dados de mock. **Pendente: decisão do dono sobre despublicar ou tornar privado.**

## Tabela de auditoria

| Arquivo / grupo | Tipo | Mantém arquitetura OMNIRA? | Preservar? | Reaplicar? | Descartar? | Motivo |
|---|---|---|---|---|---|---|
| `migrations/000093_service_hubs.*` | schema Hub | Sim | Sim | **Sim, reescrita** | Não | Aplica no migrador de produção, `down` volta ao schema exato pré-Hub, reaplica idêntico (`scripts/test-hub-migrations.sh`). FKs compostas fecham grant→contrato de outro hub/tenant. Não aplicada em nenhum banco (live em 092). |
| `migrations/000094_hub_rls_policies.*` | RLS Hub | Sim (ENABLE+FORCE em 9 tabelas) | Sim | **Sim, reescrita** | Não | **A versão original quebrava**: `infinite recursion detected in policy for relation "hub_memberships"` (reproduzido como `omnira_app`). Reescrita com funções `SECURITY DEFINER`, provada por teste de RLS real e por 12 mutações. |
| `migrations/000095_hub_seed_test_data.*` | seed de teste | **Não**: inseria hub de teste com UUID fixo em todo ambiente que rodasse `migrate` | Só no backup | Não | **Sim** | Rodaria em produção; o comentário citava uma flag `.env` inexistente. O número 095 passou a ser a migration `hub_delegated_read` (nenhum ref local nem banco aplicou o 095 antigo). |
| `migrations/000096_integration_gateway.*` | schema gateway | Não | Só no backup | Não | **Sim** | `integration_instances` e `external_action_receipts` sem RLS/FORCE (reprova `TestRLSCompleteness`); duplica `ticket_external_*`, `channel_webhook_events`, `channel_credentials`. Ver `docs/architecture/INTEGRATION_CONVERGENCE.md`. |
| `internal/hub/{domain,ports,adapters/postgres.go}` | backend Hub | Sim | Sim | **Sim, reaplicado** | Não | Compila e passa em `go vet`. Sem teste real de banco. |
| `internal/integrations/**` | gateway + adapters | Não | Só no backup | Não | **Sim** | Zero consumidores; tabelas inexistentes na cadeia; dedupe de webhook com `SELECT COUNT` + `INSERT` (corrida) vs `ON CONFLICT` existente. |
| `internal/tenancy/domain/context.go` | TenantContext | Sim (aditivo: 4 campos novos e 1 factory) | Sim | **Sim, reaplicado** | Não | Compatível com os ~156 arquivos que importam o pacote. |
| `internal/tenancy/domain/context_test.go` | teste unitário | Sim | Sim | **Sim, com correção** | Não | Tinha `WithTenantContext(nil,…)` que dava panic. Corrigido; passa. |
| `internal/ai/tools/authorization.go` | interface stub | Não | Só no backup | Não | **Sim** | Duplica `internal/tool` (ports/application/execution/connectors). |
| `internal/hub/adapters/postgres_test.go` | "teste de RLS" | n/a | Só no backup | **Substituído** | Sim | Não compilava e todos os `t.Run` eram `TODO`. Substituído por `rls_integration_test.go` (asserções reais como `omnira_app`). |
| `internal/e2e/multitenant_hub_test.go` | "e2e Hub" | n/a | Só no backup | **Substituído** | Sim | Não compilava. Cobertura equivalente agora em `http_integration_test.go` (HTTP VERIFIED); E2E real de canal **não** existe. |
| `.agent/multitenant-service-hub/{PRD,ADRS-ESSENTIAL,PHASE-0-AUDIT,acceptance,mission}` | especificação | Sim | Sim | **Sim** | Não | Insumo de produto. `mission.yaml` marca fases 3–13 `pending`, o que contradiz os docs "COMPLETE". |
| `.agent/…/{MISSION-COMPLETE,FINAL-REPORT,DELIVERY-COMPLETE,PHASE-13/14/15-*,DEPLOYMENT-READY}.md` | relatórios | Não | Só no backup | Não | **Sim** | Declaram gate de segurança passado, "zero P0", E2E PASS e "PRODUCTION READY" sem execução. |
| `docs/HUB-*.md`, `docs/E2E-TEST-FLOW.md`, `docs/ROLLOUT-MOBILE-ANALYTICS.md`, `docs/PRODUCTION-STATUS-REPORT.md`, `docs/DEPLOYMENT-READY.md`, `docs/phase-*.md`, `EXECUTION-SUMMARY.md` | docs | Não | Só no backup | Não | **Sim** | Descrevem rotas `/hub-workspace/*`, `/admin/hubs/*`, SLA e fluxos que **não existem no código**. |
| `docs/hub-workspace-ux-spec.md`, `docs/phase-9-lovable-handoff.md` | spec UX | Parcial | Só no backup | Reaproveitar como insumo | Não | O conteúdo servirá de base para a matriz de integração UX, depois de revisado. |
| `scripts/deploy-production.sh`, `scripts/deploy-hub-day0.sh` | deploy | **Não** (ignoram a receita docker compose; escrevem em `/var/www`) | Só no backup | Não | **Sim** | Contradizem o deploy real. |
| `docs/HUB-NGINX-*.conf` | nginx | Não | Só no backup | Não | **Sim** | Sobrepõem `00-omnira.conf`; geraram conflito de `server_name`. |
| `omnira-api` (binário de 34 MB), `go.mod`, `go.sum` | build | Não | Não | Não | **Sim** | Binário versionado, rebuild de árvore intermediária. `go.mod` adicionou `gorilla/mux` sem uso. Restaurados de `main`. |
| `web/src/components/hub/HubWorkspace.tsx` | UI órfã | **Não**: sem rota, props mockadas, `hub-theme`/tokens inexistentes, escrita à parte do design system | Só no backup | Não | **Sim** | Um terceiro produto. Será substituído por adaptação de componentes existentes. |
| `web/…/AIIntegrationCard*`, `lib/aiIntegration.ts`, `ai_integration_http*.go`, `omnira-v1.yaml` | fix Gemini | Sim | n/a | n/a | n/a | Já em `main` (`8ce866d`, `94250f4`). Não é trabalho do Hub. |

## Estado de verificação das mudanças preservadas (atualizado)

| Verificação | Resultado |
|---|---|
| `go build ./...` | OK |
| Migrations 093..095 no migrador de produção, up/down/up | **POSTGRES VERIFIED** (`scripts/test-hub-migrations.sh`) |
| RLS do Hub como `omnira_app` (matriz, revogação, escopo de fila, integridade) | **POSTGRES VERIFIED** (`internal/hub/adapters/rls_integration_test.go`) |
| Os testes falham quando a policy está errada | **12 mutações detectadas** (`scripts/test-hub-rls-mutations.sh`) |
| Serviço Go × RLS concordam em 15 estados | **POSTGRES VERIFIED** (`authorization_integration_test.go`) |
| Rotas HTTP do Hub (`/hubs/{id}/inbox[/{item}]`) | **HTTP VERIFIED**, atrás de `OMNIRA_HUB_API_ENABLED=false` |
| Gate de integração completo | ver `CORRECTIVE-INTEGRATION-REPORT.md` |
| Projetor da inbox, provisionamento, escrita via Hub, UI | **NOT BUILT / NOT WIRED** |
