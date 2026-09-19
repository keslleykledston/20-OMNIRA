-- Grants mínimos nas funções de RLS.
--
-- Por padrão o PostgreSQL concede EXECUTE em novas funções a PUBLIC. Isso
-- não é um risco de escalonamento de privilégio aqui (as funções só leem
-- memberships/roles ou current_setting), mas viola o princípio de menor
-- privilégio: qualquer role futura no banco poderia chamá-las sem
-- necessidade. Restringe explicitamente a quem de fato precisa.
REVOKE EXECUTE ON FUNCTION current_user_id() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION is_system_admin() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION has_active_membership(UUID, UUID) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION has_active_admin_membership(UUID, UUID) FROM PUBLIC;

GRANT EXECUTE ON FUNCTION current_user_id() TO omnira_app;
GRANT EXECUTE ON FUNCTION is_system_admin() TO omnira_app;
GRANT EXECUTE ON FUNCTION has_active_membership(UUID, UUID) TO omnira_app;
GRANT EXECUTE ON FUNCTION has_active_admin_membership(UUID, UUID) TO omnira_app;
