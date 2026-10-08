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
| `migrations/000093_service_hubs.*` | schema Hub | Sim (PostgreSQL, tenant_id, FK) | Sim | **Sim, reaplicado** | Não | Não aplicada em nenhum banco (live em 092). Aguarda teste em banco descartável. |
| `migrations/000094_hub_rls_policies.*` | RLS Hub | Sim (ENABLE+FORCE em 9 tabelas) | Sim | **Sim, reaplicado** | Não | Políticas não provadas por teste real. Pendente do gate de RLS. |
| `migrations/000095_hub_seed_test_data.*` | seed de teste | **Não**: insere hub de teste com UUID fixo em todo ambiente que rodar `migrate` | Como fixture | Como `internal/hub/testdata/` | **Sai de migrations/** | Rodaria em produção. Comentário cita uma flag `.env` que não existe. |
| `migrations/000096_integration_gateway.*` | schema gateway | Sim | Sim | **Sim, reaplicado** | Não | Não aplicada. Lacuna 095 é inofensiva (`migrate-sql.sh` ordena por nome). |
| `internal/hub/{domain,ports,adapters/postgres.go}` | backend Hub | Sim | Sim | **Sim, reaplicado** | Não | Compila e passa em `go vet`. Sem teste real de banco. |
| `internal/integrations/**` | gateway + adapters | Sim | Sim | **Sim, reaplicado** | Não | Compila. Phase 8 ("migrar integrações existentes") não alterou os adapters reais de WAHA/Meta/K3G; são arquivos novos sem consumidores. |
| `internal/tenancy/domain/context.go` | TenantContext | Sim (aditivo: 4 campos novos e 1 factory) | Sim | **Sim, reaplicado** | Não | Compatível com os ~156 arquivos que importam o pacote. |
| `internal/tenancy/domain/context_test.go` | teste unitário | Sim | Sim | **Sim, com correção** | Não | Tinha `WithTenantContext(nil,…)` que dava panic. Corrigido; passa. |
| `internal/ai/tools/authorization.go` | interface stub | Sim | Sim | Sim | Não | Só interface, sem uso. Especulativo, baixo risco. |
| `internal/hub/adapters/postgres_test.go` | "teste de RLS" | n/a | Só no backup | **Não** | **Sim** | Não compila (`for a,b,c := range`), depende de `testhelpers.New` inexistente e todos os `t.Run` são `TODO` sem asserção. Verde vazio seria enganoso. |
| `internal/e2e/multitenant_hub_test.go` | "e2e Hub" | n/a | Só no backup | **Não** | **Sim** | Não compila (`testhelpers.New` inexistente; importa `AuthorizationService`, apagado depois). |
| `.agent/multitenant-service-hub/{PRD,ADRS-ESSENTIAL,PHASE-0-AUDIT,acceptance,mission}` | especificação | Sim | Sim | **Sim** | Não | Insumo de produto. `mission.yaml` marca fases 3–13 `pending`, o que contradiz os docs "COMPLETE". |
| `.agent/…/{MISSION-COMPLETE,FINAL-REPORT,DELIVERY-COMPLETE,PHASE-13/14/15-*,DEPLOYMENT-READY}.md` | relatórios | Não | Só no backup | Não | **Sim** | Declaram gate de segurança passado, "zero P0", E2E PASS e "PRODUCTION READY" sem execução. |
| `docs/HUB-*.md`, `docs/E2E-TEST-FLOW.md`, `docs/ROLLOUT-MOBILE-ANALYTICS.md`, `docs/PRODUCTION-STATUS-REPORT.md`, `docs/DEPLOYMENT-READY.md`, `docs/phase-*.md`, `EXECUTION-SUMMARY.md` | docs | Não | Só no backup | Não | **Sim** | Descrevem rotas `/hub-workspace/*`, `/admin/hubs/*`, SLA e fluxos que **não existem no código**. |
| `docs/hub-workspace-ux-spec.md`, `docs/phase-9-lovable-handoff.md` | spec UX | Parcial | Só no backup | Reaproveitar como insumo | Não | O conteúdo servirá de base para a matriz de integração UX, depois de revisado. |
| `scripts/deploy-production.sh`, `scripts/deploy-hub-day0.sh` | deploy | **Não** (ignoram a receita docker compose; escrevem em `/var/www`) | Só no backup | Não | **Sim** | Contradizem o deploy real. |
| `docs/HUB-NGINX-*.conf` | nginx | Não | Só no backup | Não | **Sim** | Sobrepõem `00-omnira.conf`; geraram conflito de `server_name`. |
| `omnira-api` (binário de 34 MB), `go.mod`, `go.sum` | build | Não | Não | Não | **Sim** | Binário versionado, rebuild de árvore intermediária. `go.mod` adicionou `gorilla/mux` sem uso. Restaurados de `main`. |
| `web/src/components/hub/HubWorkspace.tsx` | UI órfã | **Não**: sem rota, props mockadas, `hub-theme`/tokens inexistentes, escrita à parte do design system | Só no backup | Não | **Sim** | Um terceiro produto. Será substituído por adaptação de componentes existentes. |
| `web/…/AIIntegrationCard*`, `lib/aiIntegration.ts`, `ai_integration_http*.go`, `omnira-v1.yaml` | fix Gemini | Sim | n/a | n/a | n/a | Já em `main` (`8ce866d`, `94250f4`). Não é trabalho do Hub. |

## Estado de verificação das mudanças preservadas

| Verificação | Resultado |
|---|---|
| `go build ./...` | OK |
| `go vet` (hub, integrations, tenancy/domain, ai/tools) | OK |
| `go test ./internal/tenancy/domain/...` | OK |
| Migrations 093/094/096 em Postgres real | **NÃO EXECUTADO** |
| RLS Hub (isolamento A/B/C, revogação) | **NÃO TESTADO** (os testes antigos eram esqueletos) |
| Rotas HTTP do Hub no `omnira-api` | **NÃO EXISTEM** |
