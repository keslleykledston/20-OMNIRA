# ADR-0042 — Contexto de sessão do banco que SQL arbitrário não consegue forjar

Status: **IMPLEMENTADA localmente (direção A)**; a implantação e a virada de modo estão registradas na §10. O dono mandou executar ("Faça … o adr-042 e os seguintes"), então as decisões em aberto da proposta
foram tomadas pelo critério abaixo e podem ser revistas: **(1)** direção A agora, B continua como etapa futura opcional; **(2)** a chave de assinatura vive na variável de ambiente do `api` e do `worker`
(a infraestrutura não tem cofre); **(3)** a janela de virada é conduzida pelo modo `measure` com tráfego real (§9), não por uma janela marcada.
Vocabulário de evidência: IMPLEMENTED / UNIT / POSTGRES / E2E VERIFIED / NOT WIRED / BLOCKED.
Relaciona: ADR-0040 (§20 e §21 registraram este ponto como HIGH em aberto do Codex), ADR-0036, `docs/architecture/HUB-VERIFICATION-STATUS.md`.

## 1. O problema, dito com precisão

Toda a RLS do OMNIRA decide pelo que a **sessão diz sobre si mesma**, em variáveis de configuração do PostgreSQL (GUCs) que a aplicação gravava no início de cada
transação (`internal/platform/db/session.go`): `app.current_user_id`, `app.is_system_admin`, `app.acting_hub`, `app.acting_tenant`.

O papel `omnira_app` pode gravar qualquer GUC com `set_config(...)`. Logo, **quem conseguir executar SQL arbitrário na sessão da aplicação** (uma injeção de SQL, ou um
defeito que deixe o cliente escolher texto de consulta) podia:

- ligar `app.is_system_admin` e passar por todas as políticas legadas que abrem para o administrador de sistema;
- limpar `app.acting_hub` e sair das políticas restritivas do contexto delegado (a guarda `acting_hub() IS NULL OR …`);
- escolher `app.current_user_id`.

**Isto não foi criado pelo Hub.** Era o modelo de confiança de toda a RLS desde o início; o Codex o apontou nas fases 06 e 07 só porque as políticas novas repetiam a guarda.
A RLS protege contra erro de lógica da aplicação (consulta sem filtro de empresa, rota que esquece a checagem), que é o risco comum, e **não contra SQL arbitrário**.

## 2. Medido antes de desenhar (2026-10-10)

- `is_system_admin` aparece em **261 linhas** das migrations (políticas e funções); é a flag que mais abre portas.
- No código Go só `internal/platform/db/session.go` grava `app.is_system_admin`/`app.current_user_id` (mais os ajudantes de teste e alguns scripts). `app.acting_hub`/`app.acting_tenant` eram gravadas
  só por `lock_served_tenant` (migrations 108/111/114).
- Leitores diretos das GUCs no banco (fora das quatro funções `current_user_id`, `is_system_admin`, `acting_hub`, `acting_tenant`): 3 funções (`has_active_membership`, `has_active_admin_membership`,
  `actor_has_permission`) e 6 políticas de leitura (`conversations`, `messages`, `contacts`, `tickets`, `message_media`, `contact_notes`). Nenhum outro.
- Nenhum teste tentava o ataque: a suíte provava que o código honesto respeita a RLS, não que o papel não consegue mentir.

## 3. Alternativas consideradas

**A. Contexto assinado** (escolhida). A aplicação assina quem é a sessão; o banco verifica com uma chave que `omnira_app` não lê.
**B. Logins separados por finalidade** (`omnira_member`, `omnira_hub`, `omnira_system`): mais forte, mas três pools e três credenciais, as 261 ocorrências migram da GUC para pertinência a papel, e o caminho
delegado (sessão de sistema por empresa dentro da transação da requisição, que a fase 08 do Hub usa de propósito) perderia a atomicidade. Fica como etapa futura opcional.
**C. Aceitar e reforçar o entorno** (varredura de SQL concatenado, log de `set_config`): não fecha o buraco; continua valendo como higiene.

