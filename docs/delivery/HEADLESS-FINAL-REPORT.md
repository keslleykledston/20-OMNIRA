# Headless — relatório final

**Estado: READY FOR SANDBOX PILOT** (laboratório), com **um gate externo BLOCKED**: o envio por WhatsApp real não foi exercido (ver §6). Nada é declarado pronto para produção. Produção, push, merge e deploy: **não ordenados e não feitos**.
Branch `feat/headless-crm-dual-operation`, worktree `/data/cafegpt/tmp/wt-headless`, base `main@88e1828`. Migrations 137–141 só em Postgres descartável.

## 1. O que foi entregue (H0–H6)
| Fase | Resumo | Commits |
|---|---|---|
| H0 | ADR-0048, inventário, modelo de ameaça | 8202064 |
| H1 | Identidade da aplicação (instalação, token opaco, escopos, filas/linhas, recheck na transação) | d58e803, b729e49 |
| H2 | Controle único e tipado da conversa (`control_*`, histórico, gatilho-guarda, serviço canônico, escritores legados respeitam o CRM) | 7499f21 |
| H3 | API de leitura (conversas, mensagens, contatos, mídia) com uma regra de visibilidade | 7499f21 |
| H4 | Journal durável (sequenciador por marca-d'água), eventos, checkpoint, webhooks HMAC com anti-SSRF, retry 72 h, fila de erros | 78f4c8c, 1ea072d, 0d8dca8 |
| H5 | Escrita: claim/release/transfer/messages/finalize/heartbeat com `Idempotency-Key`, `If-Match`, recibos, lease | 943909a, 7af18b4 |
| H6 | UI (Aplicações externas + Inbox), CRM de referência, E2E, contratos, runbook, benchmark, roteiro do teste real | f93ee4d, a509d61, 12d6ac1, 471c2fa, + correções finais |

## 2. Contratos e rotas
- **Externas (token Bearer)**: `GET /api/v1/external/{me,conversations,conversations/{id},conversations/{id}/messages,contacts/{id},media/{id},sync/checkpoint,events}`; `POST /api/v1/external/conversations/{id}/{claim,release,transfer,messages,finalize,heartbeat}`.
- **Administração (pessoa, `integration.read|manage`)**: `/api/v1/tenants/{t}/integrations/apps[...]` (instalações, credenciais, webhooks, entregas, reenvio) e `POST …/inbox/conversations/{id}/takeover` (`conversation.takeover_external`).
- **Contratos**: `contracts/openapi/omnira-external-v1.yaml`, `omnira-v1.yaml` (ExternalApps), `contracts/asyncapi/omnira-external-events-v1.yaml`, `contracts/jsonschema/external-event.v1.schema.json`, amostras verificáveis em `contracts/samples/external/` (inclui um webhook assinado). Testes de contrato nos dois sentidos (rota ↔ documento) e de amostras.
- **Flags** (compose, todas OFF): `OMNIRA_HEADLESS_ENABLED`, `OMNIRA_HEADLESS_WRITE_ENABLED`, `OMNIRA_HEADLESS_EVENT_RETENTION_DAYS=30`, `OMNIRA_HEADLESS_WEBHOOK_RETRY_HOURS=72`.
- Docs: `docs/adr/0048-…`, `docs/ops/HEADLESS-RUNBOOK.md`, `docs/ops/HEADLESS-LIVE-TEST.md`, `docs/delivery/HEADLESS-PHASE-REPORT.md`, `HEADLESS-BENCHMARK.md`, `HEADLESS-PROGRESS.md`, `docs/architecture/HEADLESS-{INVENTORY,THREAT-MODEL}.md`.

## 3. Evidência (rodada, não declarada)
- **Suíte completa de integração** (`scripts/test-integration.sh` sem argumentos, Postgres+NATS descartáveis, papel `omnira_app`): **41 pacotes verdes** na execução final, sobre o commit `43f6a82` (inclui as correções da revisão final).
- **Web**: `vitest` 96 arquivos / 990 testes e `vite build` passando; `tsc --noEmit` limpo.
- **Mutação**: `scripts/test-headless-mutations.sh h1 … h5` — ~200 mutantes, todos mortos (cada sobrevivente virou teste novo ou código redundante removido).
- **Roundtrip** `up → down → up` com schema idêntico: 135, 136, 137, 138, 139.
- **E2E**: Workspace real + simulador de CRM no mesmo tenant (disputa, recusa mútua, resposta perdida e repetida, takeover, CRM fora do ar por horas e recuperação, fora de horário, conversa invisível, token só-leitura, suspensão).
- **Benchmark 30 000 conversas**: lista 17 ms/página (era 1,4 s sem índice), journal 8 400 fatos/s, fan-out 32 000/s, 133 pares claim+send/s com p95 61 ms, **0 deadlock**. Detalhes e limites em `HEADLESS-BENCHMARK.md`.
- **Revisão independente (Codex, somente leitura)**: H0, H1, H2+H3, H4, H5 e a revisão final transversal. Todo CRITICAL/HIGH foi corrigido com teste de regressão e mutante (contagens por fase em `HEADLESS-PHASE-REPORT.md`).

## 4. Defeitos que a própria validação revelou (corrigidos)
Gatilho que descartaria escritas legítimas (`RETURN NULL`) → falha ruidosa; capacidade de escrever controle forjável por `current_user` → `session_user` + capacidade por transação; ordem de locks assinatura/entrega; expurgo que apagaria fato recente; revogação que podia ser adiada indefinidamente por escritas sobrepostas (portão advisory justo); gatilho de `control_changed` que não disparava para escritor legado (achado pelo E2E); mensagens do CRM já enfileiradas que saíam após o takeover; holds órfãos após reduzir escopo/allow-list ou perder o último token; lista sem índice (1,4 s/página).

## 5. Riscos e limites declarados
- **NOT WIRED**: anexos de saída pelo CRM; identidade humana verificada do operador do CRM (`crm_agent_verified`; `external_agent_id` é rótulo não verificado); mutação de chamado ERP; HA/distribuído; H7.
- **REAL-PROVIDER BLOCKED**: nenhum envio por WhatsApp real foi feito. Roteiro pronto e isolado em `docs/ops/HEADLESS-LIVE-TEST.md` + `scripts/headless-lab-stack.sh` (compose próprio, banco descartável, portas 4xxxx); número de teste autorizado pelo dono: +55 92 99188-2864 (destinatário). Falta alguém conectar um remetente de teste por QR.
- Sem navegador real (Playwright): a UI foi provada por vitest. O Hub não está dentro do E2E (coberto nos testes de H2/Hub).
- Com as flags desligadas as rotas e o worker ficam inertes, mas o **banco** já tem os gatilhos (guard de `conversations` e produtores do journal): custo pequeno por escrita, não «byte a byte igual» (runbook §1).
- Migration 138 não faz backfill em lotes (≈170 conversas em produção; fazer fora de horário se a tabela for muito maior).
- Rate limit por instância da API; suspensão de empresa inteira espera a escrita externa em andamento; o worker de canal segura o `FOR UPDATE` da mensagem durante a chamada ao provedor (pré-existente).
- Quem gerencia aplicações precisa também de `conversation.takeover_external` (o `tenant_admin` tem).

## 6. Como seguir (nada disto foi executado)
1. **Revisão humana** do diff (`git diff main..feat/headless-crm-dual-operation`, ~125 arquivos) e do ADR-0048.
2. **Teste real** com o roteiro `HEADLESS-LIVE-TEST.md` (muda o gate externo de BLOCKED para verificado).
3. **Implantação** (somente com ordem expressa): backup; `migrate` aplica 137–141; build de api/worker/web a partir de worktree limpo; **flags continuam OFF**; ligar `OMNIRA_HEADLESS_ENABLED` e só depois `…_WRITE_ENABLED` no piloto de uma empresa/um CRM/uma linha, com os critérios de abortar do runbook §3.
4. **Reversão**: desligar as flags (WRITE, suspender aplicações, ENABLED) sem perder dados; `down` das migrations só com backup e aprovação (perde as tabelas novas; a 137 recusa com instalação viva).
5. Push/merge: decisão do dono.
