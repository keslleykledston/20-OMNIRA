# ADR-0044 — relatório final da entrega (horários, plantões, transbordo e SLA)

Branch `feat/sector-scheduling-overflow`, base `c804344` (`main`). **Estado: LAB. IMPLANTADA em 2026-10-13 por ordem do dono (mesclada em `main` `1f30046`, enviada ao GitHub, flag LIGADA só para conversas novas; ver §7).** A flag `OMNIRA_ROUTING_ENGINE_ENABLED` não existe em produção. Vocabulário de prova: ver `ROUTING-ENGINE-ACCEPTANCE.md` (nenhuma linha é E2E).

## 1. O que foi entregue
| Fase | Entrega |
|---|---|
| Calendário | `internal/scheduling`: intervalos, turno que atravessa a meia-noite, feriado/exceção, suspensão de emergência, fuso IANA e horário de verão, horário útil |
| Setores e horários | migrations 124/126; API e telas de Setores e Horários; herança fila → setor → padrão da empresa; padrão do dono (todos 24x7 e no Suporte) |
| Escala, plantão, elegibilidade | migration 125; regra única (horário do setor ∧ escala ∧ presença ∧ capacidade) com os motivos de cada recusa |
| Motor central | migration 127; `Decide` com estado, decisões, timers duráveis (lease, *fencing*, equidade entre empresas), `least_loaded`; atrás da flag |
| Transbordo em cadeia | migrations 128/129/133; destinos fila, setor, plantão, atendente, Hub e fluxo; validação ao salvar e ao entrar; guarda de laços e limite de etapas |
| SLA | migration 130; 4 métricas, precedência fila > setor > empresa, tempo preservado na transferência, risco/violação uma vez |
| Aviso fora do expediente | migration 132; opt-in por grupo, uma vez por episódio |
| Flow Builder | 5 nós finos (consultar horário/disponibilidade, escolher e enviar a setor, aplicar transbordo); simulador com `SimRouting`; destino `flow` (`StartRouted`) |
| Segurança | `tenant_id` imutável (131); Hub não administra nada disso (rotas não delegáveis, 404); chaves não delegáveis; contratos do Hub sem operadores |
| Interface | Configurações › Atendimento (Setores, Horários, Escalas e plantões, Transbordo e SLA), painel “Roteamento e SLA” na conversa, “Menor carga” em Grupos |
| Operação | `docs/operations/ROUTING-ENGINE-OPERATIONS.md`; matriz de aceite; estado local (`ROUTING-ENGINE-STATE.md`) |

## 2. Verificação (evidência, não afirmação)
- **Gate de integração da branch: 39/39 pacotes** (Postgres real sob RLS, mode `enforce`), executado de `git archive` do commit `d41bdff` (último de código): **39/39 pacotes passaram**, 2026-10-13. Os commits seguintes são só documentação.
- Testes Go sem integração: todos `ok`. Web: `vitest` 958 testes, `tsc --noEmit`, `vite build` ok. Migrations 124–133: `scripts/test-hub-migrations.sh` (up/down/up idêntico), `tools/validate-schema.sh` ok. `tools/validate-specs.sh`: os mesmos 33 erros que já existem em `main` (nenhum novo).
- Mutantes: cada correção relevante tem um teste que falha quando a correção é desfeita (savepoints, guarda de fluxo, hand-off, SLA ao fechar, filtro de canal, `Startable`, rota que vira delegável…).
- **Codex (revisor independente, somente leitura)**: revisões por fase (0 CRITICAL; HIGH/MEDIUM tratados) + **revisão final da branch inteira (4 HIGH, 3 MEDIUM, 1 LOW — todos tratados)** + reconferência do delta (2 HIGH, tratados) + confirmação final: **nenhum CRITICAL/HIGH/MEDIUM/LOW** sobre `e5d29b9..f0ee674`. O savepoint de `tryAssign` (commit `d41bdff`) veio depois da última confirmação e é coberto por 60 repetições do teste de concorrência (antes: ~1 falha em 25).

