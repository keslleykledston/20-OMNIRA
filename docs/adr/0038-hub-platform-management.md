# ADR-0038 — Gestão de empresas, integrações e atendentes pelo Hub (PROPOSTO)

Status: **ACEITO (direção e decisões da §9); fase 0 IMPLEMENTADA e POSTGRES VERIFIED em 2026-10-08; fase 1 (criar empresa, suspender, capacidades) IMPLEMENTADA e POSTGRES/HTTP VERIFIED em 2026-10-08; fases 2–5 NOT WIRED.**
Contexto anterior: ADR-0036 (Hub dentro do OMNIRA), ADR-0037 (assumir e responder pelo Hub).
Vocabulário de evidência: tudo abaixo é desenho (`NOT WIRED`) até o gate de cada fase passar.

## 1. Problema
Hoje o Hub só atende (ver conversas, assumir, responder). Tudo o que antecede o atendimento é feito fora dele:
- criar uma empresa (tenant): **SQL manual** (não existe API nem tela);
- cadastrar canal, ERP/CRM, integrações: telas por tenant (`/channels`, `/settings`), só para **membros** daquele tenant;
- escolher quem atende cada empresa: `omnira-hubctl` no servidor.

O operador de BPO precisa gerir **todas** as empresas e seus agentes num só lugar, sem virar membro de cada tenant e sem que o navegador decida qual empresa está sendo alterada.

## 1.1 Decisão de direção (dono, 2026-10-08): o Hub é o plano de controle
O Hub é o ponto central para **criar empresas, gerir permissões e habilitar/desabilitar empresas e capacidades**. O workspace do tenant continua sendo onde a empresa *opera*; o Hub passa a ser onde a empresa *nasce, é configurada e é ligada/desligada*.

Opções avaliadas:
| | Opção | Veredito |
|---|---|---|
| A | **Hub como plano de controle** acima dos tenants (operador de plataforma → Hub → empresas); empresas sem Hub (as atuais) continuam existindo e podem ser vinculadas | **Recomendada** |
| B | App/painel de plataforma separado do Hub | Rejeitada: duplica auth, navegação e auditoria; contraria ADR-0036 |
| C | Manter CLI + telas locais de cada tenant | Rejeitada: não escala para BPO, sem visão única |

### Habilitar/desabilitar (duas alavancas, ambas só do plano de controle)
1. **Empresa**: `active | suspended`. Suspensa = ninguém atende, entradas de webhook são recusadas/ignoradas, grants e contrato ficam guardados (reversível). **Achado atual:** o caminho direto do tenant já recusa tenant inativo (`tenancy/application/authorization.go:69`), mas `has_active_hub_access` (migration 094) **não consulta `tenants.status`** — hoje uma empresa suspensa ainda seria atendida pelo Hub. A fase 1 corrige isso na própria função SQL (leitura, resposta e gestão) com mutante dedicado.
2. **Capacidades por empresa** (`tenant_entitlements`: canal WhatsApp, ERP/CRM, flows, IA, anexos de saída, gestão pelo Hub…): tabela nova, escrita só pelo plano de controle, **aplicada no servidor** (a UI apenas esconde). Hoje só existem flags globais do ambiente (`OMNIRA_*_ENABLED`); não há flag por empresa. Desligar uma capacidade nega a operação (403 uniforme) e não apaga dados.
Toda mudança é auditada com operador, empresa, antes/depois.

## 2. O que já existe e será reaproveitado (PRESERVE/EXTEND)
| Peça | Onde | Uso |
|---|---|---|
| Conexões de canal provider-neutral (WhatsApp WAHA/Meta, ERP `k3g_crm`) | `POST/GET /api/v1/tenants/{id}/channels/connections`, `internal/channels` | etapa "Canal" e "ERP/CRM" |
| Wizard WAHA, QR, teste de conexão, templates | `WahaWizardPage`, `ChannelsPage`, `.../session/start`, `.../qr`, `.../test` | reaproveitar a UI, parametrizando o escopo |
| Credenciais cifradas (ADR-0009) | `internal/channels` | segredo nunca volta para o cliente nem para auditoria |
| Convites, equipe, papéis, filas | `/team/invitations`, `/roles`, `/queues` | primeiro admin do tenant e filas |
| Hub: membros, contratos, grants (`can_reply`), `service_scope.queue_ids`, work_pools, skills | migrations 093–097, `hubctl` | modelo de "quem atende o quê" |
| Auditoria transacional | `audit_events` | toda ação de gestão |

