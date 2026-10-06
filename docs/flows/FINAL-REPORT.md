# Flow Builder — relatório final da entrega

Data: 2026-10-06 · Branch `feat/flow-builder` (worktree `/home/suporte/projects/omnira-flow-builder`, base `main` @ `8af6d54`) · ADR-0019.
Estado: **LAB** · Nada implantado, enviado, etiquetado ou mesclado · Gate: `PRODUCTION-GATE.md` · Uso: `USER-GUIDE.md`.

## Resumo

O Visual Flow Builder foi entregue de ponta a ponta: **backend** (domínio, persistência com RLS, control plane, motor de execução,
17+3 tipos de nó, simulador, biblioteca de templates e packs, instalador, nós de IA opcionais, histórico de execuções, analytics e
métricas), **integração** ao pipeline de mensagens (gate no ingest, consumidor no worker, sweeper) e **frontend** em `web/`
(editor visual, biblioteca/instalação, execuções). Tudo **aditivo e atrás de flags desligadas**: com `OMNIRA_FLOWS_ENABLED=false` o
sistema se comporta como antes (provado por teste de ingest real). Foi verificado em Postgres real, com detector de corrida, com
mutações que provam que os testes mordem e em um Chromium real.

**Uma correção de rumo importante, registrada com transparência:** a primeira passada desta sessão produziu um esqueleto que
**nunca compilou** (módulo errado, RLS fora do padrão, frontend duplicado em `apps/web`) e cujos commits afirmavam testes que não
tinham rodado. Foi **revertida e descartada** (branch `feat/flow-builder-discarded-scaffold`, backups em `backup/…` e em
`git bundle`) e tudo foi refeito com compilação e testes reais.

## Entrega por fase (um commit por fase)

| Fase da spec | Entrega | Commit |
|---|---|---|
| FLOW.0 auditoria | ADR-0019, análise de conflito, baseline medida | `9fbd97a` (+ `fbeffeb` conserto de build isolado) |
| FLOW.1 domínio/persistência | 6 tabelas com RLS `FORCE`, versões imutáveis (trigger), 1 run ativo por conversa e 1 run por evento no banco, permissões, repositório | `2fa4665` |
| FLOW.2 control plane | validador do servidor, rascunho com validação ao vivo, publicar/rollback/arquivar, pin de subflows, auditoria, REST + RBAC, OpenAPI | `d6b31c9` |
| FLOW.3 runtime | motor transacional por conversa, idempotência, retomada, limites, subflows, autoridade derivada do bot | `c0a6926` |
| FLOW.4 nós + efeitos | 17 nós determinísticos, efeitos reais, `SystemSender` (envio do bot) | `af1a26d` |
| (integração) | gate no ingest, consumidor JetStream, sweeper, flag e compose | `e0cb2a1` |
| FLOW.6 simulador | motor real sobre memória, sem nenhum efeito | `7ee44d4` |
| FLOW.7–9 templates/packs | 24 templates em 3 packs (Starter, K3G, ISP NOC), 88 cenários, instalador atômico, API | `04ee9d3` |
| FLOW.10–11 IA e observabilidade | 3 nós de IA que só sugerem, runs/timeline/analytics, métricas Prometheus | `46b2c73` |
| FLOW.5 frontend | editor visual, biblioteca + assistente, execuções | `c5433c2` (+ `d84952f` docs) |
| FLOW.12 gate | `PRODUCTION-GATE.md`, este relatório, `USER-GUIDE.md` | (este commit) |

## Números

Série de commits por fase (`git log main..HEAD`) · 114+ arquivos · ~9,1 mil linhas de Go de produção e ~4,8 mil de teste · ~2,9 mil linhas de web de produção ·
3 migrations · 23 operações REST documentadas · 20 tipos de nó · 24 templates / 3 packs / 88 cenários Given-When-Then ·
**117 testes Go** dos módulos novos (0 falhas; suíte completa 69 pacotes ok com as mesmas 5 falhas preexistentes de `main`) ·
**614 testes web** passam · 2 cenários em Chromium real · **0 dependências novas**.

## Decisões de arquitetura (detalhe no ADR-0019)

1. **Dois planos:** control plane (rascunho → validação → versão imutável → ponteiro ativo) e runtime (um evento = um passo transacional, conversa travada). O frontend nunca executa nada.
2. **Templates e packs são código** (DSL em Go, versionados, hash fixado): o banco guarda só instalações. Instalar clona para um **rascunho do tenant** e não deixa vínculo operacional com o modelo.
3. **Autoridade da conversa é derivada** (`bot` **e** sem responsável): quando um operador assume, o bot cala sem nenhum gatilho novo no claim existente.
4. **A empresa atendida não mora na conversa** (invariante do ADR-0017/0018, protegido por teste): fica no run e no chamado; com várias empresas o bot pergunta, nunca escolhe.
5. **A IA só sugere:** prioridade/fila não vêm de variável; saídas de erro/baixa confiança são obrigatórias; o simulador nunca chama modelo.
6. **Tudo aditivo e reversível:** migrations testadas nos dois sentidos, flag por camada (`OMNIRA_FLOWS_ENABLED`, `OMNIRA_FLOWS_AI_ENABLED`).

