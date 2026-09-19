-- Fix: after a transaction that used set_config(name, value, true), a pooled connection keeps
-- the custom GUC as an EMPTY STRING (not unset). current_setting(name, true) then returns '' and
-- ''::BOOLEAN / ''::UUID raises 22P02, so any later request on that connection whose policy
-- evaluates is_system_admin()/current_user_id() failed with a 500 (e.g. a non-member probing
-- another tenant) instead of being denied. Treat '' exactly like "unset".
CREATE OR REPLACE FUNCTION current_user_id() RETURNS UUID AS $$
  SELECT NULLIF(current_setting('app.current_user_id', TRUE), '')::UUID;
$$ LANGUAGE SQL STABLE;

CREATE OR REPLACE FUNCTION is_system_admin() RETURNS BOOLEAN AS $$
  SELECT COALESCE(NULLIF(current_setting('app.is_system_admin', TRUE), '')::BOOLEAN, FALSE);
$$ LANGUAGE SQL STABLE;
