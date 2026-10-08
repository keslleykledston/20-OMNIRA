# CORRECTIVE-INTEGRATION-REPORT — checkpoint 2026-10-08

## ATUALIZAÇÃO — 3ª fatia: tela `/hub` (autorizada: "siga")
Continua **local, sem push, nada implantado**.
- Backend: `GET /api/v1/hubs` (meus hubs) e `tenant_name` nas linhas, lidos pela sessão do próprio agente. **HTTP VERIFIED**, 5 mutantes mortos.
- Frontend: página `/hub` (lista com o nome da empresa, conversa somente leitura, sem composer, sem mídia), entrada no Sidebar e na barra mobile só para membros de Hub com a flag ligada. 21 testes jsdom, 10 mutantes mortos, **Chromium real** (desktop e 390 px, API mockada, telas revisadas). Bug real encontrado pelos testes e corrigido: id de item vazando para outro Hub por um render. `MessageBubble` deliberadamente não reutilizado (mídia pela empresa da SESSÃO).
- **E2E com binários reais** (`scripts/e2e-hub-smoke.sh`): `omnira-api` + `omnira-hubctl` + projetor + Postgres + NATS, **14/14**; falha quando a revogação ou a flag quebram. Nova ferramenta: `omnira-hubctl reconcile`.
- Frontend completo: 715/716 testes (o vermelho é o `SettingsShell`, também no `main`); `tsc` e build ok. Specs de navegador mockadas: as 5 que falham (3 de `attendance`/`topics`, 2 de `flows` por `EACCES` em diretório root) **também falham no `main`**; as 3 novas do Hub passam.
- **Não verificado:** navegador real contra o API real, Keycloak/OIDC real, canal real; escrita pelo Hub; painel de contexto do Hub.

---

## ATUALIZAÇÃO — 2ª fatia (autorizada pelo dono: "pode tratar com a mesma proteção" e "siga")
Tudo continua **local, sem push, nada implantado**; o banco vivo segue em `092`.

| Entrega | Estado de verificação |
|---|---|
| `has_active_membership` / `has_active_admin_membership` só respondem pelo usuário da sessão ou sessão de sistema (migration 097) | **POSTGRES VERIFIED**: teste vermelho sem a 097, verde com ela. Nenhum chamador legítimo muda (todas as policies passam `current_user_id()`; nenhum código Go as chama) |
| Projetor da inbox do Hub (`internal/worker/hubprojector`), no worker atrás de `OMNIRA_HUB_PROJECTOR_ENABLED=false` | **POSTGRES VERIFIED**; 9 mutações do SQL mortas; suíte hermética (3 execuções seguidas em banco sujo). Inbox ordenada por `last_activity_at` |
| Provisionamento: serviço + `omnira-hubctl` (CLI de operador, auditada) + runbook `docs/ops/HUB-PROVISIONING.md` | **POSTGRES VERIFIED** + CLI executada de ponta a ponta; 8 mutações mortas; a imagem do API compila com a ferramenta. **Sem API HTTP** (o OMNIRA não tem "administrador de plataforma" humano) |
| Faixa de contexto de tenant no `ChatPane` (`TenantContextBar`, `TenantBadge`, `--tenant-accent`) | **UNIT VERIFIED** (jsdom), 4 mutações mortas. **Não** verificada em navegador real |

**Gate de integração completo (31 pacotes): 29 ok, 2 falham, ambos idênticos no `main` limpo** (10 testes de `tenancy/adapters`, 2 de `worker/jobsstream`). Regressões introduzidas: **0**.
**Frontend:** `tsc` e build de produção ok; 697 de 698 testes passam; o único vermelho de teste (`SettingsShell`) e uma spec Playwright capturada pelo vitest **também falham no `main`**.

### Revisão do Codex nº 2 (projetor, provisionamento, CLI, 097)
Foi **iniciada** (`task-muyxtuiu-n3c6zb`), mas **o relatório não foi lido**: `/codex:status` e `/codex:result` são reservados ao usuário. Registro `CODEX_PLUGIN_NOT_EXECUTED` para esta rodada. **Pendente do dono:** rodar `/codex:result task-muyxtuiu-n3c6zb` e me passar os achados. Em substituição fiz revisão própria (não é independente): tenant/hub nunca vêm de payload, SQL parametrizado, validações no servidor, auditoria na mesma transação. Dela saiu uma melhoria: o `--operator` é texto livre, então a CLI agora grava **também o usuário e o host do sistema operacional** ao lado do nome informado (evidência, não prova).

### Limites desta fatia
- Projetor é **reconciliação periódica**, não por evento; `sla_due_at` não é projetado (sem fonte de SLA).
- Quem consegue executar comandos no container do API já tem as credenciais do banco; a CLI não amplia esse limite, só o torna auditável.
- Concorrência de dois workers projetando o mesmo par é segura (upsert idempotente), mas pode haver um deadlock raro; ele é registrado e a próxima rodada corrige.

