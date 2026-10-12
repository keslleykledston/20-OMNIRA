# Hub — pacote do gate formal (candidato) — **NÃO ASSINADO**

Estado do projeto: `LAB`. Este documento reúne as provas para o dono decidir se o Hub passa a `LIMITED_INTERNAL_PRODUCTION_CANDIDATE`. **Nada aqui declara produção pronta**; sem a assinatura da seção 5 o estado continua `LAB`. Evidência em `HUB-VERIFICATION-STATUS.md` (por data), `HUB-ACCEPTANCE-MATRIX.md` (40 critérios), `HUB-PRE-GATE-REPORT.md` (revisões do Codex), `HUB-ISOLATION-AUDIT.md`.

## 1. Critérios do gate (`CLAUDE.md` global) × situação

| Critério | Situação | Evidência |
|---|---|---|
| Testes PASS | **PASS** em 2026-10-12, commit `6d083a5` | integração 37/37 (`git archive`, `enforce`); Vitest 916/916; `tsc` limpo; E2E navegador real 14/14 + varredura de vazamento limpa; E2E com Keycloak 6/6; migrations 093–121 up/down/up idêntico; `test-hub-health-check` 13/13; `test-gate-clone-retry` 10/10; nginx do web streaming do sinal (`test-web-nginx-sse.sh`) |
| Revisão independente (Codex) | **FEITA, com achados tratados** | 3 camadas + seguimento; CRITICAL 0; todos os HIGH corrigidos, mitigados ou aceitos com motivo escrito (`HUB-PRE-GATE-REPORT.md`). Não é "zero HIGH": é "nenhum HIGH aberto sem decisão" |
| Backup/restore validado | **PASS** | `backup-restore-hub-check.sh`: backup real restaurado em cluster novo (~6 s) com isolamento conferido |
| Health/metrics | **PASS em LAB** | `/metrics` do api e worker, `hub-health-check` em cron `*/5` (último `OK`); alerta chegando ao celular **não provado** |
| Carga | **PASS em LAB** | 16 clientes, 20 mil itens, 0 falhas; lista 10k = 83 ms, 30k = 0,28 s |
| Aceite humano | **PENDENTE** | seção 5 |

## 2. Matriz de aceite (40 critérios)

28 VERIFIED · 9 PARTIAL · 3 NOT BUILT (base de conhecimento por tenant, painel de supervisão do Hub, pontuação de roteamento). Os PARTIAL são capacidade inexistente ou limite medido/aceito, listados um a um na matriz.

## 3. O que NÃO está provado (precisa de aparelho, provedor ou ação do dono)

- Uso por pessoa real do Hub em produção (`omnira_hub_events_streams_total{outcome="opened"}` ainda sem tráfego real) e login do Keycloak de **produção** com conta só-Hub.
- ERP ao vivo (cria chamado real e aviso de WhatsApp), WhatsApp com aparelho (anexos de saída, abertura/despedida), IA com provedor real e injeção de prompt contra modelo vivo (o que existe é determinístico: conteúdo hostil inerte, catálogo de ferramentas fechado, escrita da IA espera uma pessoa).
- Keycloak de produção: edição do próprio e-mail ligada (o código bloqueia o caso explorável, mas o desenho de "autorizar por e-mail" pressupõe edição só por `admin`).
- ACLs de Valkey e NATS, TLS/criptografia do JetStream, dono/modo do volume de mídia.
- Alerta chegando ao celular do dono.

## 4. Riscos aceitos (resumo; texto completo no relatório de pré-gate)

Revogação de tempo real limitada por ~15 s · contexto de sessão do banco depende do segredo `OMNIRA_SESSION_CTX_KEY` guardado no processo (ADR-0042) · chamada de IA delegada roda dentro da transação delegada · listas avaliam a política por linha (dezenas a centenas de ms em volumes altos).

## 5. Decisão do dono (preencher à mão)

- [ ] Li a seção 3 e aceito ou resolvo cada item antes de usar com cliente real.
- [ ] Autorizo o estado `LIMITED_INTERNAL_PRODUCTION_CANDIDATE` para o Hub (piloto interno supervisionado).
- [ ] Mantenho `LAB`.

Nome / data / assinatura: ______________________

Sem este bloco preenchido, nenhum documento deste repositório pode afirmar mais que `LAB`.
