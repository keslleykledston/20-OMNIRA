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
2. **Contexto assinado.** `b1|<usuário ou vazio>|<0|1 sistema>|<pid do backend>|<emitido em, epoch s>|<hmac-sha256 hex>`. A aplicação o monta em `internal/platform/sessionctx` e o entrega a `ctx_begin(raw)` como
   **primeiro comando de cada transação**, na mesma ida ao banco que grava as GUCs legadas (`sessionctx.Set`, chamado por `WithTenantSession`, que é o único ponto do código de produção que abre sessão).
   `ctx_begin` aceita só se: tag e número de campos certos, assinatura válida com uma das chaves instaladas (comparada pelo SHA-256 das duas assinaturas, para o tempo não revelar quantos bytes iniciais de uma assinatura falsa estão certos),
   **o pid é o do backend atual** (uma cópia capturada em outra sessão não serve), emitido há no máximo 5 minutos e não mais de 1 minuto no futuro. Aceito, grava uma linha em `session_ctx_verified` (UNLOGGED, uma por backend).
3. **A linha só vale para a transação que a gravou** (`txstart = now()`): a próxima transação da mesma conexão de pool não herda nada.
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
(d) reaproveitar o contexto de outra transação ou de outro backend, (e) ler, escrever ou apagar a chave, o registro ou o modo, (f) chamar as funções internas, (g) sombrear as funções do verificador (`search_path` fixo com `pg_catalog` primeiro; teste com função temporária).
Cada item é um teste contra o PostgreSQL real como o papel da aplicação (§8).

**Não garante:**
- Quem **lê a memória do processo** `api`/`worker` ou o `.env` tem a chave e assina o que quiser. A chave é segredo da aplicação, no mesmo nível das outras (`OMNIRA_CREDENTIALS_KEY`); a combinação "injeção de SQL + vazamento da chave" volta ao estado anterior.
- O dono do banco (`omnira`, superusuário) está fora do modelo, como sempre esteve.
- Um contexto assinado que vaze por log de statements com parâmetros inlinados (protocolo simples) só vale 5 minutos e só no backend para o qual foi assinado: a captura não é reproduzível de outra sessão. O `pgx` usa o protocolo estendido (valores não aparecem em `pg_stat_activity`).
- O vínculo ao pid pressupõe **conexão direta ao PostgreSQL** (ou pooler em modo de sessão). Um pooler em modo de transação (PgBouncer) mudaria o backend entre comandos e quebraria `ctx_begin`; o compose atual não tem pooler.
- `hubctl` e qualquer ferramenta que abra sessão pelo `WithTenantSession` precisa da mesma variável no ambiente quando o modo for `enforce` (sem ela, sessões sem contexto = sem acesso).
- Scripts que gravam as GUCs com `psql` (`tools/test-isolation.sh`, `scripts/w3-smoke.sh`) rodam em bancos descartáveis em modo `off`; contra um banco em `enforce` seriam "sem contexto".
- Custo: ver §6. A direção B (papéis separados) continua mais forte e continua sendo a etapa futura se o dono quiser defesa em profundidade.

## 6. Custo medido

[PREENCHER com a saída de `scripts/bench-hub-policies.sh` nos três modos]

## 7. Rollback

`scripts/session-ctx.sh mode off` (instantâneo, sem reiniciar): os leitores voltam à definição antiga. A migration tem `.down.sql` gerado das definições anteriores (testado: sobe, desce, sobe).
Retirar a variável do ambiente de `api`/`worker` com o banco em `off` ou `measure` também é seguro (voltam a gravar só as GUCs legadas); com o banco em `enforce` seria perder o acesso, por isso a ordem de operação da §9.

## 8. Evidência

[PREENCHER ao final: gate, mutação, E2E, Codex]

## 9. Operação (`scripts/session-ctx.sh`)

1. `gen-key` → guardar como `OMNIRA_SESSION_CTX_KEY` no `.env` (o compose a passa a `api`, `worker` e `migrate`).
2. Backup, migrar (instala a chave), recriar `api` e `worker` com a mesma variável. Modo continua `off`.
3. `mode measure`; observar `divergences` sob tráfego real até ficar em zero (webhooks e workers já geram tráfego; usuários reais, se houver).
4. `mode enforce`. Observar o log do `api` (a mensagem "the database REFUSED a signed context") e a saúde.
5. Rotação: `gen-key` → `.env` → migrar (instala como `current`, a antiga vira `previous`) → recriar `api`/`worker` → `drop-previous`.

## 10. Estado

[PREENCHER: implantação e modo]