## 4. Desenho implementado (migration 000118)

**Mudança de rumo medida durante a implementação.** O primeiro desenho verificava o HMAC **a cada leitura** do contexto. Medido (100 000 chamadas por linha, banco descartável): **36 µs por chamada**
contra ~3 µs da definição antiga: dez vezes, e as políticas legadas chamam as funções por linha. Trocado por: **o HMAC é verificado uma vez por transação** e o resultado fica num registro que só o dono escreve.

1. **Chave.** `session_ctx_keys` (`current` e `previous`; sem nenhum privilégio para `omnira_app`, RLS ligada sem política como segunda parede). A aplicação lê a mesma chave de `OMNIRA_SESSION_CTX_KEY` (hex, ≥ 32 bytes); o migrador
   (`tools/migrate-sql.sh`) a instala a partir da mesma variável, por stdin (nunca aparece na lista de processos). A rotação instala a nova como `current` e a antiga vira `previous` (continua aceita: nenhuma instância ainda
   não reiniciada perde acesso); `ctx_drop_previous_key()` a aposenta.
2. **Desafio e resposta.** Como **primeiro comando de cada transação** a aplicação (`internal/platform/sessionctx`, chamado por `WithTenantSession`, o único ponto do código de produção que abre sessão) pede ao banco
   `ctx_challenge()`: um nonce aleatório (128 bits), guardado no registro **desta** transação. Responde com `ctx_begin('b1|<usuário>|<0|1 sistema>|<nonce>|<hmac-sha256 hex>')`, assinado com a chave do seu ambiente, na mesma ida ao banco que grava as GUCs legadas.
   `ctx_begin` aceita só se: tag e número de campos certos, **assinatura válida** com uma das chaves instaladas (comparada pelo SHA-256 das duas assinaturas, para o tempo não revelar quantos bytes iniciais de uma assinatura falsa estão certos),
   **o nonce é exatamente o emitido para esta transação deste backend** e nenhum contexto foi aceito nesta transação ainda (a garantia é "uma vez por estado transacional": um `ROLLBACK TO SAVEPOINT` que desfaz a aceitação desfaz também a identidade, então a sessão fica **sem contexto**, nunca com outro; testado). Consequência: **um token vale para uma transação, uma vez**; uma cópia capturada em outra sessão, em outra transação do mesmo backend, ou de um pedido que
   terminou em rollback não serve para nada, e **nada depende de relógio**. (Primeiros desenhos, descartados: janela de 5 minutos; depois pid + sequência por backend, que o teste derrubou: a marca de "token já usado" se perde no rollback do próprio pedido.)
   Aceito, o registro (`session_ctx_verified`, UNLOGGED, uma linha por backend) ganha o usuário e o indicador de sistema. Custo: **uma ida ao banco a mais por transação** (a assinatura precisa do nonce).
3. **A linha só vale para a transação que a gravou**: ela guarda o identificador da transação de nível superior (`pg_current_xact_id()`, `xid8`; savepoints o compartilham) e os leitores comparam com `pg_current_xact_id_if_assigned()`: a próxima transação da mesma conexão de pool não herda nada, e não há comparação de relógio. (O pedido já tem xid porque o desafio grava no registro, que é UNLOGGED: o commit não espera flush do WAL.)
4. **Contexto delegado.** `lock_served_tenant` (que já prova a delegação sob trava) grava o hub e a empresa que está servindo no mesmo registro por `ctx_set_acting` (só ele chama). Não há o que assinar: roda dentro do banco.
5. **Leitores.** `current_user_id()`, `is_system_admin()`, `acting_hub()`, `acting_tenant()` e o novo `ctx_acting_present()` (que substitui as 9 leituras diretas) leem do registro. As funções e as políticas continuam as mesmas; só o que
   as quatro respondem muda.
