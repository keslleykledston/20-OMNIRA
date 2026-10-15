# Headless — relatório por fase (H0 a H6)

Branch `feat/headless-crm-dual-operation` (worktree isolado, base `main@88e1828`). **Nada foi enviado, mesclado, implantado nem aplicado em banco real.** Migrations 135–139 só em Postgres descartável.
Vocabulário de estado: IMPLEMENTED · NOT WIRED · UNIT / POSTGRES / HTTP / E2E VERIFIED · BLOCKED. Estado de produção: **LAB** (nenhuma declaração de pronto para produção).

Convenções de evidência: «gate» = `scripts/test-integration.sh` (Postgres + NATS descartáveis, papel `omnira_app` sem privilégio de dono); «mutantes» = `scripts/test-headless-mutations.sh <fase>` (cada mutante recompila/reaplica e **precisa** derrubar ao menos um teste; sobrevivente = defeito de teste); «Codex» = revisão independente somente-leitura, CRITICAL/HIGH bloqueiam.

## H0 — Descoberta, ADR e modelo de ameaça — `8202064`
- ADR-0046 (controle tipado em `conversations` com gatilho funil, `assigned_to_user_id` como projeção humana, `control_version` monotônica), inventário (leitores/escritores de `assigned_to_user_id`, envios, flows, Hub, realtime), modelo de ameaça.
- Codex H0: 2 CRITICAL + 4 HIGH (entre eles: `RETURN NULL` descartaria escritas legítimas; o gatilho não distingue `tenant_user` de `hub_user`; TOCTOU de revogação de credencial; leitura direta das tabelas de instalação). **Todos incorporados ao desenho** antes de codar (falha ruidosa `OM00x` no lugar de descarte; escritores legados derivam o controle; recheck cobre instalação + credencial + empresa; portas `SECURITY DEFINER`).

## H1 — Identidade da aplicação externa — `d58e803`, `b729e49`
- Migration 135: instalações por empresa, credenciais (token opaco, só digest), allow-lists de fila/linha, portas `SECURITY DEFINER` (`external_authenticate`, `external_recheck`, `external_installation_allowlists`).
- API: `GET /external/me`, administração (criar/listar/atualizar/suspender/reativar/revogar/emitir e revogar credencial) para pessoa com `integration.manage`, nunca delegável ao Hub.
- Codex H1: 2 HIGH (suspensão da empresa não serializada com o recheck; expiração só avaliada no início) + 3 MEDIUM (throttle atrás do nginx; teste de admin sem CSRF; contrato sem cobertura) + 1 LOW (correlação de auditoria) — **corrigidos** (travas, `clock_timestamp()`, `X-Real-IP` só de proxy privado, testes via `Server` real, correlation id).
- POSTGRES/HTTP VERIFIED; mutantes `h1` mortos.

## H2 — Controle único e tipado — `7499f21` (+ correções `78f4c8c`)
- Migration 136: `control_kind/holder/version/since/origin`, histórico append-only `conversation_control_events`, gatilho-guarda (falha ruidosa; capacidade por transação), `messages.sent_by_installation_id`.
- Serviço canônico `internal/conversationcontrol` (claim, release, transfer, takeover de supervisor, drain na revogação, release no fechamento); todos os escritores legados (roteador, Hub, flows, atendimento, fila) respeitam o titular externo.
- Corridas: 2/10/100 reivindicantes simultâneos → exatamente um titular; nenhum caminho legado rouba nem fala por cima.
- POSTGRES VERIFIED; mutantes `h2` mortos (inclui capacidade, histórico forjável, dequeue do Hub, efeitos de Flow).

## H3 — API de leitura — `7499f21`
- `GET /external/conversations{,/{id}{,/messages}}`, `/contacts/{id}`, `/media/{id}`; **uma** regra de visibilidade (mesma empresa, nem equipe nem contato interno/spam, allow-lists); paginação por cursor; mídia só `clean`, `nosniff`, tipos não inline como anexo; sem nome de pessoa.
- Codex H2+H3: 2 HIGH (gatilho como fronteira de autorização; id de mídia) + 5 MEDIUM + 1 LOW — **corrigidos** (capacidade `OM004` por transação, histórico só por função, checagem do id da mídia, efeitos de Flow, dequeue do Hub, ordem de locks testada, mutação default). Residual aceito: backfill da 136 não é em lotes (anotado no runbook).
- HTTP/POSTGRES VERIFIED; mutantes `h3` mortos.