---

Branch `fix/integrate-lovable-into-omnira` (**locais, sem push, nada implantado**). Backup do estado anterior:
`backup/lovable-rebuild-attempt` (`8d3bf83`). Frontend (`web/`) **idêntico a `main`**, congelado.

Vocabulário: IMPLEMENTED · NOT WIRED · UNIT / POSTGRES / HTTP / E2E VERIFIED · BLOCKED. `build` e `vet` são pré-requisito, não aceitação.

## MIGRATIONS
- Cadeia agora: `093_service_hubs`, `094_hub_rls_policies`, `095_hub_delegated_read`, `096_harden_security_definer_search_path`. A `096` antiga (gateway de integrações) e a `095` antiga (seed de teste) foram **retiradas**.
- Prova do histórico antes de reutilizar 095/096: nenhum ref local (`main`, `origin/main`, `master`, branches de feature) nem banco local passa da `092`; os números só existiam na branch de backup.
- **POSTGRES VERIFIED** no migrador de produção (`scripts/test-hub-migrations.sh`): baseline 092 → `up` → `down` em ordem inversa (schema **idêntico** ao pré-Hub) → `up` (schema idêntico ao primeiro). Também verifica RLS+FORCE+policy em toda tabela com `tenant_id`, nas 9 tabelas do Hub e que `omnira_app` não contorna RLS.
- O original quebrava: `ERROR: infinite recursion detected in policy for relation "hub_memberships"` (reproduzido como `omnira_app`).
- Banco vivo `omnira_dev`: continua em `092`. **Nenhuma migration aplicada em produção.**

## RLS
- **POSTGRES VERIFIED**: `internal/hub/adapters/rls_integration_test.go` (papel `omnira_app`, `WithTenantSession`, sempre `isSystemAdmin=false`). Cobre RLS-001..012: membro direto, delegação A/B/C, Bob, membership sem grant, grant expirado/futuro/revogado/suspenso, saída do hub, contrato revogado/suspenso/expirado, hub suspenso, escopo de fila (lista, vazia, malformada, fila de outro tenant), IDOR por UUID, somente leitura, credenciais/integrações invisíveis ao agente, integridade relacional (FKs compostas, CHECKs).
- **Os testes falham quando a policy está errada**: `scripts/test-hub-rls-mutations.sh`, **16 mutantes, todos detectados** (13 na função de acesso, 1 policy de escrita, 2 na policy da inbox). Um mutante equivalente (só trocar `search_path`) foi identificado e substituído por uma mutação das duas camadas.
- Acesso do Hub é **somente leitura** (`tenants`, `conversations`, `messages`, `hub_inbox_items`). Escrita via Hub: sem policy e todo caminho de escrita existente exige `Source==direct`.

## AUTHORIZATION
- `application.HubAuthorizationService` + `EffectiveAccessResolver`: fonte (`direct`/`hub`) **obrigatória e explícita**, sem fallback em nenhuma direção; negação única e opaca (`ErrAccessDenied`) com motivo só para auditoria.
- **UNIT VERIFIED** (22 negativas + resolver) e **POSTGRES VERIFIED**: serviço Go e RLS **concordam em 15 estados** (`TestHubAuthorizationAgreesWithRLS`).
- `TenantContext`: `NewTenantContext` recusa `hub`, fonte desconhecida e fonte vazia; Hub só por `NewHubTenantContext` (carrega hub, contrato, grant, work pool, correlação).
- Nenhum atalho de system-admin: nenhum teste do agente usa `is_system_admin()`.

## HUB
`HUB-VERIFICATION-STATUS.md` classifica cada parte. Resumo: schema/RLS/autorização/API de leitura **verificados**; **projetor da inbox NOT WIRED**, provisionamento **NOT BUILT**, escrita via Hub **NOT BUILT**, UI **NOT BUILT**. API `GET /api/v1/hubs/{hub_id}/inbox[/{item_id}]` **HTTP VERIFIED**, atrás de `OMNIRA_HUB_API_ENABLED=false`, registrada no contrato OpenAPI e no guard de drift. Candidatos a remoção: ~24 métodos do repositório sem consumidor.

## INTEGRATIONS
`INTEGRATION_CONVERGENCE.md`: o OMNIRA já resolve idempotência (`ticket_external_create_attempts`), deduplicação de webhook (`channel_webhook_events` + `ON CONFLICT`), runtime de provedor (`TicketingConnector`/`K3GTicketingRuntimeResolver`), credencial por tenant (`channel_credentials`), ID externo e reconciliação. O gateway da missão (`internal/integrations`, tabelas `integration_*`) foi **retirado**: zero consumidores, duas tabelas sem RLS (reprova `TestRLSCompleteness`), dedupe com corrida e duplicação do que existe. Integração Hub→ERP: **inexistente**; credenciais e estado de integração são invisíveis ao agente do Hub (POSTGRES VERIFIED).

