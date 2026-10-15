# ADR-0046 — Convite completo: função e filas no convite, e conta de login criada pelo OMNIRA

Status: **IMPLEMENTADA** (2026-10-14), ampliando o fluxo de convite existente. O dono pediu "implante as melhorias propostas" sem responder §6; adotadas as recomendações: caminho A; conta de convite cancelado é **desabilitada**; a autorização por e-mail do Hub **não** cria conta nesta fase; a edição de e-mail no Keycloak **não** foi mudada (continua pendência do dono, para não alterar o comportamento atual). Diferenças da proposta, para ampliar sem criar peças novas: o plano fica em **colunas** de `membership_invitations` (migration 136, sem tabela nova; a fila é conferida contra a instância ao convidar e de novo ao aceitar) e não há `PATCH` do plano. O marcador `omnira_provisioned` exige no realm `unmanagedAttributePolicy=ADMIN_EDIT` (Keycloak 25 descarta atributos não declarados); sem ele a conta funciona, mas reenviar/cancelar não a tocam, e o adaptador avisa no log. Provas: unidade (`keycloak_logins_test.go`), Postgres com instâncias A/B (`invitations_plan_test.go`), Keycloak 25 real descartável (`scripts/test-keycloak-logins.sh`), Vitest e Playwright com mocks.

**Revisão do Codex, rodada 1 (2026-10-14): 1 HIGH e 10 MEDIUM, corrigidos antes do deploy:**
- **HIGH: a conta ficava presa só ao "OMNIRA criou", não ao convite.** Agora fica presa ao convite que a criou: `omnira_invitation` e a validade dele, `omnira_invitation_expires`. Quando o OMNIRA a desliga por cancelamento, ela recebe `omnira_disabled`.
  - Quem pode assumir a conta: o reenvio do mesmo convite; o convite que substitui o anterior na mesma instância (o `UPDATE ... RETURNING` passa os ids substituídos); ou um convite novo depois que o anterior venceu ou foi cancelado.
  - Um convite de **outra** instância, com um convite ainda vivo, não redefine a senha nem desliga a conta. O e-mail desse convite manda usar a senha do primeiro convite.
  - Cancelar desliga só a conta daquele convite.
  - Uma conta desligada por um administrador do Keycloak nunca é religada (502).
- **Falha fechada:** se o realm descartar os atributos (sem `ADMIN_EDIT`), a conta recém-criada é removida e o convite responde 502.
- **A flag ligada sem configuração completa impede a API de subir** (`config.Validate`).
- O segredo vai só para o serviço `api`, não para o worker.
- O aceite audita `agent.enabled` e `agent.queue_assigned` em nome de quem convidou, com o convite como causa.
- A auditoria do cancelamento diz se a conta foi desligada.
- O down da 135 não reabre a política antiga (fica irreversível). O down da 136 avisa que descarta os planos.
- A senha é sorteada sem viés (`rand.Int`).
- A configuração do realm virou script versionado: `scripts/keycloak-provisioner-setup.sh`. O teste com Keycloak real roda esse mesmo script, com hostname público diferente da URL interna, como em produção.
- Provas: 15 mutantes mortos.

**Riscos aceitos (MEDIUM, ficam registrados):**
- A conta de um convite que **só venceu** continua habilitada com a senha temporária até chegar um convite novo. Não há job de reconciliação. Só quem tem a caixa de e-mail conhece a senha, e sem convite a pessoa não ganha acesso a nada.
- O Keycloak e o SMTP são chamados dentro da transação do convite (o SMTP já era assim). Se o commit falhar depois da chamada ao Keycloak, sobra uma conta presa a um convite que não existe. Ela é assumida quando vence (7 dias).
- Não há outbox para esses efeitos externos.

**Revisão do Codex, rodada 2: 2 HIGH, corrigidos antes do deploy:**
- **Corrida entre convites para o mesmo e-mail.** Criar, reenviar e cancelar tomam um lock consultivo do Postgres por e-mail (`pg_advisory_xact_lock`), válido até o fim da transação do pedido e tomado antes de qualquer escrita em convite. Além disso, o adaptador relê a conta logo antes de gravar e só grava se ela ainda cumpre a regra. Se não cumprir, responde 502 e o convite pode ser reenviado.
- **Conta sem vínculo completo** (sem convite ou com validade ilegível): ninguém a assume (falha fechada).

Também corrigido nessa rodada:
- A conta criada é conferida e, se preciso, removida pelo id que o Keycloak devolveu, nunca por busca.
- Uma vaga antiga e inativa numa fila escolhida no convite volta ativa no aceite.
- Eventos próprios `membership.invitation.login_prepared` e `membership.invitation.login_disabled`, com resultado `failure` quando o Keycloak falha.
- A auditoria do plano no aceite fica num `SAVEPOINT`: uma falha dela não desfaz o aceite.
- O script de configuração passa a senha do admin em `KC_CLI_PASSWORD`, fora da linha de comando. Ele retoma uma execução interrompida e põe o `.env` em 600 antes de gravar o segredo.

