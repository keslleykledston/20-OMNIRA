# ADR-0039 — Painel de Acessos (instâncias, administradores, agentes e permissões) e Conversas unificadas

Status: **aceito** (dono, 2026-10-08) · Implementado em LAB atrás de flag · Complementa o ADR-0038 (fase 2 parcial)

## 1. Problema

"Hub", "tenant", "empresa" e "instância" estavam misturados. O dono pediu um painel **separado** para gerenciar pessoas e
permissões, com regras simples:

1. gerenciar as **instâncias** do OMNIRA;
2. gerenciar os **administradores** de cada instância;
3. gerenciar os **agentes** de cada instância e dizer se cada um atua em **uma** ou em **várias**;
4. o **administrador da instância** gerencia os agentes dela **dentro do OMNIRA**;
5. **só o administrador do Hub** autoriza/convida um agente para **mais de uma** instância.

E, em "Conversas", quem atende mais de uma instância vê **por padrão tudo o que lhe foi liberado**, com filtro de empresas
(todas, uma ou várias) **no lugar do menu de canais**.

## 2. Vocabulário (um só)

| Palavra do dono | No sistema | Onde vive |
|---|---|---|
| Instância | tenant (empresa) | `tenants` |
| Administrador do Hub | `hub_admin` | `hub_memberships` |
| Administrador da instância | membership com papel `tenant_admin` | `memberships` |
| Agente de **uma** instância | membership (`tenant_agent`/`tenant_supervisor`) | `memberships` |
| Agente de **várias** instâncias | `hub_agent` + grants (leitura ou ler-e-responder) | `hub_memberships` + `effective_access_grants` |

Regra de ouro: **vários = grant do Hub**. Um administrador de empresa nunca cria esse vínculo.

## 3. Decisões

1. **Painel separado em `/acessos`**, fora de `/hub` e do workspace do tenant. Flag própria `OMNIRA_HUB_ACCESS_API_ENABLED`
   (precisa de `OMNIRA_HUB_API_ENABLED`). Só aparece para quem é `hub_admin` (`can_manage_access` na lista de Hubs é só dica de
   UI; o servidor decide de novo em toda chamada).
2. **Quem pode usar:** apenas `hub_admin` ativo do Hub do caminho, de um Hub ativo — **não** exige ser operador de plataforma
   (operador é criar empresa/ligar capacidade, ADR-0038). Qualquer outro, inclusive agente do mesmo Hub e administrador de
   uma empresa, recebe **404 uniforme**. A checagem é feita na sessão RLS de quem chama e **de novo dentro da transação de
   escrita** (`access.Service.tx`, `provisioning.guard`).
3. **Uma só definição de "conceder acesso":** o painel escreve por `provisioning.Service` com `provisioning.WithActor(ctx, pessoa)`:
   revalida `is_hub_admin` na transação, grava `audit_events.actor_id` = a pessoa (fim do ator `NULL` do `hubctl` para estas
   ações) e **recusa o que é do `hubctl`**: criar/rebaixar/remover `hub_admin`.
4. **Célula da matriz** = `none | read | reply` por (agente × instância). Definir é ato explícito (pode ampliar: renova um
   grant revogado); validade passa no corpo (`valid_until`, a UI ainda não a expõe). Só em instância **ativa** (contrato e
   empresa ativos).
5. **Administradores da instância:** adicionar = conta já existente (e-mail exato) vira `tenant_admin`; remover = **rebaixa a
   agente da empresa** (não expulsa) e **nunca o último** (linhas bloqueadas `FOR UPDATE`). Instância fora do Hub = 404.
6. **Respostas não viram oráculo:** e-mail inexistente, ambíguo ou inativo → a mesma mensagem 422 (achado L-01 do Codex).
7. **Regra 5 aplicada no servidor, não na UI:** o convite de um administrador de empresa é recusado (409) se o e-mail já
   atua em **outra** instância (membership ativa em outro tenant) **ou** é de algum Hub. A pergunta é respondida por
   `person_works_in_other_instance` (migração 101, `SECURITY DEFINER`, só responde a quem gerencia pessoas da empresa ou à
   sessão de sistema; senão `false`, sem vazar quem trabalha onde). Checada na **criação**, no **reenvio** e no **aceite**;
   falha de consulta = recusa (fail-closed). Vínculos antigos de mais de uma empresa **não** são alterados: aparecem no
   painel como "Também é membro direto de…".
   **Fechado depois da revisão do Codex:** (a) **reativar** uma membership inativa pela tela "Equipe" (`PATCH /team`) passa pela
   mesma pergunta (`user_works_in_other_instance`); (b) "perguntar e escrever" é **atômico por pessoa** (lock consultivo na
   reativação e no aceite), senão dois convites/reativações simultâneos, em empresas diferentes, passariam os dois; (c) "não
   remover o último administrador" é atômico por empresa (lock consultivo compartilhado entre `PATCH /team` e o painel; antes
   dois pedidos sobre administradores diferentes esvaziavam a empresa); (d) o painel lê o papel do Hub **com lock da linha**
   (em duas instruções, porque o recheck do PostgreSQL descarta a linha quando um `JOIN` casou com a versão antiga): uma
   promoção a `hub_admin` pelo `hubctl` nunca é desfeita pelo painel; (e) o ator do painel precisa ser usuário **ativo** na
   própria transação (a autenticação já exige isso a cada pedido; aqui é defesa em profundidade).
