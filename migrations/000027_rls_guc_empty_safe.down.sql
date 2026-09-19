-- Restore the 000004 definitions (empty-string sensitive).
CREATE OR REPLACE FUNCTION current_user_id() RETURNS UUID AS $$
  SELECT current_setting('app.current_user_id', TRUE)::UUID;
$$ LANGUAGE SQL STABLE;

CREATE OR REPLACE FUNCTION is_system_admin() RETURNS BOOLEAN AS $$
  SELECT COALESCE(current_setting('app.is_system_admin', TRUE)::BOOLEAN, FALSE);
$$ LANGUAGE SQL STABLE;
