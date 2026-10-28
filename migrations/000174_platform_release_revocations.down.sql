DROP TRIGGER IF EXISTS platform_release_revocations_no_delete ON platform_release_revocations;
DROP TRIGGER IF EXISTS platform_release_revocations_no_update ON platform_release_revocations;
DROP FUNCTION IF EXISTS platform_release_revocations_immutable();

DROP TABLE IF EXISTS platform_release_revocation_delivery;
DROP TABLE IF EXISTS platform_release_revocation_requests;
DROP TABLE IF EXISTS platform_release_revocations;
