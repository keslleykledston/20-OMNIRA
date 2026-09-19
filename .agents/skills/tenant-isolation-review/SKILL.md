# Skill: tenant-isolation-review

## Quando usar

Antes de mergear qualquer mudança que:
- crie uma tabela nova com coluna `tenant_id` (ou equivalente tenant-owned);
- adicione/altere uma policy RLS existente;
- crie um novo endpoint que leia/escreva dado tenant-owned;
- troque a role de conexão do banco usada pela aplicação;
- adicione uma função `SECURITY DEFINER`.

Contexto: nesta sessão a RLS do OMNIRA ficou inerte por meses sem que
nenhum teste pegasse isso — a role de conexão da aplicação era superuser
com `BYPASSRLS`, então toda policy no catálogo era decorativa. Ver
`docs/research/deskcomm/REUSE-AUDIT.md` para o achado equivalente
encontrado no projeto doador DeskcommCRM (mesma classe de bug, unidade
inteira de teste dedicada a caçá-lo). Esta skill existe para que essa
classe de bug nunca mais dependa de alguém notar manualmente.

## Procedimento

1. Rode a varredura de completude: `bash tools/check-rls.sh`
   - Verifica, via catálogo (`pg_class`/`pg_policies`), que TODA tabela com
     `tenant_id` tem `relrowsecurity=true`, `relforcerowsecurity=true` e
     pelo menos uma policy. Falha nomeando a tabela e o motivo exato.
   - Uma tabela nova sem essas três condições reprova o build — não é
     opcional esperar "depois adiciono RLS".

2. Rode o teste adversarial com role real:
   `docker run ... go test ./internal/tenancy/adapters/... -run TestRLS_KnownUUID_CrossTenantDenied`
   - Executa SQL cru com a role de produção (`omnira_app`), contornando
     os repositórios Go, tentando ler/escrever um tenant por UUID
     conhecido sem ter membership nele. Se este teste passa, o
     isolamento não depende de nenhuma linha de código Go estar correta.

3. Rode `bash tools/test-isolation.sh` (smoke via `docker exec`,
   independente do toolchain Go — útil quando só o Postgres está
   disponível).

4. Se a mudança introduz uma função `SECURITY DEFINER` nova:
   - `search_path` explícito (`SET search_path = public`) — sem isso, a
     função é sequestrável por quem controla o `search_path` da sessão;
   - `REVOKE EXECUTE ... FROM PUBLIC` seguido de `GRANT` só para a role
     que precisa chamá-la (ver migration `000008_rls_function_grants`);
   - a função só deve fazer a checagem pontual que motivou o
     `SECURITY DEFINER` (evitar recursão de RLS) — nunca deve virar um
     canal genérico de bypass.

5. Se a mudança troca/adiciona uma role de conexão:
   - confirmar `NOSUPERUSER NOBYPASSRLS` explicitamente — nunca assumir
     o default;
   - `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = '<role>';`
     deve retornar `f, f`.

6. Se a mudança usa `db.WithTenantSession` (ou equivalente) para setar
   `app.current_user_id`:
   - confirmar que é `set_config(..., true)` (terceiro argumento
     `true` = local à transação) — nunca `SET` sem `LOCAL`/sem o
     `is_local=true`, que vazaria entre requests reusando a mesma
     conexão do pool;
   - confirmar que a conexão é liberada de volta ao pool só depois do
     commit/rollback (nunca reaproveitar a mesma `pgx.Tx` para dois
     requests).

## Checklist de PR (copiar na descrição)

```
- [ ] tools/check-rls.sh passa
- [ ] TestRLS_KnownUUID_CrossTenantDenied passa (ou equivalente para a tabela nova)
- [ ] tools/test-isolation.sh passa
- [ ] nenhuma tabela tenant-owned nova sem FORCE ROW LEVEL SECURITY
- [ ] nenhuma função SECURITY DEFINER sem search_path fixo e grants explícitos
- [ ] nenhuma role de conexão nova sem NOSUPERUSER NOBYPASSRLS confirmado
```

## Não fazer

- adicionar uma tabela tenant-owned "depois eu crio a policy" — sem RLS+FORCE+policy, a tabela nasce com fail-closed (nega tudo) OU, pior, herda um GRANT largo demais e fica lendo sem restrição nenhuma;
- registrar uma tabela em `knownRLSDebt` (ver `internal/platform/db/rls_completeness_test.go`) sem um motivo nomeado e rastreável (issue/ticket) — dívida sem nome é dívida escondida;
- testar isolamento só com a role admin/migração (`omnira`) — ela é superuser e sempre contorna RLS; qualquer teste de isolamento real precisa rodar com a role de aplicação (`omnira_app`);
- copiar uma policy de outro projeto (Deskcomm incluso) sem adaptar ao modelo de `TenantContext`/`Membership` do OMNIRA — RLS "parece" com a de outro projeto não é RLS que funciona aqui.
