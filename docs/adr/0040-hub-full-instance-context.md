# ADR-0040 — Contexto completo da instância dentro do Hub (acesso delegado a dados operacionais)

Status: **PROPOSTA** (nada implementado; nenhuma migration escrita). Vocabulário de evidência: tudo abaixo é desenho (`NOT WIRED`).
Relaciona: ADR-0036 (Hub dentro do OMNIRA), ADR-0038 (plataforma/gestão), ADR-0039 (painel de acessos, "Conversas" unificada).

## 1. O problema

A "Conversas" do Hub foi entregue só com texto: sem mídia, sem painel do contato, sem classificar/editar contato e sem abrir chamado
no ERP da instância. Isso foi decisão de implementação, não do produto: o que torna o Hub útil é atender **com o contexto completo
da instância** (mídia, cliente, ERP). Sem isso o Hub é uma caixa de entrada com menos recursos que a da instância.

Causa técnica: toda a caixa completa (rotas `/api/v1/tenants/{tenant_id}/...` e o RLS por baixo) só reconhece **membership**.
Quem tem apenas concessão do Hub é recusado em quase tudo. A concessão só foi liberada (políticas `has_active_hub_access`
e `has_hub_manage_access`) em 6 tabelas operacionais/de gestão: `conversations`, `messages`, `audit_events`, `channel_connections`,
`channel_credentials`, `tenant_entitlements` (mais `tenants` e as tabelas do próprio Hub).

## 2. Fatos medidos no código e no catálogo (2026-10-09, só leitura)

- 81 tabelas com RLS ligado. Cerca de 55 usam `has_active_membership(tenant_id, current_user_id())` como único predicado de linha.
  Só `membership_invitations` escreve a regra inline contra `memberships`. Ou seja: **já existe um ponto único de definição**.
- Tabelas só-admin (`has_active_admin_membership`): `tenant_ai_integrations`, `ai_usage`, escrita de `channel_*`, `tenants`, `memberships`.
- A caixa completa usa ~40 rotas de tenant (conversas, mídia, contato, notas, tópicos, resumo, tickets/ERP, finalizar, memória).
- Autorização de aplicação: `AuthorizationMiddleware` -> `AuthorizeAccessToTenant` (membership) e checagem de permissão por handler.
  O Hub já tem o conceito de fonte de acesso (`AccessSource`: Direct, HubManage) e o checker já pergunta ao banco ao vivo.
- Já existem: contrato Hub–instância (`hub_tenant_service_contracts`), concessão efetiva (`effective_access_grants`), avaliação ao vivo
  (`now()`), trava de suspensão (`LockTenantActive`) e atribuição de auditoria (`via=hub`).

## 3. Como sistemas profissionais resolvem (pesquisa)

| Padrão | Exemplos | Ideia |
|---|---|---|
| Relação de delegação do lado do cliente, com papéis mínimos e prazo | Microsoft GDAP/CSP (grupo de segurança do parceiro recebe papéis **no tenant do cliente**; o cliente aceita, define duração de 1 a 730 dias e pode encerrar) | O **tenant dono** guarda a relação e os papéis; o parceiro só mapeia as pessoas dele |
| Assumir papel no destino, sessão curta e atribuída | AWS STS AssumeRole com ExternalId (evita o "confused deputy"); Stripe Connect (plataforma age em nome da conta conectada) | O chamador se autentica como ele mesmo; o servidor valida a relação **para aquele destino** e age com escopo do destino |
| Convidado/colaborador externo com registro no destino | Azure B2B guest, Slack guests, GitHub outside collaborators | Um registro **tipado** (`guest`) no destino, com papel e prazo; some quando a relação acaba |
| ReBAC (Zanzibar/OpenFGA) | OpenFGA: "usuário de outra organização é uma tupla, sem caso especial"; revogar = apagar a tupla | Uma fonte da verdade das relações, consultada ao vivo |

Princípios comuns (e o que adotamos):
1. A relação pertence ao tenant (contrato) e é **consentida, com escopo e prazo**. -> já temos o contrato.
2. **Um único ponto de decisão** "esta pessoa, neste tenant, agora, com este nível". -> um predicado de banco, uma resolução na aplicação.
3. **Papel por dados**, não por `if` espalhado. -> o nível da concessão mapeia para um papel em `roles`/`role_permissions`.
4. **Revogação ao vivo**, sem cópia a sincronizar. -> avaliação por `now()`; nada materializado.
5. **Atribuição** em toda ação ("fulano, via Hub X"). -> `via=hub`, `hub_id` já existem.
6. Não adotar um motor externo (OpenFGA etc.): segunda fonte de verdade e consistência a administrar sem ganho aqui.

## 4. Alternativas

- **A. Predicado único novo `has_tenant_access(tenant, user, nivel)`** = membership ativa **OU** delegação ativa do Hub no nível pedido.
  As políticas das tabelas **operacionais** passam de `has_active_membership` para ele (migração mecânica, uma lista explícita);
  tabelas administrativas e de segredo **não** migram. A aplicação resolve a fonte de acesso no mesmo middleware
  (membership -> `Direct`; senão delegação ao vivo -> `HubServe`), na **mesma URL** `/api/v1/tenants/{tenant_id}/...`.
  Os handlers não mudam; as permissões vêm do papel mapeado pela concessão. **(recomendada)**
