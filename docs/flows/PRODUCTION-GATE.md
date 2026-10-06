# Flow Builder — gate de produção (FLOW.12)

Data: 2026-10-06 · Branch `feat/flow-builder` (base `main` @ `8af6d54`; lista exata: `git log main..HEAD`) · ADR-0019.

## Decisão

**Estado: `LAB`.** O código está **completo e verificado em laboratório**, mas **não** está liberado para `INTERNAL_PILOT` nem
para produção. Nada foi enviado (`push`) nem etiquetado; **atenção ao banco vivo:** as migrations 082–084 foram aplicadas ao `omnira_dev` em 2026-10-06 07:54 (-04), **fora da sessão de desenvolvimento** (inertes: `OMNIRA_FLOWS_ENABLED=false`, 0 linhas em `flows`/`flow_runs`); a migration **085** (FLOW-003) existe no repositório mas **não foi aplicada** ao banco vivo. A feature segue desligada; nada foi (re)implantado por esta sessão. Nada foi etiquetado
(`tag`) ou mesclado. A flag `OMNIRA_FLOWS_ENABLED` vem **desligada**; desligada, o comportamento é o de hoje (provado abaixo).

Para subir a `INTERNAL_PILOT` faltam, no mínimo, os itens da seção **"Condições para o piloto"**. Esta página não substitui o
aceite humano.

## Evidência de verificação

| Verificação | Resultado |
|---|---|
| Go, banco **descartável**, `go test -p 1 ./...` | **69 pacotes ok, 5 falhos — os mesmos 5 da baseline de `main`** (abaixo) |
| Go, pacotes novos com `-race` (`flows`, `worker/flows`, `messages`) | **sem corrida** |
| Testes Go dos módulos desta entrega (`flows`, `worker/flows`, `messages/application`, `inbox/application`) | **117 PASS, 0 FAIL** |
| `go build ./...`, `go vet` dos pacotes novos, `gofmt -l` nos arquivos novos | limpos |
| Migrations 082–084 | `down` das três devolve o schema **idêntico** ao de 081 (0 linhas de diferença, 0 permissões sobrando) e o `up` seguinte reproduz o de 084 |
| `TestRLSCompleteness` / `TestRLSPolicyCoverage` sobre as 6 tabelas novas | passam |
| Contrato `contracts/openapi/flows-v1.yaml` × rotas | **23 operações**, deriva testada nos dois sentidos |
| Web: `tsc`, `vite build`, `vitest` | limpos; **614 passam**; 1 falha preexistente em `main` (`SettingsShell`) + 1 spec Playwright coletado pelo vitest (preexistente) |
| Navegador real (Chromium/Playwright, API mockada) | 2 cenários passam: monta, arrasta com o mouse real, liga, simula, salva, publica, conflito |
| Dependências novas (Go e JS) | **nenhuma** (`go.mod`, `go.sum`, `package.json`, `package-lock.json` intactos) |
| Merge com `main` (simulado com `git merge-tree`) | **sem conflito**; `main` não andou desde a base |
| Segredo literal no diff | nenhum (varredura das linhas adicionadas) |
| HTML injetado na UI / SQL por concatenação | nenhum |

**As 5 falhas preexistentes (todas já na baseline de `main`, nenhuma causada por esta feature):**
`internal/platform/authn`, `internal/platform/httpserver`, `internal/tenancy/adapters` — os **testes** não compilam (IAM5 mudou
assinaturas e os testes não acompanharam); `internal/intelligence/adapters TestQueriesStayFastOnABusyConversation` — limite de
tempo sensível à carga do host; `internal/outbox/adapters TestStoreUsesInjectedTransaction`. Duas quebras de **código de
produção** de `main` que impediam `go build ./...` foram consertadas em um commit isolado e descartável (`fix(build)`).

## Segurança — checklist com a prova de cada item

