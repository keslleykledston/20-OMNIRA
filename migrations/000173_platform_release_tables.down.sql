DROP TRIGGER IF EXISTS platform_update_events_no_delete ON platform_update_events;
DROP TRIGGER IF EXISTS platform_update_events_no_update ON platform_update_events;
DROP FUNCTION IF EXISTS platform_update_events_immutable();

DROP TABLE IF EXISTS platform_component_versions;
DROP TABLE IF EXISTS platform_backup_record_requests;
DROP TABLE IF EXISTS platform_backup_records;
DROP TABLE IF EXISTS platform_update_events;
DROP TABLE IF EXISTS platform_update_runs;
DROP TABLE IF EXISTS platform_update_run_requests;
DROP TABLE IF EXISTS platform_update_plan_requests;
DROP TABLE IF EXISTS platform_update_plans;
DROP TABLE IF EXISTS platform_release_ingest_requests;
DROP TABLE IF EXISTS platform_releases;
DROP TABLE IF EXISTS platform_installation_version_requests;
DROP TABLE IF EXISTS platform_installations;