## 3. Princípios
1. **O servidor decide a empresa.** Rotas do Hub carregam `hub_id` + `tenant_id` no caminho, mas o `tenant_id` só vale depois de hub → contrato vivo → capacidade de gestão (§5). Nada vem do corpo nem de estado do cliente.
2. **Gestão é outra capacidade, separada de atender.** Quem responde conversas não ganha poder de mexer em canal/ERP.
3. **Sem política de escrita nova para delegados.** Igual ao ADR-0037: autorizar na sessão RLS do operador, executar numa sessão de sistema do tenant lido do banco, revalidando na mesma transação.
4. **Segredo entra, nunca sai.** Token de ERP/CRM e credencial de canal são aceitos uma vez, cifrados, e só aparece "configurado / testado em…".
5. **Nunca apagar empresa.** Só suspender (`status`), com contrato e grants cortados em cascata.

## 4. Papéis (novo conceito: operador de plataforma)
O OMNIRA não tem administrador de plataforma humano (`system_admin` é só modo interno de sessão). Proposta:

| Papel | Onde vive | Pode |
|---|---|---|
| `platform_operator` | nova tabela `platform_operators(user_id, status)`, lida pelo **servidor** a cada requisição (não é claim do token) | criar empresa, criar/suspender Hub, contratos, conceder `manage` |
| `hub_admin` | `hub_memberships.role` (já existe) | gerir membros e grants **dentro do próprio Hub**; configurar empresas cujo contrato tenha escopo de gestão |
| `hub_agent` | idem | atender conforme grant |
| `tenant_admin` | do tenant (já existe) | continua dono do que é dele; vê no tenant que existe um contrato de gestão e quem o exerce |

Bootstrap: `omnira-hubctl platform-operator add --email ...` (único caminho para criar o primeiro; auditado). Sem tela para se auto-promover.

## 5. Capacidade de gestão por contrato
`hub_tenant_service_contracts` ganha `management_scopes text[]` (vazio por padrão). Valores: `channels`, `integrations`, `team`, `queues`, `settings`. Grant individual ganha `can_manage` (nunca implícito de `can_reply`). Avaliação: `has_active_hub_access(..., p_require_manage, p_scope)` — a mesma função SQL para leitura, resposta e gestão (um só lugar para auditar).

Rotas novas, todas sob `OMNIRA_HUB_ADMIN_API_ENABLED` (flag própria, desligada):
```
GET/POST   /api/v1/hubs/{hub}/tenants                       (platform_operator: criar empresa)
PATCH      /api/v1/hubs/{hub}/tenants/{tenant}              (suspender/reativar, dados cadastrais)
*          /api/v1/hubs/{hub}/tenants/{tenant}/channels/... (espelha /channels/connections do tenant)
*          /api/v1/hubs/{hub}/tenants/{tenant}/integrations/...
GET/PUT    /api/v1/hubs/{hub}/tenants/{tenant}/agents       (matriz de atendentes)
```
Implementação: **um adaptador fino** monta o `TenantContext` (`Source=hub`, permissões derivadas do escopo, nunca do papel de tenant) e chama os **mesmos handlers/serviços** de canal/integração do tenant, dentro de sessão de sistema. Isso evita um segundo código de canais. *A verificar no spike da fase 3:* se os handlers de canal só dependem de `TenantContext`/permissões (esperado) ou leem membership direto.