6. **Três modos** (`session_ctx_mode`, só o dono muda, por `ctx_set_mode`, que reescreve os leitores de uma vez):
   - `off`: os leitores são exatamente os de antes da migration (SQL simples sobre as GUCs legadas, inlinados pelo planejador: custo zero). **Estado em que a migration deixa o banco.**
   - `measure`: a GUC legada decide, e toda divergência do valor verificado vira `WARNING session_ctx divergence: …` no log do banco.
   - `enforce`: só o contexto verificado vale; ausente, forjado, vencido ou de outro backend = sem contexto (sem acesso); as GUCs legadas são ignoradas.
   `enforce` exige chave instalada.
7. **Fail-closed preservado.** Um contexto delegado danificado nunca devolve direitos de membro: no `enforce`, `ctx_acting_present()` é "existe linha de acting nesta transação".

## 5. Garantias e limites (honestos)

**Garante, no modo `enforce`:** SQL arbitrário na sessão da aplicação, sem a chave, **não** consegue (a) virar administrador de sistema, (b) escolher outro usuário, (c) forjar nem limpar o contexto delegado,
(d) reaproveitar o contexto de outra transação ou de outro backend, ou um token capturado (nem em transação posterior do mesmo backend, nem de pedido que deu rollback), (e) ler, escrever ou apagar a chave, o registro ou o modo, (f) chamar as funções internas, (g) sombrear as funções do verificador (`search_path` fixo com `pg_catalog` primeiro; teste com função temporária).
Cada item é um teste contra o PostgreSQL real como o papel da aplicação (§8).

**Não garante:**
- Quem **lê a memória do processo** `api`/`worker` ou o `.env` tem a chave e assina o que quiser. A chave é segredo da aplicação, no mesmo nível das outras (`OMNIRA_CREDENTIALS_KEY`); a combinação "injeção de SQL + vazamento da chave" volta ao estado anterior.
- O dono do banco (`omnira`, superusuário) está fora do modelo, como sempre esteve.
- Um token que vaze (log de statements, captura de tráfego) não é reproduzível: só vale para a transação que recebeu aquele nonce, e uma vez. A **chave**, sim, valeria tudo: ela vai ao banco como parâmetro ligado (`\bind`, nunca em linha de comando), e o instalador (`tools/migrate-sql.sh` e `scripts/session-ctx.sh`) **sobrepõe, só na sua própria sessão**, todo registro de comando/duração/parâmetro (`log_statement`, `log_min_duration_*`, `log_transaction_sample_rate`, `log_parameter_max_length*`) e **recusa** instalar se `pgaudit` ou `auto_explain` estiverem pré-carregados (registram parâmetros por conta própria; `SESSION_CTX_ALLOW_LOGGED_KEY=1`/`FORCE=1` aceita o risco e exige rotação). `scripts/test-session-ctx-key-logging.sh` prova: com o servidor logando TUDO, a chave chegou ao banco e não ao log (com controle positivo).
- O registro é por backend (`pg_backend_pid()`), então o desenho pressupõe **conexão direta ao PostgreSQL** (ou pooler em modo de sessão). Um pooler em modo de transação (PgBouncer) trocaria o backend entre o desafio e a resposta e quebraria `ctx_begin`; o compose atual não tem pooler.
- O registro é lido por `pid + xid da transação`. (Primeiro desenho: `now()`; a revisão do Codex apontou que igualdade de relógio não é identidade de transação, e como o desafio já grava no registro — o pedido já tem xid —, o xid saiu de graça.)
- `hubctl` e qualquer ferramenta que abra sessão pelo `WithTenantSession` precisa da mesma variável no ambiente quando o modo for `enforce` (sem ela, sessões sem contexto = sem acesso).
- Scripts que gravam as GUCs com `psql` (`tools/test-isolation.sh`, `scripts/w3-smoke.sh`) rodam em bancos descartáveis em modo `off`; contra um banco em `enforce` seriam "sem contexto".
- Custo: ver §6. A direção B (papéis separados) continua mais forte e continua sendo a etapa futura se o dono quiser defesa em profundidade.

## 6. Custo medido

`scripts/bench-hub-policies.sh` (banco descartável, papel `omnira_app`, 2 instâncias × 30 000 conversas × 10 mensagens = 600 000 mensagens; mediana de 5 execuções do `EXPLAIN ANALYZE`, ms). `CTX_MODE` roda as mesmas sessões com o contexto assinado.