| Requisito | Prova |
|---|---|
| **Isolamento por tenant** (leitura, escrita, publicar, arquivar, runs, filas, templates) | `TestFlowTenantIsolation` (inclui RLS escondendo a linha **sem** filtro explícito); `TestHTTPRBACAndLifecycle` (tenant B: 404/403/lista vazia); `TestRunsAreTenantIsolated`; `TestCustomerCandidates…`; `TestHTTPRunsTimelineAndAnalytics`; `TestSystemSendEndToEnd` |
| **RLS efetiva** nas 6 tabelas (`ENABLE` + `FORCE` + políticas + grants mínimos) | migration 082 + `TestRLSCompleteness`; `flow_versions` e `flow_node_executions` sem `UPDATE/DELETE` para `omnira_app` |
| **Versão publicada imutável** | trigger no banco bloqueia até o dono (`TestPublishSnapshotsAreImmutable…`); rollback só move o ponteiro |
| **RBAC separado** (editar ≠ publicar; agent sem acesso) | `TestHTTPRBACAndLifecycle`, `TestHTTPSimulate…`, `TestHTTPTemplateLibrary…`, `TestHTTPRunsTimeline…`; matriz fixada em `internal/iam3` |
| **Tenant nunca vem do payload** | `TestHandlerTrustsOnlyPersistedState` (envelope com tenant forjado é ignorado); tenant só do `TenantContext`/conversa persistida |
| **Referência a recurso de outro tenant** (fila, linha de canal, empresa) | recusada ao salvar/publicar/instalar (`TestControlPlaneLifecycle…`, `TestSettingsRejectForeignChannelLines`, `TestInstallRefusesMissingAndForeignMappings…`, `ValidateCustomer`) |
| **Idempotência** (evento duplicado, redelivery, envio) | `TestDuplicateEventsDoNotDuplicateAnything`, `TestInboundJobDrivesTheFlowAndRedeliveryIsHarmless`, `TestSystemSendEndToEnd` (chave repetida = 1 mensagem; conteúdo diferente = recusa) |
| **Concorrência** (mesma conversa) | `TestConcurrentDeliveriesOfOneConversationAreSerialized` (8+8 entregas → 1 retomada, 1 run, exatamente uma mensagem por passo); **mutação** removendo o `FOR UPDATE` faz o teste falhar; passa 5/5 e com `-race`; 1 run ativo por conversa imposto pelo banco |
| **Nunca encalhar uma conversa** | todo fim sem handoff devolve à fila; sweeper libera retenção sem run, dispara timeouts e cancela runs de conversas fechadas (`TestSweeperTimeouts…`) |
| **Bot não fala sobre operador** | derivação `automation_mode='bot' E sem responsável` (`TestHumanTakeoverSilences…`, `TestSystemSendEndToEnd`) |
| **Janela de 24 h (Meta)** | `SystemSender` reutiliza `SessionWindow`; `TestSystemSendRespectsTheMetaWindow`; porta `window_closed` |
| **Nenhum texto/telefone/segredo na fila** | `TestSystemSendEndToEnd`, `TestGateOnInboundEnqueuesReferencesOnlyAndOnlyWhenRelevant` |
| **Segredos** (embutir, vazar) | validador rejeita chave/valor com cara de segredo; `Redact` mascara a credencial **dentro de qualquer texto**; trilha, API de runs, handoff, simulador e logs passam por ela (`TestSecretsNeverReachTheAuditTrail`, `TestAuditTrailIsRedactedInTheDatabase`, `TestHTTPRunsTimelineAndAnalytics`). Vazamento real achado e corrigido nesta rodada |
| **Falha do gate nunca perde mensagem** | `TestGateNeverBreaksTheIngestTransaction`; savepoint; sem gate = comportamento anterior (`TestIngestWithoutAGateIsUnchanged`, ingest real com a flag desligada) |
| **Instalação de template não publica nem vira dependência** | `TestInstallStarterPackCreatesOnlyTenantOwnedDrafts`, `…IsAllOrNothing` (falha injetada na 3ª instalação deixa 0 linhas e a sessão utilizável) |
| **Templates imutáveis** | hash fixado em `testdata/hashes.json`; mutação (editar um template publicado) quebra o build |
| **Severidade nunca vem da IA** | `TestAICannotSetSeverityOrQueue` (`priority` só aceita literal); `ai.*` reservado |
| **IA: saída do modelo estritamente validada** | `TestAIClassifyAcceptsOnlyListedIntents…` (mutação confirmada), `TestAIExtractKeepsOnlyValid…`, `TestAISummarizeIsBoundedAndRedacted` |
| **Injeção de prompt** | `TestAIPromptInjectionStaysDataNeverInstruction` (texto do cliente só como dado; entrada limitada) |
| **Limites contra abuso** | definição ≤ 1 MiB/200 nós/400 arestas; ≤ 1000 execuções por run (padrão 200); subflow ≤ 3 níveis; ≤ 5 chamadas de IA por run; simulador ≤ 30 eventos; sem ciclos (validador) |
| **Simulação sem efeito** | `TestSimulationWritesNothingToTheDatabase` (10 tabelas idênticas antes/depois, conversa real intacta); simulador nunca chama modelo |
| **Observabilidade sem vazamento** | métricas com rótulos limitados (nunca tenant/flow/conversa/run); logs só com ids |
| **Reversibilidade** | migrations aditivas e `down` provado; `OMNIRA_FLOWS_ENABLED=false` desliga tudo |