## H4 — Journal durável, eventos e webhooks — `78f4c8c`, `1ea072d`, `0d8dca8`, `+ trigger`
- Migrations 137 (journal por empresa com sequenciador de marca-d'água por `xid`, sem lock nos escritores; expurgo por prefixo; `min_seq`) e 138 (assinaturas/entregas, funções `SECURITY DEFINER`, segredos selados AES-GCM).
- `GET /external/sync/checkpoint`, `GET /external/events?after=` (410 `resync_required`, visibilidade reavaliada **na leitura**, sem conteúdo no envelope); webhooks HMAC-SHA256 `v1=` sobre `timestamp.corpo-bruto` (duas assinaturas na rotação), backoff até 1 h ±20 %, janela 72 h, fila de erros, reenvio, cliente anti-SSRF (IP/porta verificados a cada conexão, sem redirect), 5 endpoints por instalação.
- Codex H4: 4 HIGH (expurgo cortaria fato recente por numeração de `xid`; ordem de locks assinatura/entrega; entrega órfã após expurgo; pausa ignorada após o claim) + 4 MEDIUM (fencing de tentativa; limite de 5 em corrida; entrega serial; checkpoint «mesma transação» no ADR) + 2 LOW — **todos corrigidos**; ADR/OpenAPI alinhados (o bootstrap converge por desenho, provado com dados mudando durante a paginação).
- Defeito descoberto depois, pelo E2E (não pelo Codex): o gatilho de `conversation.control_changed` não disparava para escritor legado (o guarda move a versão num gatilho BEFORE e `UPDATE OF` só olha o `SET`); a atribuição feita pelo Workspace não gerava fato. **Corrigido** (`AFTER UPDATE ... WHEN`) com teste dedicado e mutante.
- Portão H4 (cenários do prompt): CRM fora 3 h e recupera tudo com os mesmos ids (`TestWebhookReceiverOfflineForHours…`); duplicata não cria fato duplicado; A não recebe B; revogação durante entrega; rebinding/redirect/IP privado; assinatura inválida/replay; `410`; bootstrap concorrente; sem PII no envelope (teste de contrato). Lag/volume em `HEADLESS-BENCHMARK.md`.
- POSTGRES/HTTP VERIFIED; mutantes `h4` mortos (≈50).

## H5 — Escrita coordenada — `943909a`, `7af18b4`
- Migration 139 (recibos append-only, leases, fechamento por instalação); `POST …/{claim,release,transfer,messages,finalize,heartbeat}` com `Idempotency-Key` (escopo tenant+instalação+operação+chave, hash canônico), `If-Match "control-N"` (claim sem ele quando livre; claim repetido pelo titular = 200 idempotente), `412/409/403/404/422/428`, `202` = só enfileirado.
- Reusa o envio do Workspace (janela 24 h, template aprovado, job de entrega) com `INSERT` que prova titular+versão+janela+template na própria instrução; `finalize` com a mesma finalização do atendimento atribuída à instalação; transferência para fila ou pessoa elegível (mesmas travas do roteador); `claim` obedece ao calendário da fila; lease opcional com heartbeat e varredura.
- Codex H5: 5 HIGH + 4 MEDIUM + 1 LOW (TOCTOU de visibilidade, replay após mudança de visibilidade, corrida de capacidade, template/janela no insert, lease NOT WIRED, rótulo `external_agent_id`, 403 de fila, JSON com 2 documentos) — **todos corrigidos**, cada um com teste de regressão e mutante.
- Matriz de corridas (HTTP+Postgres): CRM × Workspace × roteador × transferência × fechamento × takeover × revogação; 10 requisições idênticas simultâneas = um efeito; mesma chave em duas conversas = um vencedor.
- NOT WIRED: anexos de saída pelo CRM; identidade humana verificada (`crm_agent_verified`).
- POSTGRES/HTTP VERIFIED; mutantes `h5` mortos (≈45).

## H6 — UX, administração, simulador, operação — `f93ee4d` + este conjunto
- UI: Configurações → Integrações → Aplicações externas (instalar/suspender/reativar/revogar, escopos, filas/linhas, tokens e segredos exibidos uma vez, endpoints, entregas, fila de erros, reenvio); Inbox mostra o titular tipado (Workspace/Hub/CRM), não oferece assumir/responder sobre o CRM, «Atualizar conversa» após 409/412, takeover do supervisor com motivo e confirmação. vitest: 990 testes e build de produção passando.
- Simulador de referência (`internal/external/crmsim` + `apps/crm-sim`, só biblioteca padrão e o contrato HTTP): autentica, faz bootstrap sem lacunas, recebe webhooks verificando HMAC e deduplicando, repete comandos com a **mesma chave** após resposta perdida, trata 409/412 relendo.
- E2E (`TestE2EWorkspaceAndTheReferenceCRMWorkTheSameCompany`): Workspace real (handlers HTTP/uso de caso reais) e simulador no mesmo tenant: disputa de conversa, recusa mútua, resposta com resposta perdida, takeover, CRM fora do ar por horas e recuperação, fora de horário, conversa invisível, token só-leitura, suspensão.
- Contratos: OpenAPI externo e de administração, AsyncAPI, JSON Schema, amostras e assinatura verificável (teste de contrato roda sem banco).
- Operação: flags `OFF` no compose, runbook (`docs/ops/HEADLESS-RUNBOOK.md`), benchmark (`HEADLESS-BENCHMARK.md`), métricas.
- Revisão final transversal do Codex (todas as fases): 2 HIGH (mensagens já enfileiradas saíam depois da perda do controle; holds órfãos após reduzir escopo/allow-list ou perder o último token) + 4 MEDIUM + 1 LOW. **Corrigidos**: `failed/control_lost` nas perdas involuntárias, `DrainOutOfScope`, drenagem ao revogar o último token e varredura de holds sem credencial (`no_credential`); documentação corrigida (flag desligada não é «byte a byte»; semântica do que está em voo; rate limit por instância; M-4 pré-existente).
- **Não verificado / bloqueado**: envio por WhatsApp real (**REAL-PROVIDER BLOCKED**, sem número de teste autorizado); navegador real (Playwright) — a UI foi provada por vitest, não por E2E de navegador; Hub dentro do E2E (coberto pelos testes de H2 e de Hub, não repetido no E2E).
