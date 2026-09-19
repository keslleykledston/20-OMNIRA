-- Row-level security policies
--
-- IMPORTANTE: as funções helper precisam existir ANTES de qualquer policy
-- que as referencie (CREATE POLICY ... USING (current_user_id() ...) falha
-- com "function does not exist" se a function ainda não foi criada nesta
-- mesma migration/conexão).

-- Helper functions for RLS
CREATE OR REPLACE FUNCTION current_user_id() RETURNS UUID AS $$
  SELECT current_setting('app.current_user_id', TRUE)::UUID;
$$ LANGUAGE SQL STABLE;

CREATE OR REPLACE FUNCTION is_system_admin() RETURNS BOOLEAN AS $$
  SELECT COALESCE(current_setting('app.is_system_admin', TRUE)::BOOLEAN, FALSE);
$$ LANGUAGE SQL STABLE;

-- has_active_membership / has_active_admin_membership são SECURITY DEFINER:
-- rodam com os privilégios de quem definiu a função (bypassando RLS
-- internamente), não de quem chama. Isso é necessário porque a policy de
-- "memberships" não pode fazer um EXISTS direto na própria tabela
-- memberships — isso reativa a mesma policy recursivamente e o Postgres
-- aborta com "infinite recursion detected in policy". A função encapsula
-- essa checagem fora do contexto de RLS, quebrando o ciclo.
CREATE OR REPLACE FUNCTION has_active_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT EXISTS (
    SELECT 1 FROM memberships
    WHERE tenant_id = p_tenant_id AND user_id = p_user_id AND status = 'active'
  );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = public;

CREATE OR REPLACE FUNCTION has_active_admin_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT EXISTS (
    SELECT 1 FROM memberships m
    JOIN roles r ON m.role_id = r.id
    WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id
    AND m.status = 'active' AND r.key = 'tenant_admin'
  );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = public;

-- Users: only system admins and self can access
CREATE POLICY users_read_self ON users FOR SELECT
  USING (current_user_id() = id OR is_system_admin());

CREATE POLICY users_update_self ON users FOR UPDATE
  USING (current_user_id() = id OR is_system_admin())
  WITH CHECK (current_user_id() = id OR is_system_admin());

-- Tenants: accessible to members and system admins
CREATE POLICY tenants_read_members ON tenants FOR SELECT
  USING (has_active_membership(id, current_user_id()) OR is_system_admin());

-- Criação de tenant permanece em admin/internal (ver docs/product/MVP.md);
-- só system admin pode inserir.
CREATE POLICY tenants_insert_admin ON tenants FOR INSERT
  WITH CHECK (is_system_admin());

CREATE POLICY tenants_update_admin ON tenants FOR UPDATE
  USING (has_active_admin_membership(id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_admin_membership(id, current_user_id()) OR is_system_admin());

-- Memberships: accessible within tenant or by system admin.
-- Usa as funções SECURITY DEFINER acima em vez de EXISTS direto na própria
-- tabela (ver comentário acima sobre recursão infinita de RLS).
CREATE POLICY memberships_read_tenant ON memberships FOR SELECT
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());

CREATE POLICY memberships_manage_tenant ON memberships FOR INSERT
  WITH CHECK (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());

CREATE POLICY memberships_update_admin ON memberships FOR UPDATE
  USING (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());

-- Audit events: accessible within tenant or by system admin
CREATE POLICY audit_events_read_tenant ON audit_events FOR SELECT
  USING (
    (tenant_id IS NULL AND is_system_admin()) OR
    has_active_membership(tenant_id, current_user_id()) OR
    is_system_admin()
  );

-- Audit events são append-only pela aplicação (nunca por membro comum
-- diretamente) — qualquer sessão com current_user_id() setado pode inserir;
-- a atribuição do actor_id correto é responsabilidade da aplicação.
CREATE POLICY audit_events_insert_authenticated ON audit_events FOR INSERT
  WITH CHECK (current_user_id() IS NOT NULL OR is_system_admin());

-- Outbox events: accessible within tenant
CREATE POLICY outbox_events_read_tenant ON outbox_events FOR SELECT
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());

CREATE POLICY outbox_events_insert_authenticated ON outbox_events FOR INSERT
  WITH CHECK (current_user_id() IS NOT NULL OR is_system_admin());

-- Permissions and Roles: readable by all authenticated users
CREATE POLICY permissions_read_public ON permissions FOR SELECT
  USING (TRUE);

CREATE POLICY roles_read_public ON roles FOR SELECT
  USING (TRUE);

CREATE POLICY role_permissions_read_public ON role_permissions FOR SELECT
  USING (TRUE);