## O que NÃO foi feito (honesto)

- **Revisão do Codex (2026-10-06): executada, mas SÓ ESTÁTICA.** O sandbox do Codex bloqueou Docker (`bwrap: loopback: Failed RTM_NEWADDR`) e não há `go` no host: **nenhum teste, migration, RLS, concorrência ou redelivery foi verificado por ele**. Resultado: **0 CRITICAL, 0 HIGH, 2 MEDIUM, 2 LOW** (`git diff --check` limpo). Veredito dele: "NÃO BLOQUEIA por CRITICAL/HIGH confirmado em análise estática", piloto "não validado" até rodar os testes. Tratamento dos achados abaixo; a execução dos testes foi feita por mim (não pelo Codex) e **não substitui** uma rodada do Codex com acesso a Docker.

  | Achado | Sev. | Tratamento |
  |---|---|---|
  | FLOW-001 `ask`/`choice`/`customer_choice` sem saída `window_closed` | MEDIUM | **Corrigido**: portas opcionais `window_closed` e `error` nos três nós (servidor, executores, espelho no web); ids de opção reservados; `TestQuestionNodesRouteAClosedWindow` (falha sem a correção) |
  | FLOW-002 `down` da 083 apaga permissões preexistentes | MEDIUM | **Aceito, sem mudança**: mesmo padrão das migrations 063 e 073; `flow.*`/`flow_template.*`/`flow_run.*` são chaves novas, criadas só por esta migration. Reabrir se alguma outra migration passar a registrar essas chaves |
  | FLOW-003 `active_version_id` não amarra a versão ao mesmo `flow_id` | LOW | **Corrigido** pela migration **000085** (forward-only; a 082 já estava aplicada no `omnira_dev`, por isso não foi editada): `UNIQUE (tenant_id, flow_id, id)` e FK composta; `TestActiveVersionMustBelongToItsOwnFlow` (falha sem a correção) |
  | FLOW-004 gate enfileira job para toda mensagem com flow `always` | LOW | **Corrigido**: só conversas abertas, sem responsável, com contato e do tipo atendimento/não classificado (parte barata de `Startable`; conflito de identidade segue com o motor); teste cobre atribuída, fechada e não-cliente |

  **Rodadas A e B do Codex (2026-10-06, estáticas, commit `d28834f`)** cobriram o que faltava (migrations 082–085, `templates.go`, edições em código existente e todo o frontend): a 085 foi dada como OK e vieram 3 achados novos, todos verificados no código e corrigidos com testes que falham sem a correção:

  | Achado | Sev. | Tratamento |
  |---|---|---|
  | FLOW-201 `inbound.go`: `Engage` retém a conversa nova (`bot`), e se o enfileiramento do job falhar (só logado) ela fica sem job e sem fila até o sweeper (2 min) | MEDIUM | **Corrigido**: `OnInbound` informa falha e há `Gate.Release`; o ingest devolve a conversa nova ao roteamento padrão na hora (follow-up que falha não libera: já há run dono). Testes em `inbound_flows_test.go` e `gate_test.go` |
  | FLOW-301 `FlowEditorPage.tsx`: salvar configurações zerava `loadedFor`, e a refetch sobrescrevia as edições locais não salvas (perda silenciosa) | **HIGH** | **Corrigido**: configurações não recarregam o rascunho; além disso, revisão mais nova do servidor com edição local pendente vira conflito explícito (mantém as edições, "Recarregar" força a recarga). O teste de mutação ainda achou um defeito na minha 1ª versão (Recarregar não reaplicava dados idênticos): corrigido |
  | FLOW-302 saída do editor por link/barra lateral com alterações não salvas perdia o rascunho sem confirmação | MEDIUM | **Corrigido** com `UnsavedChangesGuard`. A limitação inicial (botão "voltar" do navegador, por causa do `BrowserRouter`) foi **eliminada em 2026-10-06** trocando o app para um router de dados (`createBrowserRouter` com o `<Routes>` atual sob uma rota curinga) e usando `useBlocker`: agora links, barra lateral, `navigate()` e voltar/avançar do navegador pedem confirmação; fechar/recarregar a aba segue coberto por `beforeunload`. Provado por teste unitário (router de dados em memória) e por cenário no Chromium real (voltar/avançar bloqueado e liberado) |

  **Passada de verificação do Codex (2026-10-06, commit `a17b69e`, estática, chamada pelo agente `codex:codex-rescue`):** FLOW-201 e FLOW-302 **OK**; achou **FLOW-401 (HIGH)** em cima da minha correção do FLOW-301: a checagem de edição não salva do efeito de carga ignorava o **nome** do fluxo, então uma edição só de nome era sobrescrita por uma revisão mais nova do servidor. **Corrigido** (estado `savedName` unifica `dirty` e a checagem do conflito; testes `… also protects a rename …` e `… freshly saved name …`, o primeiro falha sem a correção). **Falta uma última conferência do Codex só do FLOW-401**, e a correção ainda **não está implantada** (web em execução é `a17b69e`).

  **Segunda conferência do Codex (FLOW-401, estática, commit `e5287a6`):** FLOW-401 **OK** (sem HIGH/CRITICAL; veredito "não bloqueia (estático)"); achou **FLOW-501 (MEDIUM)**: ao salvar um nome em branco ou com espaços sobrando, o servidor aparava/mantinha o anterior mas o campo local continuava diferente e o editor seguia "sujo". **Corrigido** (o campo é alinhado ao nome que o servidor gravou, sem perder digitação feita durante o salvamento) com teste que falha sem a correção. Estas duas correções e a troca do router estão em `main`; **implantadas em 2026-10-06 por ordem do dono:** web `20-omnira-web:4305478` (rollback: `…:rollback-pre-router-20261006`), saudável em ~9 s, bundle `index-7BzclUyH.js`, links profundos do app respondem 200 pelo nginx, 0 erros nos logs, e um Chromium real contra as páginas públicas de produção (`/login`, `/privacidade` e redirecionamento de rota inexistente) renderizou o app sobre o router de dados com 0 erros de console. As telas autenticadas não foram exercidas em produção (exigem login); foram cobertas pelos 6 cenários de navegador contra o bundle compilado.

  Com isso a cobertura estática do Codex fica completa (rodada 1: itens 2–5 e parte do 1; A: migrations, `templates.go` e item 7; B: item 8). **Ainda falta** uma nova passada do Codex para conferir estas 3 correções, e a execução dos testes por ele (sandbox sem Docker). **Implantado em 2026-10-06 (~12:56 -04) por ordem do dono:** api (`20-omnira-api:a17b69e`) e web (`20-omnira-web:a17b69e`) reconstruídas e recriadas saudáveis, sem migration; rollback: tags `…:rollback-pre-codex-ab-20261006`. Flags continuam `false`; 0 erros nos logs; mensagem real recebida após a troca; asset servido mudou (`index-CqEvq-mR.js` → `index-D6Qxr3Ox.js`). O worker segue em `d12ed5d` (não é afetado por estas mudanças).

  Verificação após as correções: suíte Go completa em banco novo = 69 ok + as mesmas 5 falhas da baseline; migrations 084→082 e 082→084 sem erro; web 621 passam (mesmas 2 falhas de `main`); navegador real 5 passam; após FLOW-201/301/302: suíte Go completa 69 ok + mesmas 5 falhas, `-race` limpo em flows/inbox/worker, web 621 passam.
