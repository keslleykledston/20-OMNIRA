-- ADR-0043 S3 (owner decision 2026-10-17: "o agente só vê a fila de que participa; administrador e supervisor veem tudo").
--
-- Visibility of conversations by queue, ONE decision in SQL used by every surface (route middleware, lists, streams):
--   * the capability `sector_scope` (tenant_entitlements, ADR-0038, starts OFF: turning it on changes what people see, so it is the owner's call per company);
--   * the key `conversation.read_all_queues`, held by administrators and supervisors (global roles) and by no one else by default;
--   * a person without the key sees: conversations without a queue, the ones assigned to them, the ones of a queue where they are an ACTIVE member, and the ones
--     they were invited to or attend as co-attendee. Everything else answers as if it did not exist (404 on routes, absent from lists).
-- The Hub keeps its own scope (contract queue_ids, acting_scope()); this does not touch the delegated path.
INSERT INTO permissions(key, description) VALUES
  ('conversation.read_all_queues', 'See the conversations of every queue of the company (without it, only the queues the person is an active member of, their own and the unqueued ones, when the company turned the sector scope on)')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, 'conversation.read_all_queues' FROM roles r
WHERE r.tenant_id IS NULL AND r.key IN ('tenant_admin','tenant_supervisor')
ON CONFLICT DO NOTHING;

-- Does the sector scope restrict THIS person in THIS company? (capability on AND no key). A system session is never restricted.
CREATE OR REPLACE FUNCTION sector_scope_restricts(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE((SELECT e.enabled FROM tenant_entitlements e WHERE e.tenant_id = p_tenant_id AND e.capability = 'sector_scope'), false)
     AND NOT COALESCE((SELECT c.sys FROM session_ctx_verified c WHERE c.pid = pg_backend_pid() AND c.xid = pg_current_xact_id_if_assigned()), false)
     AND NOT actor_has_permission(p_tenant_id, p_user_id, 'conversation.read_all_queues');
$$;

-- May this person see this conversation? true when the scope does not restrict them or the conversation is in their reach; false also for an id that does not exist.
CREATE OR REPLACE FUNCTION conversation_in_sector_scope(p_tenant_id UUID, p_user_id UUID, p_conversation_id UUID) RETURNS BOOLEAN
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT CASE WHEN NOT sector_scope_restricts(p_tenant_id, p_user_id)
              THEN EXISTS (SELECT 1 FROM conversations c WHERE c.tenant_id = p_tenant_id AND c.id = p_conversation_id)
              ELSE EXISTS (
                SELECT 1 FROM conversations c
                WHERE c.tenant_id = p_tenant_id AND c.id = p_conversation_id
                  AND (c.queue_id IS NULL
                       OR c.assigned_to_user_id = p_user_id
                       OR EXISTS (SELECT 1 FROM queue_members qm WHERE qm.tenant_id = c.tenant_id AND qm.queue_id = c.queue_id AND qm.user_id = p_user_id AND qm.active)
                       OR EXISTS (SELECT 1 FROM conversation_participants cp WHERE cp.tenant_id = c.tenant_id AND cp.conversation_id = c.id AND cp.user_id = p_user_id AND cp.left_at IS NULL)))
         END;
$$;

REVOKE ALL ON FUNCTION sector_scope_restricts(UUID, UUID) FROM PUBLIC;
REVOKE ALL ON FUNCTION conversation_in_sector_scope(UUID, UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION sector_scope_restricts(UUID, UUID) TO omnira_app;
GRANT EXECUTE ON FUNCTION conversation_in_sector_scope(UUID, UUID, UUID) TO omnira_app;