## UX MAPPING
`docs/ux/LOVABLE_INTEGRATION_MATRIX.md` (28 linhas KEEP/RESTYLE/EXTEND/ADAPT/REJECT), `COMPONENT_MAPPING.md`, `API_MAPPING.md`, `docs/adr/0036-frontend-evolution-strategy.md`, regra curta em `CLAUDE.md` e `AGENTS.md`. Limites: `omniflow-hub` no GitHub é privado e sem credencial/`gh` nesta máquina; a inspeção usou o MCP da Lovable, e vários componentes do protótipo **não foram lidos** (listados na matriz). `customer-space-central.lovable.app` = protótipo/mock ≠ produção; **não foi alterado**.

## TESTS
| Verificação | Resultado |
|---|---|
| `go build ./...` | OK |
| Unitários: `hub/application`, `tenancy/domain`, `platform/config`, `platform/httpserver` (inclui guard OpenAPI×rotas), `messages`, `flows`, `apps/api` | OK |
| `scripts/test-hub-migrations.sh` | PASS (093..096) |
| `scripts/test-hub-rls-mutations.sh` | PASS (16/16 mutantes mortos) |
| Gate de integração completo (`scripts/test-integration.sh`, 29 pacotes) | **27 ok, 2 falham**, ambos **idênticos no `main` limpo**: `tenancy/adapters` (10 testes: aceite de convite, matriz de papéis) e `worker/jobsstream` (2 testes: `nats: API error 10047`). Regressões introduzidas pela branch: **0** |
| `TestRLSCompleteness`, `hub/adapters`, `tickets/adapters`, `channels/adapters`, `inbox/adapters` | ok |
| E2E com canal/navegador real | **não executado** |

## CODEX FINDINGS
Revisão somente leitura, escopo restrito à autorização do Hub. 8 achados (1 crítico, 2 altos, 4 médios, 1 baixo). **Todos reproduzidos por teste vermelho antes de corrigir e corrigidos**; detalhes em `HUB-VERIFICATION-STATUS.md`. O crítico (tabela `TEMP` falsificada em função `SECURITY DEFINER`) existia também em 9 funções **pré-existentes** do `main`; corrigido de forma independente na migration 096. O Codex não conseguiu executar testes (sandbox somente leitura): `CODEX_PLUGIN_NOT_EXECUTED` para a execução; a leitura foi feita. Não foi pedida revisão de UX.

## COMMITS (sobre `main`@`94250f4`)
```
c2e219c  re-aplica só o backend verificado; auditoria do desvio
bc34177  migrations 093-095 reescritas + provas (roundtrip, RLS real, mutação)
f8ee78e  remove gateway de integrações e interface de ferramenta duplicada
7c7ca0d  resolução explícita direct/hub, sem fallback nem atalho de system-admin
92a2180  API de leitura da inbox do Hub atrás de flag
9aee984  ADR-0036, mapeamentos de UX, convergência, status de verificação
b73f2ac  auditoria atualizada
51d0dee  096: pg_temp por último nas funções SECURITY DEFINER (independente do Hub)
96231b0  fecha os achados da revisão adversarial
```

## BLOCKERS / PENDÊNCIAS
1. **`main` andou** (`aba7523`) desde o ponto de partida; rebasear antes de qualquer merge (sem colisão de migrations: `main` ainda termina em 092).
2. 12 testes que falham no `main` (convites/papéis; JetStream `10047`): fora do escopo, mas bloqueiam um gate verde de verdade.
3. Oráculo pré-existente `has_active_membership(tenant, user)`: não alterado (muda código fora do Hub).
4. Projeto Lovable publicado com rota pública que serve um build (`/api/public/download/...`): decisão do dono (não alterado).
5. Sem aprovação do dono para aplicar migrations em `omnira_dev`/produção, nem para ligar `OMNIRA_HUB_API_ENABLED`.

## NEXT SLICE
**Projetor da inbox** (conversa → `hub_inbox_items`, via worker em contexto de sistema derivado de estado persistido) com teste de reconciliação e de fila mudada; depois provisionamento mínimo (criar hub/contrato/grant por API de admin com auditoria). Só então a primeira superfície visual: faixa de contexto de tenant no cabeçalho do `ChatPane` (`LOVABLE_INTEGRATION_MATRIX.md`, linha 6).

## REGRA DE SAÍDA (autorização para mexer em UI)
As três afirmações exigidas estão provadas com evidência: (1) acesso delegado funciona no PostgreSQL real; (2) acesso cruzado indevido é bloqueado no PostgreSQL real; (3) a autorização da aplicação estabelece o `EffectiveTenantContext` sem system-admin. **A decisão de liberar o frontend é do dono**; até lá, `web/` permanece congelado.
