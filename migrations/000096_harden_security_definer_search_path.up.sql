-- Security hardening (found by the Hub adversarial review, reproduced by a test): SECURITY DEFINER functions that
-- run with "SET search_path = public" still search the session's pg_temp schema FIRST for relations. Any session
-- that can run SQL and create TEMP tables (PUBLIC has TEMP on the database by default, so omnira_app does) could
-- create a TEMP table named like a real one (memberships, roles, ...) and make the function read forged rows.
--
-- Fix: pin pg_catalog, public, pg_temp (pg_temp LAST). Function bodies are unchanged and every object they use
-- lives in public or pg_catalog, so behaviour is identical. This migration is independent of the Hub tables.
ALTER FUNCTION has_active_membership(UUID, UUID)       SET search_path = pg_catalog, public, pg_temp;
ALTER FUNCTION has_active_admin_membership(UUID, UUID) SET search_path = pg_catalog, public, pg_temp;
ALTER FUNCTION can_read_invitation(UUID, TEXT)         SET search_path = pg_catalog, public, pg_temp;
ALTER FUNCTION can_read_tenant_peer(UUID)              SET search_path = pg_catalog, public, pg_temp;
ALTER FUNCTION inbox_message_persisted_event()         SET search_path = pg_catalog, public, pg_temp;
ALTER FUNCTION message_media_analysis_enqueue()        SET search_path = pg_catalog, public, pg_temp;
ALTER FUNCTION message_media_analysis_realtime()       SET search_path = pg_catalog, public, pg_temp;
ALTER FUNCTION message_media_enqueue()                 SET search_path = pg_catalog, public, pg_temp;
ALTER FUNCTION message_media_realtime()                SET search_path = pg_catalog, public, pg_temp;
