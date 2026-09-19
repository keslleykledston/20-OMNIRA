-- Role de aplicação sem privilégios de superuser/bypassrls.
--
-- A conexão da aplicação nunca deve ser superuser: um superuser ignora RLS
-- incondicionalmente (FORCE ROW LEVEL SECURITY não tem efeito sobre ele).
-- A role de migração/admin pode continuar sendo superuser; só a connection
-- string usada pelo binário da aplicação (OMNIRA_DATABASE_URL) deve trocar
-- para esta role.
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'omnira_app') THEN
    CREATE ROLE omnira_app LOGIN PASSWORD 'omnira_app' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
  END IF;
END
$$;

GRANT CONNECT ON DATABASE omnira_dev TO omnira_app;
GRANT USAGE ON SCHEMA public TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO omnira_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO omnira_app;

-- Tabelas criadas por migrations futuras também devem ficar acessíveis
-- a esta role, sem precisar de outra migration manual de GRANT.
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO omnira_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO omnira_app;