## O que foi alterado em código que não é novo (toda mudança é de adição, salvo as 3 linhas do conserto de build)

`inbox/application/inbound.go` (+26/−1, interface opcional `FlowGate`) · `apps/api/.../main.go` (+27) e `apps/worker/.../main.go` (+46) · `platform/config/config.go` (+7) · `platform/httpserver/server.go` (+17/−1: a função nova `RegisterFlowHandlers` e o conserto de 1 linha) · `platform/authn/password_handler.go` (−1 import, conserto de build) · `docker-compose.yml` (+4) · `iam3/security_test.go` (+4, matriz de papéis) · `web/src/App.tsx` (+4), `Sidebar.tsx` (+2), `permissions.ts` (+14). Total de linhas removidas em todo o diff: 3 (os dois consertos de build e 1 linha do `inbound.go`). Nenhuma migration existente foi tocada.

## Achados fora desta feature (não corrigidos de propósito, para não mexer no trabalho da outra frente)

- `main` **não compilava** (`authn` com import sem uso; `server.go:214` com `HandleFunc` recebendo `http.Handler`): consertado em commit isolado e descartável (`fbeffeb`). Três pacotes de **teste** (`authn`, `httpserver`, `tenancy/adapters`) seguem sem compilar por mudança de assinatura do IAM5.
- `web/src/__tests__/SettingsShell.test.tsx` falha em `main` (o commit `6f2a380` mudou o componente sem atualizar o teste).
- `internal/outbox/adapters TestStoreUsesInjectedTransaction` e o teste de performance de `intelligence/adapters` falham na baseline.
- **`web/src/components/primitives/Modal.tsx`** refocava o primeiro controle a cada mudança de `onClose` e engolia texto digitado. **Corrigido** (ref + dependência só de `[open]`, com teste de regressão); o contorno `StableModal` foi removido.

## Decisões que precisam de você

1. **Revisão independente (Codex)** antes do piloto: não foi executada (`CODEX_PLUGIN_NOT_EXECUTED`); não afirmo "zero CRITICAL/HIGH".
2. **Polimento visual pelo Lovable?** Construí localmente (sem custo de créditos e sem dependência). Se quiser o acabamento do projeto OmniFlow Hub, o modelo e a API não mudam.
3. ~~Corrigir o `Modal` compartilhado~~ **feito** (2026-10-06). Os testes quebrados de `main` seguem com as frentes IAM5/agentes.
4. **Ordem de merge:** reconferir o número das migrations (`0000082+`) e resolver por união os 4 arquivos compartilhados (`App.tsx`, `Sidebar.tsx`, `permissions.ts`, `iam3/security_test.go`).
5. **`ai_agent`** (agente autônomo com ferramentas) ficou de fora por ser a primeira capacidade da IA com efeito: pede ADR e modelo de permissão de ferramenta.
6. **Piloto:** seguir as condições do gate (aceite, revisão, restore, tenant de teste, smoke com telefone real, 24 h observadas).

## Como verificar (sem tocar em nada vivo)

```bash
# banco descartável + todas as migrations (nunca omnira_dev; sem URL de NATS)
docker run --rm --network host -v "$PWD":/src -v omnira-gomod:/go/pkg/mod -w /src \
  -e GOFLAGS=-buildvcs=false -e OMNIRA_INTEGRATION_TEST=1 \
  -e OMNIRA_DATABASE_URL='postgres://omnira:omnira@127.0.0.1:55434/omnira_test_flows?sslmode=disable' \
  -e OMNIRA_APP_DATABASE_URL='postgres://omnira_app:omnira_app@127.0.0.1:55434/omnira_test_flows?sslmode=disable' \
  golang:1.25 go test -count=1 -p 1 ./internal/flows/... ./internal/worker/flows/... ./internal/messages/...
# web (Node 22) e navegador real
docker run --rm -v "$PWD/web":/app -w /app node:22 sh -c 'npm ci && npx tsc --noEmit && npx vitest run'
docker run --rm -v "$PWD/web":/app -w /app --ipc=host mcr.microsoft.com/playwright:v1.63.0-noble \
  sh -c 'npm run build && npx playwright test -c playwright.mock.config.ts flows.mock.spec.ts'
```
O banco descartável é criado assim (nunca `omnira_dev`):
```bash
P="docker exec -i omnira-postgres psql -U omnira -v ON_ERROR_STOP=1 -q"
$P -d postgres -c "DROP DATABASE IF EXISTS omnira_test_flows" -c "CREATE DATABASE omnira_test_flows"
for f in migrations/*.up.sql; do $P -d omnira_test_flows < "$f" || break; done
```

## Backup e estado do git

- `git bundle` por fase em `/home/suporte/projects/omnira-flow-builder-backups/` (8 arquivos) e branch `backup/flow-builder-before-rebuild-*`.
- Branch descartada preservada: `feat/flow-builder-discarded-scaffold`.
- Árvore principal (`20-OMNIRA`) **não foi tocada**; nada de `push`, `tag` ou merge.
- Memória do projeto atualizada (`flow-builder-feat-branch.md`) com decisões, armadilhas e lições.
