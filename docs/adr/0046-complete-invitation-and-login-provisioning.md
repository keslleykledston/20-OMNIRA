# ADR-0046 — Convite completo: função e filas no convite, e conta de login criada pelo OMNIRA

Status: **PROPOSTA** (2026-10-14), aguarda o dono. Fase 2 do redesenho de Pessoas (a Fase 1 é o commit `a206b91`: uma página de Pessoas com a ficha da pessoa). Nada desta proposta está construído.

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