| consulta (30 000 conversas por instância) | off (= antes) | **enforce** | measure |
|---|---|---|---|
| LIST como membro | 402 | **427** | 5 594 |
| COUNT como membro | 88 | **93** | 1 083 |
| LIST como delegado | 165 | **164** | 174 |
| COUNT como delegado | 8,4 | **8,0** | 10 |
| leituras pontuais (MSGS, CONTACT, NOTES) | 0,3–7 | **0,4–7** | 0,5–10 |

O PASS do bench (delegado ≤ 3× o membro e dentro dos limites absolutos) vale em `off` e `enforce`. **`measure` é caro em tabelas grandes** (as funções passam a ser plpgsql e cada uma consulta o registro): é um modo de observação de poucos minutos, não de operação.

O caminho até aqui, para registro: verificar o HMAC a cada leitura custava 36 µs/chamada (10× a definição antiga); o registro verificado uma vez por transação baixou para ~10 µs/chamada, e ainda deixava o membro 5× (LIST) e 14× (COUNT) mais lento porque a política do membro chama cinco leitores por linha; o que fechou a conta foi (a) `has_active_membership`, `has_active_admin_membership` e `actor_has_permission`
lerem o registro **uma vez** por chamada em `enforce` e (b) as seis políticas de leitura quentes (`conversations`, `messages`, `contacts`, `tickets`, `message_media`, `contact_notes`) tomarem `(SELECT current_user_id())` e `(SELECT is_system_admin())` como InitPlan (lidos uma vez por consulta).
Números medidos na revisão anterior à passada do Codex; as mudanças dela tocam só `ctx_begin` (uma vez por transação).

Volume real hoje (produção): a maior tabela com RLS tem ~19 mil linhas, `messages` ~3 mil e `conversations` ~170; o bench acima é um cenário de estresse de 600 mil mensagens.

## 7. Rollback

`scripts/session-ctx.sh mode off` (instantâneo, sem reiniciar): os leitores voltam à definição antiga. A migration tem `.down.sql` gerado das definições anteriores (testado: sobe, desce, sobe).
Retirar a variável do ambiente de `api`/`worker` com o banco em `off` ou `measure` também é seguro (voltam a gravar só as GUCs legadas); com o banco em `enforce` seria perder o acesso, por isso a ordem de operação da §9.

## 8. Evidência (local, 2026-10-11; `LAB`)

- **POSTGRES (papel real `omnira_app`, não superusuário, FORCE RLS):** `internal/platform/db/session_ctx_integration_test.go` — SQL arbitrário não vira administrador nem escolhe usuário (GUC forjada, token com outra chave, adulterado em cada campo, de outra
  transação, de pedido que deu rollback, sem desafio, com assinatura válida mas protocolo errado); o papel da aplicação não alcança a chave, o registro, o modo nem as funções internas; a função temporária que sombreia `sha256` não vale; modos `off`/`measure`/`enforce`
  (em `measure` o aviso de divergência sai, em `enforce` a recusa é erro 28000); rotação da chave sem derrubar ninguém; um contexto por transação; savepoint. `internal/hub/adapters/serving_membership_while_acting_integration_test.go` — a associação própria não fala dentro do contexto delegado.
  Os testes de forja de GUC do Hub passaram a afirmar, em `enforce`, que forjar **não muda nada**.