**Riscos aceitos (MEDIUM):**
- Uma conta desligada pelo OMNIRA que um administrador do Keycloak religa e depois desliga à mão volta a ser religada por um convite novo. Não há como distinguir os dois casos, e só o dono administra o Keycloak.
- Cancelar com o Keycloak fora do ar cancela o convite e deixa a conta ligada. Isso fica registrado como falha na auditoria e tem a mesma exposição do convite vencido.
- O convite não tem chave de idempotência: repetir o pedido substitui o convite, como antes.

**Revisão do Codex, rodada 3: 2 HIGH e 2 MEDIUM, corrigidos:**
- A senha temporária só é gravada depois de reler a conta mais uma vez: ainda sem uso, ligada e presa a este convite. Se a pessoa terminou o primeiro login no meio do caminho, a senha que ela escolheu fica.
- A validade do vínculo é conferida antes de qualquer resposta positiva, inclusive para o próprio convite.
- Uma conta nova precisa guardar os três marcadores (criada pelo OMNIRA, convite e validade). Se faltar qualquer um, ela é removida pelo id e o convite responde 502.
- O script de configuração recusa um cliente `omnira-provisioner` já existente que não seja exatamente o esperado: confidencial, só conta de serviço, e só com os papéis padrão do realm mais `view-users` e `manage-users`. A recusa acontece antes de o segredo ser usado. O teste real cobre um cliente com um papel a mais.

**Resíduo aceito:** entre a última leitura e cada escrita no Keycloak sobra uma janela de milissegundos. O Keycloak não tem escrita condicional, e as escritas do próprio OMNIRA para o mesmo e-mail já são serializadas pelo lock.

**Revisão do Codex, rodada 4: 1 HIGH e 2 MEDIUM, todos no script de configuração, corrigidos:**
- O script confere o que a conta de serviço **alcança**, não só os papéis diretos. Ela não pode pertencer a grupo nenhum. Os papéis de realm efetivos ficam limitados ao papel padrão do realm e ao que ele contém. Os papéis efetivos de `realm-management` ficam limitados a `view-users`, `manage-users` e ao que eles contêm (`query-users`, `query-groups`).
- O cliente precisa ser OpenID Connect com autenticação `client-secret`.
- Tudo é conferido **antes** de qualquer papel ser adicionado, e de novo depois.
- O teste com Keycloak 25 real recusa, antes de usar o segredo: um papel direto a mais, um grupo com `manage-realm`, um papel composto com `manage-clients` e um cliente `client-jwt`.

## 1. Contexto: o que acontece hoje

- Em produção o login é só pelo Keycloak (`OMNIRA_AUTH_MODE=oidc`, realm `omnira`, `registrationAllowed=false`). O OMNIRA **não** chama a API de administração do Keycloak.
- O e-mail do convite manda uma **senha temporária do OMNIRA** (hash em `membership_invitations.temporary_password_hash`, migration 081) e diz "ao fazer login você será obrigado a criar uma nova senha". O Keycloak não conhece essa senha e não deixa a pessoa criar conta. Resultado: **todo convidado novo vê "Invalid username or password"**.
- Em 2026-10-14 isso foi contornado criando contas à mão com o admin master do Keycloak, que estava com a senha padrão e com o console aberto na internet (senha trocada no mesmo dia; bloquear `/admin/` no nginx ainda é pendência do dono).
- A autorização por e-mail do Hub (ADR-0039 §3.10, "vale no primeiro acesso") tem o mesmo buraco: quem não tem conta não consegue fazer o primeiro acesso.
- Pôr alguém para atender exige três momentos separados: convidar, esperar o aceite, depois ligar "Recebe conversas" e escolher as filas na ficha.

## 2. Decisão proposta

### 2.1 O OMNIRA cria a conta de login (modo OIDC, atrás de `OMNIRA_INVITE_PROVISION_LOGIN`, desligada por padrão)

1. Cliente confidencial novo no realm `omnira`: `omnira-provisioner`, com conta de serviço que tem **somente** `realm-management: view-users` e `manage-users`. Nunca o realm master, nunca `manage-realm`/`manage-clients`. Segredo em `.env` (`OMNIRA_KEYCLOAK_PROVISIONER_SECRET`, arquivo 600); a API fala com o Keycloak pela rede interna (`http://keycloak:8888`).
2. Ao criar um convite, a API procura a conta pelo e-mail exato:
   - **Existe:** não toca em senha, e-mail nem atributos. O e-mail do convite diz "entre com a sua conta de sempre".
   - **Não existe:** cria `{username=email, email, emailVerified=true, enabled=true, attributes.omnira_invitation=<id do convite>}` com uma senha temporária (`temporary=true`). Essa é a senha que vai no e-mail: o texto atual passa a ser verdade, e o Keycloak obriga a trocá-la no primeiro login.
