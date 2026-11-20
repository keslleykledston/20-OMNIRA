# Copiloto de grupo (ADR-0060) — relatório de entrega

Data: 2026-11-20. Branch: `feat/group-copilot-memory` (worktree `/data/cafegpt/tmp/wt-groupmem`), a partir de `main` em `714c114`. **Nada implantado, nada enviado ao remoto, nenhuma migration aplicada em banco vivo.** Estado por camada, no vocabulário do projeto:

| Camada | Estado |
|---|---|
| Banco (migrations 000205–000207, RLS FORCE, append-only, permissões) | POSTGRES VERIFIED (cadeia completa aplicada em Postgres descartável; varredura de RLS passou) |
| Domínio (parser de exportação, extração, consolidação, regras de descarte) | UNIT VERIFIED |
| Aplicação + adaptadores Postgres + HTTP | POSTGRES VERIFIED (25 testes de integração, RLS real, runtime `omnira_app`) |
| Contrato OpenAPI `contracts/openapi/group-ai-v1.yaml` (19 operações) | HTTP-surface VERIFIED (deriva nas duas direções; 401 anônimo/credencial forjada) |
| Worker (`Engine.Run`), wiring api/worker, compose, nginx do `web` | IMPLEMENTED, NOT WIRED a nenhum ambiente (compila; não subiu nenhum contêiner) |
| Provedor real (Gemini) | NOT VERIFIED: os testes usam um modelo falso; nenhuma chamada real foi feita |
| Painel web | ver o relatório do frontend (abaixo/anexo) — visual pendente da aprovação do dono |
| E2E com WhatsApp/WAHA reais | NOT VERIFIED |
| Codex (revisão) | `CODEX_PLUGIN_NOT_EXECUTED`. No lugar: revisão adversarial independente (um agente sem contexto, leitura + testes) — ver "Revisão independente" |

## Fatias (ADR-0060)
- **M0 modelo e permissões:** `wa_groups.ai_enabled*`, `wa_group_memory_items`, `wa_group_memory_item_events` (append-only: `UPDATE/DELETE` revogados + gatilho), `wa_group_memory_state`; permissões `group.ai.read`, `group.ai.manage`, `group.import`, `group.memory.erase` (admin e supervisor).
- **M1 memória a partir do histórico:** `internal/groupai` (domain/application/adapters). O worker lê as mensagens novas em lotes, o modelo devolve operações (`add`/`resolve`/`update`/`reinforce`), validadas uma a uma; evidência verificada contra a mensagem; item protegido imune.
- **M2 importar arquivo:** parser de `.txt`/`.zip` (Android/iOS, dia-mês ou mês-dia detectado, multilinha, mídia como marcador, sistema/apagada/vazia contadas), antivírus fail-closed, cifra (HKDF + AES-256-GCM), raw + normalizado imutáveis, registro de cobertura, dedupe contra mensagens vivas, descarte por regra mecânica (cobertura completa + confirmação, ou 90 dias), reprocessamento.
- **M3 propostas:** rascunho (modelo ou o próprio texto do item), edição, aprovação que cria `service_cases`/`service_tasks` na sessão de quem aprova, rejeição; idempotente.
- **M4 consolidação e apagar:** camada consolidada (sem citação literal, pessoas por papel), relevância com meia-vida de 60 dias (ordena o prompt, nunca apaga), "apagar histórico" (ADR-0015) removendo citações e raw, "apagar memória" (`group.memory.erase`).

## Testes executados (comandos reais)
- `go vet ./...` — limpo.
- `go test ./internal/groupai/... ./internal/platform/httpserver/... ./internal/intelligence/... ./internal/audit/... ./internal/groups/...` — ok (os de integração pulam sem o banco; ver abaixo).
- `scripts/test-integration.sh ./internal/groupai/adapters` — **ok** (25 testes em Postgres 16 descartável).
- `scripts/test-integration.sh ./internal/platform/db ./internal/groups/adapters` — **ok** (inclui `TestRLSCompleteness` sobre as tabelas novas e a regressão do módulo de grupos com o gancho de "apagar histórico").
- Suíte de integração **completa** (sem argumentos, todos os pacotes) rodada uma vez: tudo passou **exceto** (a) `internal/iam3` e `internal/tenancy/adapters`, cujos testes fixam a matriz de permissões de admin e supervisor e passaram a incluir as 4 chaves novas (testes atualizados, depois **ok**), e (b) `internal/sectors/adapters` `TestCalendarEditsHolidaysSuspensionsAndRevision`, que **já falha na `main` sem estas mudanças** (usa datas fixas de outubro de 2026, hoje no passado): preexistente, não tratado aqui. Depois das correções da revisão independente, a suíte completa não foi repetida: rodaram de novo `groupai/adapters`, `groups/adapters`, `platform/db`, `iam3` e `tenancy/adapters`.

