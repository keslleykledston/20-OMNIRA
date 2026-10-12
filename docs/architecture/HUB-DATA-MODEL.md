# Hub — modelo de dados (migrations 093–121)

Estado: `LAB`. Complementa `DATA-MODEL.md` e `TENANCY-SECURITY.md`. Fonte de verdade: as migrations; este texto só orienta a leitura. Tudo aqui é **aditivo** sobre o modelo de tenants existente. As tabelas das empresas (conversas, contatos, mensagens…) não ganharam coluna de Hub.

## 1. Tabelas novas

| Tabela | Migration | Papel |
|---|---|---|
| `service_hubs` | 093 | O Hub (uma operação que atende várias empresas). Não é tenant. |
| `hub_memberships` | 093 | Quem trabalha no Hub e com que papel (`hub_admin`, agente). Revogação = linha desativada, nunca apagada. |
| `hub_tenant_service_contracts` | 093 | Contrato Hub × empresa: janela de validade, teto de permissões (`ceiling`), escopo de gestão e filas atendidas. É o **teto**: nada passa dele. |
| `effective_access_grants` | 093 | Concessão de uma pessoa do Hub numa empresa, sob um contrato (modo leitura/resposta, chaves de permissão, validade). |
| `hub_inbox_items` | 093 | Projeção da caixa agregada (preenchida pelo projetor do worker; a lista lê também o vivo, ver §3). |
| `work_pools`, `work_pool_members`, `work_pool_instances` | 093/104 | Equipes e distribuição automática (capacidade por pessoa, instâncias servidas). `skills`/`agent_skills` existem mas **não** são usadas por roteamento ponderado (NOT BUILT). |
| `platform_operators` | 099 | Operador da plataforma (único que cria Hub/contrato). |
| `tenant_entitlements`, `company_creation_requests` | 100 | Direitos de empresa e pedidos de criação delegados. |
| `hub_preauthorizations` | 102 | E-mail autorizado antes de existir conta (14 dias; aplicado no primeiro login **verificado**). |
| `permission_domains` | 108 | Domínio (leitura/escrita) de cada chave delegável; chave administrativa/segredo nunca é delegável. |
| `session_ctx_keys`, `session_ctx_mode` (+ registro `session_ctx_verified`, UNLOGGED) | 118 | ADR-0042: chave de assinatura do contexto de sessão e modo (`off`/`measure`/`enforce`). Só o dono do banco lê. |

## 2. Funções (SECURITY DEFINER com `search_path` fixo, ver 096)

- **Identidade/contexto:** `ctx_challenge`, `ctx_begin`, `ctx_verified`, `ctx_signed`, `acting_hub`, `acting_tenant`, `acting_scope`, `lock_served_tenant` (um contexto agindo por transação), `lock_managed_tenant`.
- **Pertencimento e acesso:** `has_active_membership`, `has_active_hub_access`, `has_delegated_access`, `has_hub_manage_access`, `is_hub_admin`, `is_hub_member`, `is_platform_operator`, `user_is_active`, `person_works_in_other_instance`.
- **Escala:** `hub_reach()` (119) — para a pessoa da sessão, os pares (Hub, empresa) vivos, `unrestricted` e `queue_texts`, avaliado **uma vez por consulta** pelas políticas. `has_active_hub_access` continua sendo o **oráculo** dos testes de equivalência.
- **Delegação do servidor:** `delegated_erp_connections`, `delegated_materialize_company_account`, `delegated_permissions`, `delegated_tenants`.

## 3. Como o isolamento é aplicado

1. **Servidor resolve** Hub → contrato → concessão → `EffectiveTenantContext` (direto ou `hub_serve`). O front manda só `X-Omnira-Acting-As: hub:<uuid>`; `tenant_id` de payload nunca autoriza.
2. **RLS FORCE** em toda tabela de empresa; o papel `omnira_app` não é superusuário. Políticas de leitura delegada pedem `hub_reach()`/`acting_scope()`; as de escrita pedem a **chave** no contexto agindo (`actor_has_permission`) e, na fase 04+, **uma instância por vez** (políticas RESTRICTIVE `*_acting_one_instance`, migration 111).
3. **Contexto assinado** (118): sem `ctx_begin` válido na transação, a sessão não enxerga nada em `enforce`.
4. **Escrita de auditoria/outbox** (121): só como a própria pessoa e só para empresas dela (membro, concessão viva ou gerência pelo Hub); `outbox` exige concessão que PODE RESPONDER.
5. **Listas quentes:** a lista da caixa lê dono/estado/fila **ao vivo** das conversas (a projeção atrasa até 60 s); `messages` e tabelas de mídia herdam por `EXISTS` e não carregam pino.

## 4. Reversão

Cada migration tem `.down.sql`. Migrations 093–121 passaram por up/down/up idêntico em 2026-10-12 (`scripts/test-hub-migrations.sh`: desfaz tudo até o esquema anterior ao Hub, reaplica, e confere RLS FORCE em toda tabela com `tenant_id`). `down` de 121 devolve as políticas de INSERT anteriores e o `CREATE` em `public`; ver `HUB-MIGRATION-GUIDE.md`.