3. **Reenviar** só redefine a senha de contas criadas por **este** convite (atributo) e que **nunca entraram**. Qualquer outra conta não é tocada.
4. **Cancelar ou vencer** um convite cuja conta nunca entrou **desabilita** essa conta (não apaga: fica auditável).
5. Falha do Keycloak ao criar o convite: o convite é registrado como hoje, a resposta diz "o login não foi criado" e a ficha do convite oferece "Criar o login de novo".
6. Auditoria sem segredo: `invitation.login_created`, `invitation.login_reset`, `invitation.login_disabled`.

### 2.2 O convite leva a função e as filas

1. Coluna `membership_invitations.attends boolean not null default false` e tabela `membership_invitation_queues(tenant_id, invitation_id, queue_id, capacity)` com chaves estrangeiras **compostas** `(tenant_id, invitation_id)` e `(tenant_id, queue_id)` (fila de outra instância é recusada pelo banco), `ON DELETE CASCADE`, RLS `FORCE` com as mesmas regras do convite (ler = `can_read_invitation`; escrever = `membership.manage` e `agent.manage` **na instância do convite**, já com a correção da migration 135).
2. API: `POST /tenants/{t}/team/invitations` aceita `attends` e `queues: [{queue_id, capacity}]` (opcionais; com filas exige também `agent.manage`). `GET` devolve o plano. `PATCH` do plano enquanto o convite estiver pendente. OpenAPI atualizado.
3. Aceite: na **mesma transação de sistema** que já cria o vínculo (`WithTenantSession(..., true)`), se `attends`, cria ou ativa o perfil de agente e as entradas de fila do plano (fila apagada é ignorada), com auditoria `agent.enabled` e `queue.member_added` atribuída a quem convidou.
4. Tela: o passo "De onde recebe conversas" do protótipo (Adicionar pessoa) e a ficha do convite com função e filas editáveis até o aceite.

## 3. Alternativas

- **B. Abrir o cadastro no Keycloak** (`registrationAllowed=true` com verificação de e-mail). Sem credencial de administração no OMNIRA, mas cria uma página pública de cadastro, manda dois e-mails, depende de SMTP no Keycloak e qualquer pessoa cria conta (o OMNIRA só daria acesso com convite).
- **C. Criar a conta à mão no console do Keycloak** (o estado atual). Provou ser fonte de erro e levou ao uso do admin master.
- **D. Variação de A com `execute-actions-email`**: o Keycloak manda o próprio e-mail para a pessoa definir a senha. Evita senha temporária, mas exige SMTP no Keycloak e a pessoa recebe dois e-mails.

Recomendação: **A**.

## 4. Segurança

- `manage-users` permite redefinir a senha de qualquer usuário do realm, inclusive administradores. Por isso o adaptador só redefine contas com `omnira_invitation` igual ao convite e sem login, e nunca altera e-mail ou username de conta existente. Se o Keycloak da versão em uso permitir, restringir o cliente a um grupo `omnira-convidados` (permissões finas).
- `emailVerified=true` na criação: a posse do endereço é provada pela entrega da senha temporária, o mesmo risco que já existe hoje (convite para o endereço errado).
- Pendência que fica mais relevante: hoje a pessoa pode editar o próprio e-mail no Keycloak. Recomendação: e-mail editável só por administrador.
- Nenhum token ou segredo em fila. Nenhuma chamada ao Keycloak real em testes.

## 5. Testes e gate

- Adaptador do Keycloak contra um servidor falso (httptest): cria, não toca conta existente, só redefine com atributo e sem login, desabilita ao cancelar.
- Postgres com instâncias A/B: fila de B não entra no plano de um convite de A (FK composta), o plano é aplicado no aceite, convite cancelado não aplica, fila apagada é ignorada, supervisor sem `membership.manage` não convida.
- E2E com Keycloak descartável (`scripts/e2e-hub-keycloak.sh`): convidar, e-mail no sink, login com a senha temporária, troca obrigatória, aceite, e a pessoa já recebe conversas da fila escolhida.
- Revisão do Codex (CRITICAL/HIGH bloqueiam). Flag desligada até o dono testar com um e-mail real em LAB.

## 6. Decisões do dono

1. Caminho A (o OMNIRA cria a conta) ou B (cadastro aberto)?
2. Criar a conta também quando o admin do Hub autoriza um e-mail sem conta (ADR-0039 §3.10)?
3. Conta de convite cancelado que nunca entrou: desabilitar (recomendado) ou apagar?
4. E-mail editável só por administrador no Keycloak (recomendado)?