- **B. Membership "convidada" gerenciada pelo Hub** (registro tipado no tenant, criado/encerrado na mesma transação da concessão).
  É o padrão de convidados do mercado. Esforço pequeno, mas cria uma segunda representação a manter em sincronia com a concessão
  e expõe o registro ao admin da instância (editar/remover). Aceitável **somente** se o registro for a única fonte e a concessão apenas o convite.
- **C. Rota espelho `/hubs/{hub}/serve/{tenant}/...`** reaproveitando handlers com contexto confiável. Evita editar RLS, mas o banco
  deixa de ser segunda barreira independente (confia no que a aplicação diz), e duplica a superfície de rotas. **Não recomendada.**
- **D. Motor de autorização externo (ReBAC)**. Rejeitada (ver 3.6).

## 5. Decisão proposta: A, em fases por área

Classes de tabela (declaradas em dados; um teste de catálogo falha se uma tabela com `tenant_id` não estiver classificada):
- **operacional** (conversas, mensagens, mídia, contatos, notas, tópicos, resumos, tickets, vínculos CRM, atribuições, fluxos de leitura):
  `has_tenant_access` (SELECT = leitura; INSERT/UPDATE/DELETE = escrita, só com concessão de resposta).
- **administrativa** (equipe, convites, configurações, chaves de IA, consumo, papéis): **só membro**; nunca delegação.
- **segredo** (`channel_credentials`, tokens): inalterado (já tem política própria `has_hub_manage_access`).

Fases (cada uma só avança com testes de acesso cruzado, mutantes e passada do Codex; HIGH/CRITICAL bloqueia):
1. Predicado + classificação + teste de catálogo; migrar conversas, mensagens e **mídia** (leitura).
2. Contato, notas, tópicos, resumo, memória (leitura, depois escrita com `can_reply`: classificar, editar, finalizar).
3. ERP/tickets (criar/atualizar chamado), reconciliação.
4. Lista de instâncias do usuário inclui as delegadas; pessoas do Hub aparecem **somente leitura**, marcadas "Acesso do Hub", na equipe da instância.

## 6. Riscos e perguntas abertas

1. **Raio de impacto**: ~45 tabelas operacionais. Mitigação: lista explícita, teste de catálogo, mutante por tabela.
2. **Privilégio indevido**: superfícies só-membro (equipe, segredos, chaves de IA, auditoria da instância) não podem ser alcançadas por delegação.
3. **Confused deputy**: o `tenant_id` da URL vem do cliente; o servidor tem de provar contrato + concessão para **aquela** instância (o `contract_id` faz o papel do ExternalId).
4. **Revogação**: o banco recusa a próxima requisição (ao vivo). A tela mostra o que já baixou até o próximo refresh (15 s); bloqueio visual no front. Tokens Bearer não rechecam conta (limite já aceito).
5. **Seletores internos**: listas de atendentes, destinos de transferência e presença leem `memberships`/`agent_profiles`. Pessoas delegadas não aparecem lá; transferir entre pessoas do Hub segue pelo fluxo do Hub.
6. **Desempenho**: o predicado roda por linha. Função `STABLE SECURITY DEFINER` com índices e medição antes de liberar.
7. **Papel por concessão**: `can_reply` -> conjunto do `tenant_agent`; só leitura -> conjunto de leitura. Falta decidir se vira `roles` novos (`hub_agent`, `hub_viewer`) ou reusa os atuais.
8. **LGPD/consentimento**: o operador do Hub passa a ver dados de clientes finais da instância. Exige base contratual explícita e trilha visível ao admin da instância (hoje a auditoria é só do Hub).
9. **Precedência**: pessoa que é membro e também tem concessão: membership vence; sem dupla contagem de cota/auditoria.
10. **Fluxos que assumem membership** (Flow Builder, copiloto de IA, notificações): inventariar antes da fase 2.

## 7. Interface (decisão de produto, já tomada)

"Conversas" do Hub passa a ter **abas superiores** (estilo plano), na mesma aba do navegador: **Todas** (triagem unificada) e uma aba por instância
com a caixa completa dela. Revogada a concessão: a aba fica borrada com mensagem "Você não tem mais acesso a esta instância. Fale com o administrador do Hub.",
o cache da instância é descartado, e na próxima carga a aba nem aparece (a lista vem da concessão ativa no servidor).
Até a fase 3 concluir, quem tem acesso **só** pelo Hub continua com a visão reduzida; membros da instância já têm a caixa completa nas abas.

## 8. O que NÃO muda

RLS continua sendo a segunda barreira, `tenant_id` do payload nunca autoriza, nenhuma política é removida, nada de segredo em fila,
suspensão da instância continua esperando o trabalho em andamento.