8. **Conversas unificadas (decisão do dono, opção A):**
   - Quem atende **uma** instância: tela atual, inalterada (menu de canais incluído).
   - Quem o Hub liberou para **duas ou mais**: "Conversas" mostra **tudo** por padrão, com o filtro de empresas (menu
     suspenso com seleção múltipla) **no lugar** do menu de canais. É a caixa do Hub (`/hub`) reaproveitada: acesso decidido
     por grant + contrato + RLS.
   - O filtro é `?companies=<id,id>` e **só estreita**: empresa sem grant continua invisível seja o que for enviado
     (`tenant_id` segue recusado). A lista de empresas oferecidas vem dos **grants vivos do próprio usuário**.
   - Qualquer falha do Hub → a tela clássica; `?modo=empresa` pede a tela clássica de propósito. "Conversas" nunca fica
     indisponível por causa disto.
   - **Limite assumido:** nesta visão responder por texto e assumir funcionam (ADR-0037); anexo, template, nota e transferir
     ainda **não**. Para isso: "Caixa completa de uma empresa" (`/inbox?modo=empresa`) para quem é membro direto.
9. **Correções dos achados HIGH do Codex feitas junto** (porque a matriz escreve grants): `grant add` repetido **não amplia
   acesso em silêncio** (reativar revogado, tirar/estender validade, subir de leitura para responder exigem `Renew` /
   `hubctl --renew`; a linha do contrato é travada `FOR SHARE` contra a revogação concorrente); e o gate de
   `outbound_attachments` agora vale também no **envio** (`SendMedia`), não só no upload/remoção. O primeiro desenho tinha a
   chave no *sender* errado (o handler usava um sender sem a chave; só o serviço de anexos usava o com a chave): agora a rota
   envia mídia **por** `Attachments.SendMedia`, e há teste pela rota HTTP real com os dois senders distintos, como em produção.

10. **Autorizar pessoa por e-mail, com ou sem conta (2026-10-09, migration 102).** Um só gesto no painel: e-mail + acesso inicial por
    instância (opcional). Se o e-mail já é de **uma** conta ativa, a pessoa vira agente do Hub **na hora**, com exatamente o acesso
    escolhido (as mesmas escritas dos outros botões, pelo mesmo serviço, como o administrador). Se não há conta, a escolha fica
    guardada em `hub_preauthorizations` por **14 dias**, cancelável, e vale no **primeiro acesso** em que o provedor de identidade
    afirma o e-mail como **verificado** (gancho em `ProvisionIdentity`, o login web). **Não há link nem token**: nada para encaminhar
    ou interceptar; confia-se no mesmo que o convite de empresa já confia (e-mail verificado pelo IdP).
    - **A autoridade acompanha a escolha e é conferida de novo ao aplicar:** o administrador que a escreveu precisa ainda ser admin
      ativo de um Hub ativo; senão a autorização vira `void` e nada acontece. Cada empresa é revalidada na hora de aplicar (suspensa
      nesse meio-tempo = pulada e contada, o resto vale). Aplicar é **uma vez** (as linhas pendentes são travadas; dois logins
      simultâneos aplicam uma só vez), nunca depois de vencida ou cancelada, e uma autorização já aplicada não ressuscita se o
      administrador tirar o acesso depois.
    - **Quem é hub_admin não é rebaixado** por ser "convidado como agente" (isso continua do `hubctl`).
    - **Aceito e dito:** o administrador do Hub consegue distinguir "aplicado" de "aguardando" (logo, se um e-mail tem conta). Ele é
      quem cadastra as pessoas deste Hub e a tela lista as duas coisas de qualquer forma; o botão antigo "adicionar por e-mail"
      (só conta existente, resposta uniforme) continua na API.
    - **Risco que depende do provedor de identidade:** se o realm aceitar login com e-mail **não** verificado, nada é aplicado
      (testado), mas o administrador precisa saber que a pessoa só recebe o acesso depois de confirmar o e-mail no Keycloak.
    - Falha ao aplicar nunca bloqueia o login (o erro é registrado; a próxima entrada tenta de novo).
    - **Limite:** aplicar usa uma segunda conexão do pool enquanto segura as linhas pendentes; muitos primeiros logins com
      autorização pendente exatamente ao mesmo tempo disputariam o pool (é um evento por pessoa).

## 4. Fora desta entrega (honesto)

- ~~Convidar pessoa sem conta pelo Hub~~ **feito** em 2026-10-09 (§3.10): por e-mail, sem senha temporária nem link. Continua fora: o
  administrador de uma **empresa** convidar pessoa nova segue pela "Equipe" da empresa, que é outro fluxo.
- Validade (`valid_until`) e filas por agente na tela; pools/skills/distribuição (ADR-0038 fase 4).
- Suspensão ainda não para webhooks, worker de entrega e fluxos (achados H-02/H-03 do Codex): **abertos**.
- Corrida revogação × escrita do reply (H1 do Codex) e o restante do review do reply: **abertos**.
- Gestão de canais/ERP pelo Hub (fase 3).

## 5. Evidência

Ver `docs/architecture/HUB-VERIFICATION-STATUS.md` (seção "Painel de Acessos"). Tudo em Postgres real
(`internal/hub/adapters`, `internal/hub/provisioning`, `internal/tenancy/adapters`), mutantes em
`scripts/test-hub-access-mutations.sh`, telas em Vitest. **Nenhuma** tela foi vista com sessão real do Keycloak.