## O que os testes de integração provam
Tenant A/B com acesso cruzado de leitura e escrita em **cada** tabela nova; membership revogada; telefone nunca chega ao modelo; uma mensagem não fecha a cerca da zona não confiável; item confirmado por pessoa não é alterado pela IA; teto diário de chamadas; consolidação poupa pedido aberto, item fixado e confirmado; raw cifrado (o texto claro não aparece no banco); import duplicado, infectado, sem antivírus e malformado recusados sem deixar linha; descarte só com cobertura completa, expiração pelo sweep; eventos e blobs imutáveis até para o dono do banco; aprovação cria registros reais no módulo de serviço sem criar conversa nem `tickets`; HTTP: matriz de permissão, 404 igual para outro tenant, item de outro grupo inacessível pelo caminho, erro nunca ecoa conteúdo.

## Revisão independente (2026-11-20)
Um revisor sem contexto leu o diff e reproduziu parte dos achados. Corrigidos e cobertos por teste de regressão: parser de exportação quadrático (DoS por um `.txt` hostil); consolidação que podia substituir um item por ele mesmo; resposta do modelo que resolvia um item consolidado e abortava a transação (travando a leitura do grupo); máscara de telefone burlada por espaço sem quebra, hífen especial, caractere de largura zero e números colados; extração em andamento que continuava gravando depois de "apagar memória"/desligar a IA/descartar o import; import de grupo sem IA travando a fila de todos os tenants; pedido recorrente absorvido por item antigo resolvido (agora reabre); `resolve`/`update` sem evidência (agora exigem; no máximo 10 `resolve` por resposta; item consolidado ou protegido nunca); lote com resposta inválida bloqueando o grupo (pula após 3 tentativas e marca a execução como não-ok); upload sem prazo próprio e erro de leitura reportado como 413; varredura do raw vencido parando com a flag desligada ou empresa suspensa (agora sempre roda); proposta mantendo texto depois de "apagar memória"; edição parcial de proposta apagando campos; mensagem ainda na janela de commit pulada pelo cursor; credencial em mensagem enviada ao provedor; rascunho de proposta pagando chamada ao modelo para uma proposta que já existia, sem teto.

Aceitos como limite conhecido (do relatório do revisor): um import grande pode fazer até 300 chamadas ao modelo sem contar na cota diária do grupo (cada passe só lê; a cobertura mostra o que ficou); a rota GET de busca grava a relevância dos itens encontrados; a política de leitura de `wa_group_import_blobs` deixa qualquer membro ativo do tenant ler o blob **cifrado** (restringir a leitura ao worker quebraria o descarte feito por uma pessoa, que precisa enxergar a linha); `MessageKeys` carrega os corpos das mensagens do intervalo do import; descartes por varredura/apagar não escrevem linha em `audit_events` (escrevem em `wa_group_memory_item_events`, sem conteúdo).

## Limites e pontos a revisar
- O modelo usado nos testes é falso; **prompt e qualidade da extração não foram avaliados com um provedor real.** Antes de ligar em produção: rodar a extração num grupo real de teste e revisar os itens à mão.
- O custo é limitado (48 chamadas/grupo/dia, lotes pequenos), mas o primeiro grupo grande lê um backlog de até 12 lotes por rodada.
- Um import acima do que um passe lê (300 lotes) lê os mais recentes e deixa o resto sem leitura: a cobertura mostra incompleto e o raw só sai por expiração.
- `clamd` padrão rejeita stream acima de 25 MB (falha fechado). O nginx do host precisa liberar o caminho do import (ver ADR, addendum, item 9).
- Não construído: o gatilho de leitura por **menção ao número da empresa / palavras de pedido** (ADR §4) — hoje a leitura é por pedido explícito ou, com `OMNIRA_GROUP_AUTO_ANALYZE`, por volume/idade; tela de diff do reprocessamento; ferramenta do gateway de IA `group.memory.search` (existe a rota); memória por cliente/conta (ponto de extensão apenas).
- Mensagens de grupo de terceiros são tratadas como hostis (injeção de prompt): a defesa é estrutural (dado, nunca instrução; a IA só propõe; um humano aprova), não um filtro.

## Pendente do dono
1. Aprovar o visual do painel web antes de qualquer deploy.
2. Confirmar os padrões decididos (ADR, addendum, item 8).
3. Decidir quando ativar (ordem em `docs/ops/GROUP-COPILOT.md`) e liberar o limite de corpo no nginx do host.
4. Teste com um grupo real e um modelo real (Gemini) antes de ligar `OMNIRA_GROUP_AUTO_ANALYZE`.
