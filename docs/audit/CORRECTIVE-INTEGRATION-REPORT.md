# CORRECTIVE-INTEGRATION-REPORT

Estado: **Steps 1–4 concluídos.** Steps 5–16 pendentes (seção 7).

## 1. O que a tentativa anterior fez
- Tratou o Hub como site separado: redirect nginx para um deploy público do Lovable e escrita em `/var/www/omnira-frontend` (diretório que a produção real não serve).
- Adicionou um componente `HubWorkspace.tsx` sem rota nem contrato de dados.
- Declarou "PRODUCTION READY", gate de segurança passado e E2E PASS sem executar nenhum desses passos. Documentou rotas HTTP do Hub que não existem.

## 2. O que era incompatível com o OMNIRA
Ver `LOVABLE_REPLACEMENT_AUDIT.md`. Resumo: migration de seed de teste em `migrations/`, `go.mod` com dependência sem uso, binário de 34 MB versionado, scripts de deploy que ignoram a receita docker compose, confs nginx que conflitam com `00-omnira.conf`, componente órfão e dois "testes" que não compilam e não afirmam nada.

## 3. O que foi preservado
Na branch `fix/integrate-lovable-into-omnira` (base `main` = `94250f4`):
- Migrations `000093`, `000094`, `000096` (schema Hub, RLS, gateway de integrações).
- `internal/hub/{domain,ports,adapters/postgres.go}`.
- `internal/integrations/**` (gateway e adapters novos, sem consumidores).
- `internal/tenancy/domain/context.go`: extensão aditiva do `TenantContext` + `NewHubTenantContext`.
- `internal/ai/tools/authorization.go` (interface, sem uso).
- Especificação: PRD, ADRS-ESSENTIAL, PHASE-0-AUDIT, acceptance.yaml, mission.yaml.
- Seed de teste movido para `internal/hub/testdata/` (fora de `migrations/`).

Backup completo do estado anterior: `backup/lovable-rebuild-attempt` (`8d3bf83`).

## 4. Código/padrões Lovable mantidos
Nenhum código Lovable foi mantido. O que existe do Lovable é só o resultado visual (layout 3 painéis, hierarquia da lista, painel de contexto), que será usado como referência na matriz de integração UX (Step 6).

## 5. Código Lovable rejeitado
`HubWorkspace.tsx`, `hub-theme.css` (nunca integrado), o deploy público e o redirect nginx.

## 6. Arquitetura OMNIRA preservada
`web/` idêntico a `main` (`git diff main -- web` vazio). Router, auth, `TenantSwitcher`/`useMyTenants`, API client, tokens Tailwind e build intactos. Backend de `apps/`, outras partes de `internal/`, workers e compose intactos.

## 7. Fatias restantes
| Step | Descrição | Estado |
|---|---|---|
| 5 | Restaurar arquitetura frontend | feito por construção (nenhuma mudança em `web/`) |
| 6–7 | Matriz de integração UX, Component Mapping, API Mapping | pendente. Requer inspecionar o `omniflow-hub` (não clonado localmente) |
| 8 | ADR "Frontend Evolution Strategy" + regra em CLAUDE.md/AGENTS.md | pendente |
| 9 | Tenant Context Bar no Conversation Cockpit existente | pendente |
| 10 | Rodar testes de regressão existentes do frontend | pendente (baseline ainda não capturado) |
| 11 | Hub Workspace aditivo (`/hub`, API `GET /hub/inbox`) | pendente. Backend não tem handlers HTTP |
| 13 | Testes cross-tenant/RLS reais | pendente. Testes antigos eram esqueletos |
| 14–15 | Revisão adversarial Codex | pendente |

## 8. Status de segurança
- **RLS do Hub: NÃO PROVADA.** Migrations nunca aplicadas em Postgres real; nenhum teste de isolamento existe.
- Nenhuma rota do Hub está exposta (não existe handler), então não há superfície nova em produção.
- Banco vivo `omnira_dev` permanece em `000092`, sem tabelas do Hub.

## 9. Testes
| Verificação | Resultado |
|---|---|
| `go build ./...` | OK |
| `go vet` (hub, integrations, tenancy/domain, ai/tools) | OK |
| `go test ./internal/tenancy/domain/...` | OK |
| Testes de integração com Postgres (migrations 093–096 + RLS) | não executados |
| Regressão do frontend | não executada (`web/` sem mudanças) |

## 10. Baseline de commits
- BASELINE_OMNIRA: `6d36f67`
- main: `94250f4`
- Backup: `backup/lovable-rebuild-attempt` = `8d3bf83`
- Corretiva: `fix/integrate-lovable-into-omnira`, a partir de `main`
