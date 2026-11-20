# Copiloto de grupo (ADR-0050) — relatório de entrega

Data: 2026-11-20. Branch: `feat/group-copilot-memory` (worktree `/data/cafegpt/tmp/wt-groupmem`), a partir de `main` em `714c114`. **Nada implantado, nada enviado ao remoto, nenhuma migration aplicada em banco vivo.** Estado por camada, no vocabulário do projeto:

| Camada | Estado |
|---|---|
| Banco (migrations 000169–000171, RLS FORCE, append-only, permissões) | POSTGRES VERIFIED (cadeia completa aplicada em Postgres descartável; varredura de RLS passou) |
| Domínio (parser de exportação, extração, consolidação, regras de descarte) | UNIT VERIFIED |
| Aplicação + adaptadores Postgres + HTTP | POSTGRES VERIFIED (14 testes de integração, RLS real, runtime `omnira_app`) |
| Contrato OpenAPI `contracts/openapi/group-ai-v1.yaml` (19 operações) | HTTP-surface VERIFIED (deriva nas duas direções; 401 anônimo/credencial forjada) |
| Worker (`Engine.Run`), wiring api/worker, compose, nginx do `web` | IMPLEMENTED, NOT WIRED a nenhum ambiente (compila; não subiu nenhum contêiner) |
| Provedor real (Gemini) | NOT VERIFIED: os testes usam um modelo falso; nenhuma chamada real foi feita |
| Painel web | ver o relatório do frontend (abaixo/anexo) — visual pendente da aprovação do dono |
| E2E com WhatsApp/WAHA reais | NOT VERIFIED |
| Codex (revisão) | `CODEX_PLUGIN_NOT_EXECUTED` |

## Fatias (ADR-0050)
- **M0 modelo e permissões:** `wa_groups.ai_enabled*`, `wa_group_memory_items`, `wa_group_memory_item_events` (append-only: `UPDATE/DELETE` revogados + gatilho), `wa_group_memory_state`; permissões `group.ai.read`, `group.ai.manage`, `group.import`, `group.memory.erase` (admin e supervisor).
- **M1 memória a partir do histórico:** `internal/groupai` (domain/application/adapters). O worker lê as mensagens novas em lotes, o modelo devolve operações (`add`/`resolve`/`update`/`reinforce`), validadas uma a uma; evidência verificada contra a mensagem; item protegido imune.
- **M2 importar arquivo:** parser de `.txt`/`.zip` (Android/iOS, dia-mês ou mês-dia detectado, multilinha, mídia como marcador, sistema/apagada/vazia contadas), antivírus fail-closed, cifra (HKDF + AES-256-GCM), raw + normalizado imutáveis, registro de cobertura, dedupe contra mensagens vivas, descarte por regra mecânica (cobertura completa + confirmação, ou 90 dias), reprocessamento.
- **M3 propostas:** rascunho (modelo ou o próprio texto do item), edição, aprovação que cria `service_cases`/`service_tasks` na sessão de quem aprova, rejeição; idempotente.
- **M4 consolidação e apagar:** camada consolidada (sem citação literal, pessoas por papel), relevância com meia-vida de 60 dias (ordena o prompt, nunca apaga), "apagar histórico" (ADR-0015) removendo citações e raw, "apagar memória" (`group.memory.erase`).

## Testes executados (comandos reais)
- `go vet ./...` — limpo.
- `go test ./internal/groupai/... ./internal/platform/httpserver/... ./internal/intelligence/... ./internal/audit/... ./internal/groups/...` — ok (os de integração pulam sem o banco; ver abaixo).
- `scripts/test-integration.sh ./internal/groupai/adapters` — **ok** (14 testes em Postgres 16 descartável).
- `scripts/test-integration.sh ./internal/platform/db ./internal/groups/adapters` — **ok** (inclui `TestRLSCompleteness` sobre as tabelas novas e a regressão do módulo de grupos com o gancho de "apagar histórico").
- A suíte de integração **completa** (sem argumentos) **não foi rodada** nesta entrega.

## O que os testes de integração provam
Tenant A/B com acesso cruzado de leitura e escrita em **cada** tabela nova; membership revogada; telefone nunca chega ao modelo; uma mensagem não fecha a cerca da zona não confiável; item confirmado por pessoa não é alterado pela IA; teto diário de chamadas; consolidação poupa pedido aberto, item fixado e confirmado; raw cifrado (o texto claro não aparece no banco); import duplicado, infectado, sem antivírus e malformado recusados sem deixar linha; descarte só com cobertura completa, expiração pelo sweep; eventos e blobs imutáveis até para o dono do banco; aprovação cria registros reais no módulo de serviço sem criar conversa nem `tickets`; HTTP: matriz de permissão, 404 igual para outro tenant, item de outro grupo inacessível pelo caminho, erro nunca ecoa conteúdo.

## Limites e pontos a revisar
- O modelo usado nos testes é falso; **prompt e qualidade da extração não foram avaliados com um provedor real.** Antes de ligar em produção: rodar a extração num grupo real de teste e revisar os itens à mão.
- O custo é limitado (48 chamadas/grupo/dia, lotes pequenos), mas o primeiro grupo grande lê um backlog de até 12 lotes por rodada.
- Um import acima do que um passe lê (300 lotes) lê os mais recentes e deixa o resto sem leitura: a cobertura mostra incompleto e o raw só sai por expiração.
- `clamd` padrão rejeita stream acima de 25 MB (falha fechado). O nginx do host precisa liberar o caminho do import (ver ADR, addendum, item 9).
- Não construído: tela de diff do reprocessamento; ferramenta do gateway de IA `group.memory.search` (existe a rota); memória por cliente/conta (ponto de extensão apenas).
- Mensagens de grupo de terceiros são tratadas como hostis (injeção de prompt): a defesa é estrutural (dado, nunca instrução; a IA só propõe; um humano aprova), não um filtro.

## Pendente do dono
1. Aprovar o visual do painel web antes de qualquer deploy.
2. Confirmar os padrões decididos (ADR, addendum, item 8).
3. Decidir quando ativar (ordem em `docs/ops/GROUP-COPILOT.md`) e liberar o limite de corpo no nginx do host.
4. Teste com um grupo real e um modelo real (Gemini) antes de ligar `OMNIRA_GROUP_AUTO_ANALYZE`.
