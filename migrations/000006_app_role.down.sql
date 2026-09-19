REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM omnira_app;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM omnira_app;
REVOKE USAGE ON SCHEMA public FROM omnira_app;
DO $$ BEGIN EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM omnira_app', current_database()); END $$;

-- Roles PostgreSQL são cluster-wide. Não tentar DROP ROLE durante rollback
-- de uma base: a role pode estar sendo usada por outra database do mesmo
-- cluster, fazendo o rollback falhar por dependências externas. Provisioning
-- de role é responsabilidade do ambiente; remoção explícita, se necessária,
-- ocorre em operação separada após auditoria de dependências.
