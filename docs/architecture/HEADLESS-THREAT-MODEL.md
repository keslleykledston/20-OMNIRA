# Headless — threat model (STRIDE + OWASP API Top 10)

Escopo: superfície `/api/v1/external/*`, administração de instalações, journal, webhooks de saída, controle de conversa. ADR: `docs/adr/0046-headless-hybrid-crm-dual-operation.md`.
Ativos: conversas/mensagens/mídia/contatos do tenant; titularidade (quem pode falar ao cliente); segredos (credencial de instalação, segredo de webhook); isolamento entre tenants.
Atores: CRM legítimo; CRM comprometido/malicioso; tenant vizinho com credencial própria; agente do Workspace/Hub; atacante de rede; operador interno.

## Fronteiras de confiança
1. Internet → `/api/v1/external/*` (token de integração) — não confiável.
2. API → PostgreSQL (`omnira_app`, `sessionctx=enforce`, RLS FORCE) — a RLS não protege contra SQL arbitrário (ADR-0042) nem restringe a sessão de sistema a um tenant (**R-1**).
3. Worker → URL do CRM (HTTPS externo) — destino controlado pelo tenant ⇒ SSRF.
4. Navegador do admin → rotas de administração (cookie + same-origin).

## STRIDE
| # | Ameaça | Vetor | Controle | Teste (gate) |
|---|---|---|---|---|
| S1 | Spoofing: usar token de outra instalação | token roubado/adivinhado | segredo de 256 bits, só digest no banco, comparação em tempo constante, expiração, rotação, revogação imediata, `last_used_at`; rate limit por instalação e por IP antes da autenticação | H1: token inexistente/expirado/revogado/suspenso |
| S2 | Spoofing: forjar tenant | `tenant_id` em path/query/header/corpo | tenant só da instalação; campos desconhecidos → 400; `?tenant_id` → 400; header `X-Tenant-ID`/`X-Omnira-Acting-As` ignorado e contado | H1/H3: manipulação de header e path |
| S3 | Spoofing: agente humano falso | `external_agent_id` arbitrário | tratado como metadado `unverified`; autoria = instalação | H5 |
| T1 | Tampering: dois titulares | corrida claim/send/transfer | trava `FOR UPDATE` + `control_version` + gatilho funil + CHECK | H2/H5: 2/10/100 competidores |
| T2 | Tampering: envio com versão velha | CRM desatualizado | `If-Match` sob trava → 412 sem job | H5 |
| T3 | Tampering: roubo por motor/fluxo | wakeup/timer/bot | `control_kind IS NOT NULL` ⇒ ocupada; gatilho descarta UPDATE legado; epoch cancela timer antigo | H2 |
| T4 | Tampering: webhook adulterado/replay | rede | HMAC `timestamp.raw_body`, janela de timestamp documentada, `delivery_id` único para dedupe do consumidor | H4 |
| R1 | Repudiation: "quem enviou?" | ação de CRM | `conversation_control_events` append-only com ator tipado; `external_command_receipts`; auditoria de administração; `request_id` correlacionado | H5/H6 |
| I1 | Information disclosure: cross-tenant | IDOR por UUID, cursor, mídia | 404 uniforme; predicados `tenant_id`; FKs compostas; lint de SQL; cursor é inteiro por tenant; mídia revalidada na leitura | H1/H3/H4 (tenants A/B/C) |
| I2 | Disclosure: conversa interna/spam | listagens, busca, eventos, mídia | exclusão por `conversation_kind='internal'` e `contacts.kind='internal'` em **toda** leitura e emissão de evento | H3/H4 |
| I3 | Disclosure: PII em webhook/log | envelope, logs, métricas | envelope só referências; logs/métricas sem texto/telefone/token; labels de baixa cardinalidade | H4/H6 |
| I4 | Disclosure: URL do provedor | mídia | nunca expor URL/objeto do provedor; download via API autenticada com limite de tamanho | H3 |
| D1 | DoS: flood de leitura/escrita | CRM mal comportado | quota e rate limit por instalação, `429` + `Retry-After`, limites de página/corpo, timeouts | H1/H6 |
| D2 | DoS: hotspot do cursor | muitos fatos num tenant | medição; sequência por tenant com trava curta; plano de escape | H6 benchmark |
| D3 | DoS: webhook lento prende worker | CRM travado | timeout por entrega, `SKIP LOCKED`, justiça por assinatura, DLQ | H4 |
| E1 | Elevation: SSRF | URL de webhook | HTTPS, portas, IP verificado a cada conexão (anti-rebinding), sem redirect, bloqueio de metadados/privado | H4 |
| E2 | Elevation: aplicação vira admin/usuário | contexto de sessão | sem `users`/membership/`is_system_admin` para aplicação; `integration.manage` só admin, não delegável | H1 |
| E3 | Elevation: agente do Hub administra credenciais | delegação | rotas de administração fora da lista delegável (`delegable_routes_test`) | H1 |
| E4 | Elevation: revogação em corrida | comando em voo | recheck da instalação `FOR SHARE` na mesma transação; revogação toma `FOR UPDATE` | H1/H5 |

## OWASP API Security Top 10 (2023)
| API | Risco | Tratamento |
|---|---|---|
| API1 BOLA | acesso a objeto de outro tenant/instalação | 404 uniforme + predicado tenant + allowlist de fila/canal na instalação |
| API2 Broken Auth | credenciais | §S1; sem credencial em query; Bearer apenas |
| API3 BOPLA | campos a mais/menos | DTOs externos minimalistas; campos desconhecidos rejeitados; sem `assigned_to_user_id` nem e-mail de agente |
| API4 Resource consumption | quotas | rate limit, paginação máxima, corpo máximo, mídia máxima, timeouts |
| API5 BFLA | função administrativa | escopos por rota; administração só por sessão humana com `integration.manage` |
| API6 Sensitive business flows | claim/send em massa | idempotência, rate limit, auditoria, `If-Match` |
| API7 SSRF | webhook | §E1 |
| API8 Misconfiguration | CORS/headers/erros | sem CORS aberto na superfície externa; `problem+json` sem stack; `Cache-Control: no-store` |
| API9 Improper inventory | rotas não documentadas | teste de contrato rota↔OpenAPI (gate 20); flag OFF por padrão |
| API10 Unsafe consumption | respostas do CRM | status/tamanho/tempo limitados; corpo da resposta não é lido além do necessário nem logado |

## Riscos residuais (aceitos, com dono e mitigação)
- **R-1 — sessão de sistema sem tenant no banco.** A API externa executa em `WithSystemTenantSession` (padrão do Hub, ADR-0037). Um erro de predicado cruzaria tenants *dentro do código externo*. Mitigação: SQL confinado a `internal/external/adapters`, lint determinístico, testes A/B/C por rota, recheck da instalação, mutação dos predicados. Evolução: contexto assinado `b2` com tenant (ADR futuro).
- **R-2 — chave de `OMNIRA_CREDENTIALS_KEY` comprometida** abre segredos de webhook; rotação documentada no runbook.
- **R-3 — cursor por tenant serializa commits** que geram fatos; medido e documentado; ver ADR §9.
- **R-4 — CRM comprometido** com credencial válida pode ler/agir dentro do escopo concedido: escopos mínimos, allowlist, revogação e `drain`, auditoria e alertas de volume anômalo.
- **R-5 — sem verificação humana do agente do CRM** (H7): autoria é a aplicação.

## Fora de escopo
Federação OIDC de agente CRM, OAuth2 Client Credentials, SDKs, exactly-once, acesso cross-tenant.