- **UNIT:** `internal/platform/sessionctx` (vetores de assinatura calculados à parte em Python, chave ausente/inválida/curta, contagem de recusas).
- **Gate:** `scripts/test-integration.sh` agora roda com o banco em `enforce` e a chave instalada (padrão; `SESSION_CTX_MODE=off|measure` reproduz o legado): **37 de 37 pacotes `ok`**. Os pacotes de banco, tenancy, hub e atendimento também passaram em `off` e `measure`.
- **Mutação:** `scripts/test-session-ctx-mutations.sh` — **41 de 41 mortos** (SQL da migration e assinador Go); os equivalentes estão documentados no cabeçalho do script. A suíte achou dois buracos de TESTE que foram corrigidos (o nonce nunca era exercido; a associação própria dentro do acting).
- **Migrations:** `scripts/test-hub-migrations.sh` — 093..118 sobem, descem até o esquema anterior ao Hub e sobem de novo idênticas.
- **E2E em navegador real contra os binários reais, banco em `enforce`:** 12 de 12.
- **A chave não vai para o log:** `scripts/test-session-ctx-key-logging.sh` (servidor logando tudo; controle positivo).
- **Custo:** §6. **Codex (somente leitura, não executou testes), 6 passadas:** 1ª HIGH 1 (replay no mesmo backend) + MEDIUM 4 + LOW 2; 2ª HIGH 1 (instalador sem a trava de log) + MEDIUM 1 + LOW 2; 3ª HIGH 1 (log por duração) + MEDIUM 1 + LOW 1; 4ª HIGH 1 (guarda incompleta) + LOW 1; todos corrigidos (a identidade por relógio virou xid, o replay virou desafio do banco, a trava virou sobreposição da sessão do instalador); 5ª HIGH 1 (a migration deixa o modo `off`: um deploy novo continua forjável até o operador virar o modo) — **resposta de desenho + guarda**: a virada escalonada é deliberada (virar `enforce` com tráfego que não assina trancaria todo mundo), então o modo é decisão do dono; `api` e `worker` passaram a registrar o modo ao iniciar, avisar alto quando não é `enforce` e, com `OMNIRA_SESSION_CTX_REQUIRE_ENFORCE=true`, **recusar iniciar** sem `enforce` e sem a chave; 6ª passada: CRITICAL 0, HIGH 0 "sob o desenho de virada controlada pelo dono", MEDIUM 0, LOW 0 (ressalva do Codex, aceita: o compose repassa a flag mas não a liga por padrão; ligá-la é o passo final da §9; a guarda só vale na partida).
- **Não provado:** o tráfego real de usuários em `measure` (§10); login real do Keycloak; carga.

## 9. Operação (`scripts/session-ctx.sh`)

1. `gen-key` → guardar como `OMNIRA_SESSION_CTX_KEY` no `.env` (o compose a passa a `api`, `worker` e `migrate`).
2. Backup, migrar (instala a chave), recriar `api` e `worker` com a mesma variável. Modo continua `off`.
3. `mode measure`; observar `divergences` sob tráfego real até ficar em zero (webhooks e workers já geram tráfego; usuários reais, se houver).
4. `mode enforce`. Observar o log do `api` (a mensagem "the database REFUSED a signed context") e a saúde.
5. Rotação: `gen-key` → `.env` → migrar (instala como `current`, a antiga vira `previous`) → recriar `api`/`worker` → `drop-previous`.

## 10. Estado

- **Migration 000118** aplicada em produção (ledger 118) em 2026-10-11, com backup prévio e tags de rollback (`rollback-pre-adr0042-20261011-0237`).
- **Modo do banco: `enforce`** (passou por `off` → `measure`, 0 divergências, → `enforce`). `api` e `worker` rodam com `OMNIRA_SESSION_CTX_REQUIRE_ENFORCE=true` e se recusam a iniciar fora de `enforce`.
- **Chave:** em `OMNIRA_SESSION_CTX_KEY` no `.env` (modo 600) e na tabela do dono; fora do log (`scripts/test-session-ctx-key-logging.sh`).
- **Sondas em produção como `omnira_app`:** administrador forjado vê 0 empresas; contexto de sistema genuíno vê 3; o mesmo token em outra transação é recusado.
- **Não provado:** `measure` rodou de madrugada, só com tráfego de workers (sem usuário real); login real do Keycloak; carga.
- **Rollback instantâneo:** `scripts/session-ctx.sh mode off`; com `REQUIRE_ENFORCE=true`, `api`/`worker` só reiniciam depois de remover a flag do `.env`.
- Estado de produção permanece `LAB`.
