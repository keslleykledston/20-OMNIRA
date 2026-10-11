# ADR-0042 — Contexto de sessão do banco que SQL arbitrário não consegue forjar

Status: **PROPOSTA** (nada implementado; aguarda decisão do dono). Vocabulário de evidência: tudo abaixo é desenho (`NOT WIRED`), exceto o que a §2 diz ter medido.
Relaciona: ADR-0040 (§20 e §21 registram este ponto como HIGH em aberto do Codex), ADR-0036, `docs/architecture/HUB-VERIFICATION-STATUS.md`.

## 1. O problema, dito com precisão

Toda a RLS do OMNIRA decide pelo que a **sessão diz sobre si mesma**, em variáveis de configuração do PostgreSQL (GUCs) que a aplicação grava no início de cada
transação (`internal/platform/db/session.go`): `app.current_user_id`, `app.is_system_admin`, `app.acting_hub`, `app.acting_tenant`.

O papel `omnira_app` pode gravar qualquer GUC com `set_config(...)`. Logo, **quem conseguir executar SQL arbitrário na sessão da aplicação** (uma injeção de SQL, ou um
defeito que deixe o cliente escolher texto de consulta) pode:

- ligar `app.is_system_admin` e passar por todas as políticas legadas que abrem para o administrador de sistema;
- limpar `app.acting_hub` e sair das políticas restritivas do contexto delegado (a guarda `acting_hub() IS NULL OR …`);
- escolher `app.current_user_id`.

**Isto NÃO é novo e NÃO foi criado pelo Hub.** É o modelo de confiança de toda a RLS desde o início; o Codex o apontou nas fases 06 e 07 só porque as políticas novas
repetem a guarda. A RLS protege contra erro de lógica da aplicação (consulta sem filtro de empresa, rota que esquece a checagem), que é o risco comum, **e não
contra SQL arbitrário**. A defesa contra SQL arbitrário hoje é: consultas parametrizadas em todo o código (revisão e testes), sem SQL montado a partir de entrada.

## 2. O que foi medido (2026-10-10)

- `is_system_admin` aparece em **261 linhas** das migrations (políticas e funções); é a flag que mais abre portas.
- No código Go, só `internal/platform/db/session.go` grava `app.is_system_admin` (mais os ajudantes de teste e um script de fumaça). `app.acting_hub`/`app.acting_tenant` são gravadas
  por funções SQL `SECURITY DEFINER` das migrations 108/111/114 (`lock_served_tenant`), com `is_local = true`.
- Nenhum teste tenta o ataque ("SQL arbitrário forja a GUC"): a suíte prova que o código honesto respeita a RLS, não que o papel não consegue mentir.

## 3. Alternativas

**A. Contexto assinado.** A aplicação grava UMA GUC `app.ctx` com `{usuário, hub, tenant, system, expira}` + HMAC; funções `current_user_id()`, `acting_hub()`,
`is_system_admin()` passam a ler dela e VERIFICAR a assinatura (chave guardada numa tabela sem nenhuma concessão para `omnira_app`, lida só por funções
`SECURITY DEFINER` do dono). Quem executa SQL arbitrário não conhece a chave: forjar vira "assinatura inválida" (contexto vazio = sem acesso).
- Custo: uma verificação por consulta que lê o contexto (mitigável com `(SELECT …)` InitPlan, que já usamos) e uma chave a rotacionar; mudança concentrada em
  `session.go` e nas 4 funções `current_*`, sem tocar as 261 linhas de política e função.
- Risco: a chave passa a ser segredo da aplicação (vive na memória do processo; hoje já há segredos assim). Se vazar junto com uma injeção, volta ao estado atual.

**B. Logins separados por finalidade.** `omnira_member`, `omnira_hub`, `omnira_system` como logins distintos (pools distintos); a flag de administrador de sistema só
existe na sessão do login de sistema (`pg_has_role(current_user, 'omnira_system', 'member')` no lugar da GUC). Um SQL arbitrário no pool de membros não vira sistema.
- Custo: três pools e três credenciais; o worker e os caminhos de escrita do Hub (sessão de sistema por empresa) passam ao pool de sistema; as 261 ocorrências
  precisam migrar da GUC para a pertinência ao papel (mecânico, mas grande, e cada política tem teste).
- Risco: o caminho `WithSystemTenantSession` é usado em requisições de usuário (escritas do Hub); ele passaria a abrir outra conexão (latência, e perda da
  atomicidade com a transação da requisição, que a fase 08 do Hub usa de propósito).

**C. Aceitar e reforçar o entorno.** Manter o modelo, registrar o risco, e adicionar: teste de varredura que falha se aparecer SQL montado por concatenação,
`log_statement` para `set_config('app.` fora de `session.go`, e revisão obrigatória de qualquer rota nova que aceite texto de consulta.
- Custo: baixo. Risco: continua um defeito de injeção equivalente a administrador de sistema.

## 4. Recomendação

**A agora, B depois (se o dono quiser a defesa em profundidade).** A é a única que fecha o buraco sem mexer nas 261 linhas de política e função e sem perder a atomicidade que o
caminho delegado precisa; B é mais forte, mas é um projeto próprio. C sozinha não resolve, mas a varredura de SQL concatenado entra junto com A (barata, e prova
a premissa de que a aplicação só usa consultas parametrizadas).

Entrega em fatias, cada uma atrás de flag e com o teste do ataque:
1. Teste que PROVA o problema hoje (sessão `omnira_app` com SQL arbitrário liga `app.is_system_admin` e lê outra empresa): vermelho no estado atual, documentado.
2. Funções `current_*` verificando a assinatura, em modo "só mede" (registra divergência, não nega), para ver o custo real e confirmar que nada legítimo diverge.
3. Modo "nega": contexto sem assinatura válida = sem acesso. O teste 1 passa a ficar verde.
4. Rotação da chave (duas chaves aceitas durante a troca) e runbook.

## 5. O que preciso do dono

1. Aceita a direção A (e B como etapa futura opcional)?
2. Onde vive a chave de assinatura: variável de ambiente do `api` e do `worker` (como os outros segredos) ou cofre? (A infraestrutura atual não tem cofre.)
3. Janela para a etapa 3: ela nega acesso a qualquer sessão que o código monte sem assinar; a etapa 2 existe para descobrir isso antes.

**Estado:** `LAB`. Nada deste ADR está implantado nem implementado.