- **Sem E2E contra a stack Docker completa e sem teste com telefone real** (equivalente ao P7 do goal WAHA). O que existe:
  Postgres real, ingest real (`PostgresInboundStore` + `InboundService`), motor real, envio de sistema real **até o outbox**
  (o provedor não foi chamado) e navegador real contra API mockada.
- **Backup/restore e saúde:** os dados dos fluxos estão no mesmo PostgreSQL e entram no backup horário existente; **não** rodei um
  restore com dados de fluxo. `/healthz` não ganhou checagem específica (as métricas do runtime ganharam).
- **Frontend:** sem suporte a toque; testado com API mockada.
- **Não implementado (decisão consciente):** `ai_agent` (agente autônomo com ferramentas: seria a primeira capacidade da IA com
  efeito e exige ADR e modelo de permissões de ferramenta); diff visual entre versões; exportar/importar fluxo; marketplace;
  assistente de atualização de templates; integração de monitoramento (os modelos de ISP funcionam sem ela); criação de
  oportunidade no CRM (não há módulo; o lead vai para a fila comercial).
- **Risco de merge:** `web/src/App.tsx`, `Sidebar.tsx`, `permissions.ts` e `internal/iam3/security_test.go` são editados também
  pela frente RBAC/agentes (hunks pequenos, só adição). **Renumerar as migrations** se `main` ganhar uma `0000082+` antes do merge.

