-- Rollback: back to the previous (weaker) search_path.
ALTER FUNCTION has_active_membership(UUID, UUID)       SET search_path = public;
ALTER FUNCTION has_active_admin_membership(UUID, UUID) SET search_path = public;
ALTER FUNCTION can_read_invitation(UUID, TEXT)         SET search_path = public;
ALTER FUNCTION can_read_tenant_peer(UUID)              SET search_path = public;
ALTER FUNCTION inbox_message_persisted_event()         SET search_path = public;
ALTER FUNCTION message_media_analysis_enqueue()        SET search_path = public;
ALTER FUNCTION message_media_analysis_realtime()       SET search_path = public;
ALTER FUNCTION message_media_enqueue()                 SET search_path = public;
ALTER FUNCTION message_media_realtime()                SET search_path = public;
