-- FORCE ROW LEVEL SECURITY nas tabelas tenant-owned.
--
-- Sem isto, o dono da tabela (a mesma role usada pela conexão da aplicação)
-- contorna toda política RLS por padrão no PostgreSQL. As políticas criadas
-- em 000004_rls_policies existiam no catálogo mas eram inertes para a
-- aplicação real: current_user_id() nunca era setado por nenhuma query do
-- código Go, e mesmo que fosse, o owner ignoraria a política sem FORCE.
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;
ALTER TABLE memberships FORCE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;
ALTER TABLE audit_events FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox_events FORCE ROW LEVEL SECURITY;