## 6. Telas (dentro de `/hub`, atrás de flag, nada substitui o workspace do tenant)
1. **Empresas** — lista com status, saúde do canal, saúde do ERP/CRM, conversas abertas, agentes com acesso. Ação "Nova empresa" (só `platform_operator`).
2. **Nova empresa (assistente)**: Dados (razão, fantasia, CNPJ opcional) → Admin inicial (convite por e-mail, fluxo de convites existente) → Canal (WAHA/Meta, QR, teste) → ERP/CRM (provedor, URL, credencial, teste de conexão) → Fila padrão → Atendentes → **Verificação** (envia mensagem de teste e mostra o resultado; só marca "pronta" se passar).
3. **Empresa › Integrações** — reutiliza `ChannelsPage`/wizard; mostra estado, último teste, rotaciona segredo.
4. **Atendentes** — matriz empresas × agentes. Célula: *sem acesso / leitura / ler e responder* (+ gestão para `hub_admin`), validade, filas permitidas. Mudar uma célula = `grant add/revoke` com auditoria; efeito imediato (RLS).
5. **Equipe do Hub** — membros e papéis.
6. **Auditoria** — quem mudou o quê, por empresa (leitura de `audit_events` filtrada por contrato).

## 7. Quem responde por cada empresa
Em camadas, da mais simples à mais automática:
1. **Acesso (já existe):** quem *pode* atender a empresa = grant ativo + contrato + escopo de fila. A matriz da tela 4 apenas escreve isso.
2. **Assumir (já existe):** hoje o agente assume explicitamente (ADR-0037).
3. **Responsável padrão por empresa (novo, fase 4):** `work_pools` por empresa (já no schema) com membros, capacidade e `skills`; a conversa nova cai no pool da empresa/fila.
4. **Distribuição (fase 4):** manual → round-robin entre disponíveis (reaproveitando presença/roteamento do tenant), sempre dentro de quem tem grant vivo e `can_reply`. Reatribuir/transferir pelo Hub com histórico (`assignment_events`).
Regra: nenhuma camada concede acesso; só escolhem entre quem já tem.

## 8. Fases e gates
| Fase | Entrega | Gate mínimo antes de ligar |
|---|---|---|
| **0** | Este ADR aprovado; `platform_operators` + `hubctl platform-operator`; flag de admin API | testes Postgres: usuário comum não vira operador; leitura do papel é por sessão; mutantes de RLS |
| **1** | Criar/suspender empresa + `tenant_entitlements` + correção do `has_active_hub_access` para respeitar `tenants.status` (API + `hubctl tenant create` + tela "Nova empresa" só dados/admin inicial/fila) | HTTP: não-operador → 404 uniforme; tenant criado é invisível a outros hubs; auditoria transacional; suspensão corta grants |
| **2** | Tela Empresas + Matriz de atendentes + Equipe do Hub (só grants/contratos/membros) | HTTP: hub_agent não edita matriz; hub_admin só no próprio hub; revogação imediata |
|  | ↳ **Entregue em parte pelo ADR-0039** (2026-10-08): painel `/acessos` com matriz agente × instância, administradores por instância, regra "só o admin do Hub libera em mais de uma instância" e Conversas unificadas com filtro de empresas. Falta: convidar pessoa sem conta pelo Hub, validade/filas na tela. | ver ADR-0039 §5 |
| **3** | Gestão delegada de canais e ERP/CRM (`management_scopes`, `can_manage`, adaptador) + assistente completo | spike dos handlers; segredo não vaza em resposta/log/auditoria; escopo `channels` não alcança `team`; revalidação na transação; mutantes |
| **4** | Pools/skills por empresa, distribuição, transferência | corrida de duas atribuições; capacidade; nunca atribuir a quem perdeu grant |
| **5** | UX final, E2E no navegador com API real e canal real de teste | E2E real (hoje só mock) |
Cada fase: revisão Codex real (read-only) antes de ligar flag em produção; sem declarar "pronto" sem gate.

