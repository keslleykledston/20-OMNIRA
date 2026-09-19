-- tools/check-rls (TestRLSCompleteness) detectou que "roles" tem coluna
-- tenant_id (roles podem ser tenant-scoped, além dos system roles com
-- tenant_id NULL) mas nunca recebeu FORCE ROW LEVEL SECURITY. A policy
-- roles_read_public já é `USING (TRUE)` — leitura deliberadamente pública
-- — então FORCE aqui não muda comportamento algum; só remove a
-- inconsistência de a tabela ficar de fora do invariante "toda tabela
-- tenant-owned tem FORCE RLS", que o scanner de completude verifica.
ALTER TABLE roles FORCE ROW LEVEL SECURITY;