## 3. O que a própria entrega achou de errado em si (e como foi provado)
Cinco falhas só aparecem **compondo** as peças; os testes por pacote estavam verdes:
1. **Decisões desfeitas em produção**: o handler do worker devolvia `ErrNoEligibleAgent` dentro da sessão, e o executor faz *rollback* em qualquer erro — então estado, decisão, timers, relógios de SLA e o aviso do motor eram desfeitos sempre que ninguém era atribuído. Achado por um teste ingest → job → handler → motor.
2. **Filas manuais nunca chegavam ao motor** (só `round_robin`/`least_loaded` recebiam o job), e as filas-padrão são manuais; e o motor ignorava o horário nelas. Aviso, SLA e transbordo não valeriam justamente nos setores padrão.
3. **Eventos de SLA sem fluxo de entrega**: tipos `routing.sla.*` não casavam com o único *stream* (`job.>`); a publicação falharia e travaria o ciclo do publicador. Renomeados para `job.routing.sla_*`.
4. **A flag não chegava aos contêineres** (o `docker-compose.yml` repassa variáveis explicitamente) e a API lia presença de ninguém no “quem pode atender agora”.
5. **Deadlock entre workers** (a ordem em que dois workers percorrem os atendentes): um candidato que falhava mantinha a trava da linha do atendente até o fim da transação. Corrigido com *savepoint* por candidato.
Além de uma corrida **pré-existente** no rodízio legado (capacidade contada antes da trava): documentada, não alterada (`ROUTING-ENGINE-STATE.md` §3b).

## 4. O que NÃO foi provado
Nenhum E2E em navegador, aparelho WhatsApp, ERP ou IA reais para este módulo; sem teste de carga com milhares de conversas; o aviso fora do expediente depende da janela de 24 h da Meta nos canais oficiais; tudo atrás da flag, desligada.

## 5. Pendências que dependem do dono
1. **Ordem de merge e de implantação** da branch (e de enviar a branch ao GitHub — não foi feito). Ao mesclar, confirmar que `main` não ganhou migrations 124+ (senão renumerar).
2. **Ligar `OMNIRA_ROUTING_ENGINE_ENABLED`** só depois, na janela combinada (guia §2) e decidir sobre a retroalimentação das conversas abertas (guia §6, escreve no banco).
3. Decisões do ADR §12 (seguem os padrões adotados): feriados manuais ou calendário nacional; fuso padrão (Manaus); limite de 4 etapas; conversa já atribuída ao fechar o expediente (nada muda); fluxo como destino só sem execução ativa (implementado).
4. **Capacidade padrão 5** dos atendentes no Suporte é palpite meu: confirmar ou ajustar.
5. Nomes reais dos setores por empresa e como “Servidores/Outsourcing TI/Monitoring” recebem trabalho (S1 do ADR-0043); escopo de leitura por setor (ADR-0043).
6. Teste real: um aparelho de teste para o aviso fora do expediente e um fluxo `check_schedule`/`select_sector` publicado numa empresa de teste.
7. Pendências antigas que continuam suas: assinatura do gate do Hub (`HUB-GATE-PACKAGE.md`), Keycloak (e-mail editável só admin), ACLs Valkey/NATS, volume de mídia, ADR-0041, Test Company sem fila padrão, logo/favicon.

## 7. Implantação (2026-10-13, ordem do dono: "faça o merge e implante… pode ligar a flag, as janelas antigas não entram só as novas")
- **Merge:** `main` = `1f30046` (merge `--no-ff` da branch; base `c804344`, sem divergência). Enviado ao GitHub (`c804344..1f30046`). Nenhuma tag.
- **Antes do merge** achado e corrigido (`8cb9329`): a varredura de recuperação puxaria para o motor as **46 conversas antigas, abertas e sem dono, das filas manuais** quando a flag ligasse, contra a ordem do dono. Agora ela só recupera conversa manual **criada a partir de `OMNIRA_ROUTING_ENGINE_SINCE`**; sem a variável, a recuperação fica desligada. Teste + mutante.
- **Gate:** 39/39 pacotes de integração sobre o commit mesclado (`1f30046`, de `git archive`), web 971 testes, `tsc`, `vite build`.
- **Implantação:** backup `omnira_dev_20261013T124937Z` (local, USB e nuvem, checksum conferido), tags `rollback-pre-engine-20261013-0849` (api, worker, web), imagens `sha-1f30046`, migrations 124–133 aplicadas (ledger 133), api + worker + web recriados; primeiro **com a flag desligada** (saudável), depois `OMNIRA_ROUTING_ENGINE_ENABLED=true` + `OMNIRA_ROUTING_ENGINE_SINCE=2026-10-13T12:50:53Z` em API e worker juntos.
- **Verificado depois de ligar:** containers saudáveis; `routing_engine_state` = 0, timers = 0, nenhum job `job.routing.*` e nenhuma conversa com `routing_retry_at` (as 46 antigas **não** entraram); sem erro nos logs de api/worker.
- **Ainda não provado em produção:** o caminho de uma conversa NOVA de ponta a ponta (cliente real/aparelho de teste → motor), aviso fora do expediente, SLA e transbordo. Isso exige o aparelho de teste do dono (pendência 6 do §5).
- **Reverter:** `OMNIRA_ROUTING_ENGINE_ENABLED=false` (e remover o `SINCE`), recriar api+worker; imagens/rollback pelas tags acima.