## Condições para o piloto (`INTERNAL_PILOT`)

1. Aceite humano documentado do dono.
2. Nova revisão do Codex **com acesso a Docker/banco descartável** (a de 2026-10-06 foi só estática), sem CRITICAL/HIGH, com a saída real anexada.
3. As migrations 082–084 já estão no `omnira_dev` (inertes). **085 validada em 2026-10-06 sobre um dump restaurado do `omnira_dev`** (up, down e up de novo sem erro; contagens de tenants/conversas/mensagens idênticas; dump apagado depois). **Aplicada ao `omnira_dev` em 2026-10-06 ~09:14 (-04)** por ordem do dono, com backup fresco antes (`omnira_dev_20261006T131315Z.dump`, local + externo + nuvem verificados). **api e worker reconstruídos do commit `d12ed5d`** (imagens `20-omnira-{api,worker}:d12ed5d`; rollback: `…:rollback-pre-flows-codex-fixes-20261006`) e reiniciados saudáveis em ~9 s; flags continuam `false`, sem erro nos logs, consumidores `worker-channel-send` e `worker-routing` com 0 pendentes e mensagem real recebida após o restart. A imagem `web` **não** foi reconstruída.
4. Build das imagens e subida com `OMNIRA_FLOWS_ENABLED=true` **em um tenant de teste**, com 1 flow simples publicado.
5. **Smoke com telefone real** (conversa nova → bot → resposta → handoff → fila → atendente assume → bot cala) nas duas linhas (WAHA e Meta, se houver).
6. Observar `/metrics` (`omnira_flow_*`) e o log do sweeper por 24 h supervisionadas antes de ampliar.
7. Decidir sobre os 5 testes quebrados de `main` (o `Modal` já foi corrigido).

## Rollback

`OMNIRA_FLOWS_ENABLED=false` e reiniciar API/worker: a feature para na hora e o ingest volta ao roteamento padrão. Dados e
migrations podem ficar (inertes). Para remover o schema: `down` de 084, 083, 082 (nessa ordem; testado).
