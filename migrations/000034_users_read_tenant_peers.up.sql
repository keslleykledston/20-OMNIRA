-- A tela de Equipe e acesso precisa listar nome e e-mail dos membros de um
-- tenant, mas users_read_self (000004) só deixa cada um ler a própria linha.
-- Sem isto o JOIN da listagem de equipe silenciosamente perde toda linha que
-- não é a do próprio ator sob RLS, em vez de falhar de forma visível.
--
-- A regra: o ator só enxerga outro usuário se os dois compartilham um tenant e
-- o ator tem, nesse tenant, o permission_key membership.read — o mesmo que já
-- guarda a leitura de memberships. Não abre a tabela toda: consultar um usuário
-- de um tenant onde o ator não tem membership.read continua invisível.
CREATE OR REPLACE FUNCTION can_read_tenant_peer(p_target_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT EXISTS (
    SELECT 1
    FROM memberships target_m
    JOIN memberships actor_m ON actor_m.tenant_id = target_m.tenant_id
    JOIN role_permissions rp ON rp.role_id = actor_m.role_id
    WHERE target_m.user_id = p_target_user_id
      AND actor_m.user_id = current_user_id()
      AND actor_m.status = 'active'
      AND rp.permission_key = 'membership.read'
  );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = public;

REVOKE ALL ON FUNCTION can_read_tenant_peer(UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION can_read_tenant_peer(UUID) TO omnira_app;

CREATE POLICY users_read_tenant_peers ON users FOR SELECT
  USING (can_read_tenant_peer(id));

-- user_identities nunca teve RLS: a tabela existe desde 000028 sem
-- ENABLE ROW LEVEL SECURITY, e omnira_app tem SELECT/INSERT/UPDATE/DELETE nela
-- pelo ALTER DEFAULT PRIVILEGES de 000006. Até agora nada em runtime a
-- consultava fora de sessão de sistema; a listagem de equipe é o primeiro
-- ponto do produto que lê last_login_at por tenant, e sem RLS a mesma query
-- devolveria a identidade de qualquer usuário do banco, de qualquer tenant.
--
-- Escrita continua exclusiva do fluxo de sistema (ProvisionIdentity/
-- ResolveIdentity rodam sob is_system_admin()); leitura usa a mesma regra de
-- can_read_tenant_peer que acabou de abrir users, mais o próprio dono da linha.
ALTER TABLE user_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_identities FORCE ROW LEVEL SECURITY;

CREATE POLICY user_identities_read ON user_identities FOR SELECT
  USING (user_id = current_user_id() OR is_system_admin() OR can_read_tenant_peer(user_id));
CREATE POLICY user_identities_insert_system ON user_identities FOR INSERT
  WITH CHECK (is_system_admin());
CREATE POLICY user_identities_update_system ON user_identities FOR UPDATE
  USING (is_system_admin()) WITH CHECK (is_system_admin());

-- DELETE nunca é usado (a cascata de users cuida do ciclo de vida) e não
-- consta em expectedPolicyCoverage; revogar mantém o privilégio mínimo real.
REVOKE DELETE ON user_identities FROM omnira_app;
