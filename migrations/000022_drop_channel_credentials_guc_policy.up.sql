-- 000020 added a policy keyed on the app.tenant_id GUC, which the runtime never
-- sets (isolation uses current_user_id()/membership policies from 000010).
-- current_setting('app.tenant_id') without missing_ok raises for every query
-- as omnira_app, so the policy is removed.
DROP POLICY IF EXISTS channel_credentials_tenant_policy ON channel_credentials;