## 9. Decisões do dono — RESOLVIDAS em 2026-10-08 ("recomendado")
1. **Quem é `platform_operator`?** Só você (`keslley@k3gsolutions.com.br`) ou um grupo?
2. **Consentimento do cliente:** o `tenant_admin` da empresa precisa aprovar o contrato de gestão, ou o operador define sozinho? (Recomendado: o tenant vê e pode revogar; aprovação prévia só para clientes externos.)
3. **Granularidade de gestão:** os cinco escopos da §5 estão bons, ou começamos só com `channels` + `integrations`?
4. **Empresa nasce já com contrato para o Hub que a criou?** (Recomendado: sim, sem grants; o operador concede.)
5. **Distribuição automática:** quer round-robin na fase 4 ou só atribuição manual por ora?

Resposta do dono: adotar as recomendações. Valores adotados: (1) `platform_operator` = só `keslley@k3gsolutions.com.br` por ora, outros só via `hubctl`; (2) o `tenant_admin` vê e pode revogar o contrato de gestão, aprovação prévia só para clientes externos; (3) escopos de gestão começam em `channels` + `integrations` (os demais ficam no schema, desligados); (4) empresa criada pelo Hub nasce com contrato para ele e sem grants; (5) round-robin na fase 5, antes disso só atribuição manual.

## 9.1 Fase 0 — o que foi feito (evidência)
- **Migration 098** `has_active_hub_access` passa a exigir `tenants.status = 'active'`: empresa suspensa/inativa deixa de ser lida e respondida pelo Hub (políticas RLS delegadas, projeção, re-checagem na transação de escrita), sem apagar contrato/grant (reativar restaura).
- **Migration 099** `platform_operators` (RLS + FORCE; leitura só da própria linha; escrita só por sessão de sistema) e `is_platform_operator(user)` (responde só para o usuário da sessão; não serve de oráculo).
- **`omnira-hubctl platform-operator add|revoke|list`** (auditado; revogar mantém o registro). Sem rota HTTP: ninguém se autopromove.
- Provas: testes em PostgreSQL real (`provisioning`: operadores e empresa suspensa; `adapters`: respostas/claims 404 com empresa suspensa/inativa, inbox some e volta); 24 mutantes mortos (`scripts/test-hub-reply-mutations.sh`, incl. 5 de SQL e 3 de operador); `scripts/test-hub-migrations.sh` PASS 093..099 (up/down/up idêntico; `platform_operators` com RLS+FORCE).
- **Não feito / não implantado:** nada disto está no banco vivo (vivo em 097). Nenhum operador cadastrado ainda. Fases 1–5 pendentes. Sem revisão Codex desta fase.

## 9.2 Fase 1 — o que foi feito (evidência)
- **Migration 100:** `tenant_entitlements` (sem linha = ligado; escrita só por sessão de sistema; membro da empresa só lê a própria) e `company_creation_requests` (Idempotency-Key do "criar empresa").
- **Capacidades aplicadas no servidor** (`internal/entitlements`, registro fechado): `whatsapp_channel` e `erp_crm` (criar NOVA conexão, pelo serviço de canais e pela rota WAHA legada), `outbound_attachments` (enviar/remover anexo). Desligar nega a operação nova com 403 e **não** apaga dados nem derruba o que já roda (uma sessão WhatsApp existente continua).
- **API (flag `OMNIRA_HUB_ADMIN_API_ENABLED`, desligada por padrão):** `GET/POST /api/v1/hubs/{hub}/companies`, `PATCH /api/v1/hubs/{hub}/companies/{tenant}` (status e capacidades, atômico e auditado). Só passa quem é operador de plataforma ativo **e** `hub_admin` de um Hub ativo (checado na sessão do próprio usuário e de novo dentro da transação de sistema); qualquer outro caso é o mesmo 404. Empresa nova nasce com fila padrão, contrato com o Hub e **nenhum** acesso; primeiro administrador opcional (usuário existente, e-mail exato).
- **Tela:** `/hub/empresas` (Empresas): lista, nova empresa (uma Idempotency-Key por tentativa), suspender com confirmação, reativar, chaves de capacidade. `GET /hubs` ganhou `can_manage_companies` (só para a tela oferecer o link; o servidor decide de novo).
- **Provas:** testes em PostgreSQL real (HTTP + serviço direto + RLS), teste unitário do gate de canais, 11 testes vitest e 2 specs Playwright (API mockada); mutantes em `scripts/test-hub-admin-mutations.sh`.
- **Limites conhecidos:** convidar pessoa nova como administrador não existe ainda (use a equipe da própria empresa); a suspensão agora também para o trabalho em segundo plano (ver "Suspensão de ponta a ponta" abaixo), mas não encerra uma chamada ao provedor que já estava em andamento; o gate de anexos e da rota WAHA legada está no wiring do servidor e **não** tem teste além da compilação; o botão de anexo na UI do tenant ainda aparece com a capacidade desligada (o servidor responde 403); gestão delegada dos canais pelo Hub (fase 3) não existe; a matriz de atendentes (fase 2) não existe.

## 10. Riscos
- Gestão delegada é a parte mais sensível (credenciais de cliente): por isso fica na fase 3, depois de operador, criação e matriz estarem provados.
- Reuso dos handlers de tenant pode esconder dependência de membership: o spike vem antes de qualquer código.
- Papel de plataforma é um novo ponto de poder: tabela só escrita por `hubctl`, auditada, sem tela de autopromoção.
- Hoje o E2E do Hub é só mock; nada acima vale em produção sem E2E real (fase 5).

## Suspensão de ponta a ponta (fecha os achados H-02, H-03, M-01 e H1 do Codex) — 2026-10-09

Regra: **empresa suspensa não é atendida, em nenhum caminho.** Antes só a leitura/escrita interativa parava (098 e `authorization.go`); agora todo caminho que escreve em nome da empresa pergunta, **na própria transação**, `platformdb.LockTenantActive` (lê `tenants.status` e toma um lock compartilhado da linha). O `UPDATE` que suspende conflita com esse lock: ou o trabalho que já decidiu seguir termina antes da suspensão confirmar, ou ele encontra a empresa suspensa e não faz nada. Não existe janela "escreveu depois de a suspensão aparecer".

| Caminho | O que acontece com empresa suspensa | Decisão |
|---|---|---|
| Webhook WAHA/Meta (`WebhookIntake`) e mensagens de grupo | **Nada é gravado** (nem contato, conversa, mensagem, ticket, fluxo, nem o registro de deduplicação); o provedor recebe 200 para não reenviar; contador `webhook_dropped_tenant_suspended_total` | É o que o §Empresa já dizia ("entradas de webhook recusadas/ignoradas"). Consequência assumida: **o que o cliente final escrever durante a suspensão não é guardado**; reativar não o recupera. |
| Worker de entrega (`LockOutbound`) | A mensagem já enfileirada vira `failed` com motivo `company_suspended`; **nada chega ao provedor** | Não fica retida para sair sozinha depois: uma resposta velha não pode ser enviada ao cliente na reativação; a pessoa decide se escreve de novo. |
| Fluxos (job de entrada, timeouts, liberação de conversas presas) | Não começam nem avançam; o job é dado como feito; as consultas do sweeper já filtram empresa ativa (sem fome por causa de uma empresa suspensa grande) | Os mesmos eventos funcionam normalmente depois da reativação. |
| Projetor do Hub | A empresa fica **congelada** (nada copiado, nada removido); a RLS já esconde; a primeira passada após reativar atualiza | Cumpre "grants, contratos e itens ficam guardados". |
| Reply/claim do Hub | Trava (`FOR SHARE`) empresa, Hub, contrato, vínculo e grant até o commit, **depois** pergunta `has_active_hub_access` | Fecha H1: uma revogação em andamento ou é vista pela escrita, ou espera ela terminar. Mesma ordem de locks dos caminhos administrativos (empresa, Hub, contrato, vínculo, grant). |

**Segunda rodada (revisão do Codex de 7a20153..4bfcf4e, task-mv0ejisb-0ff3dw):** o roteamento (job de atribuição e a varredura de
liveness que reenfileira), os jobs de IA (`intelligence_jobs`), a análise de mídia (`message_media` e `message_media_analysis`, vision e
transcrição), a limpeza de runs de fluxo, a reserva de id do provedor e a reconciliação de envios também **param** para empresa
suspensa: as consultas de "pegar trabalho" só enxergam empresa ativa (o trabalho fica pendente, nada se perde) e, onde existe uma
sessão por empresa, ela pergunta `LockTenantActive` primeiro (roteamento: o job é dado como feito; IA: tentativa transitória; reserva:
a mensagem falha como `company_suspended`). Fica de fora de propósito: faxina de retenção de anexos enviados (apaga arquivos vencidos,
não atende ninguém) e gravação de presença de agentes.

**Terceira rodada (re-revisões do Codex, task-mv0gnbxs-m3e5ej e task-mv0holp8-xyusd8):** o desenho final para tudo que chama algo
**fora do banco** em nome da empresa é **segurar o lock da empresa durante a operação**, não só perguntar antes:
`ports.TenantGate.WhileActive(ctx, tenant, fn)` abre uma transação curta que trava `FOR SHARE` a linha da empresa e roda `fn` inteiro
(buscar o arquivo, antivírus, chamar o Gemini, transcrever **e gravar o resultado em todos os ramos**: texto, vazio, rejeitado, erro).
A suspensão **espera** a operação em curso e nenhuma operação começa depois de a suspensão ser visível; `ran=false` = empresa suspensa,
`fn` não rodou e a linha reclamada fica como está (a locação vence e ela volta a ser reclamada quando a empresa estiver ativa). Uma
pergunta que falha conta como "não" (falha fechada). Vale para a mídia (`Processor`), a visão e a transcrição. A **reserva de id do
provedor** também chama o provedor com o lock da empresa seguro (a mensagem não é travada: a reserva continua confirmando numa
transação própria, antes da entrega). Os **claims** de mídia, análise e jobs de IA, o `EnqueueVision`, a varredura de liveness e a
reconciliação de envios fazem `JOIN tenants ... FOR SHARE OF t SKIP LOCKED` (uma suspensão em andamento é pulada, nunca disputada;
as linhas voltam quando ela é desfeita); a limpeza de runs de fluxo espera a suspensão em andamento.
**Custo e limite assumidos:** a suspensão de uma empresa pode demorar até o fim da operação externa em curso dela (o maior prazo é o
da chamada ao provedor/Gemini); `EnsureFromEvent` ainda pode criar um job de IA atrasado para empresa suspensa (resíduo de fila,
nunca é pego nem executado); a gravação de presença de agentes, a faxina de retenção de anexos enviados e o relay do outbox seguem
fora do congelamento de propósito (não atendem clientes; os consumidores têm seus próprios portões).

Também corrigidos no reply: conversa finalizada entre o carregamento e o lock agora recusa (409, antes enfileirava); a auditoria de `hub.message.sent` aponta para a **mensagem** (`resource_type=message`).

**Risco aceito (M-02):** o `--operator` do `hubctl` continua um texto declarado (mais conta do SO e host, como evidência, nunca como prova). Quem tem as credenciais do banco já pode escrever qualquer coisa, inclusive em `audit_events`; assinar a invocação não muda isso. O caminho com identidade de verdade é a API autenticada (`/hub/empresas`, `/acessos`), que grava `actor_id`. `hubctl` fica como ferramenta de bancada/emergência.

**Provas:** `scripts/test-hub-suspension-mutations.sh` (15 mutantes mortos, cada um por pelo menos um teste que falha), testes em PostgreSQL real em `internal/hub/adapters` (escrita espera revogação em andamento, 5 linhas × claim/reply + finalização), `internal/platform/db` (a suspensão espera quem perguntou primeiro), `internal/worker/{delivery,flows,hubprojector}`, `internal/inbox/adapters` (Meta ponta a ponta) e `internal/groups/adapters`.

