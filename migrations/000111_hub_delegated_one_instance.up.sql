-- ADR-0040: a delegated request acts for ONE instance, and the database says so too.
--
-- Found when 04a went to production (2026-10-10, read-only proof with the application role): while acting for instance A through hub H, the session
-- still SAW the conversations of instances B and C that the same hub H serves, through the legacy hub-access policy (conversations_read_hub_delegation,
-- phase <= 02) and, by design of the domain policies, the contacts/accounts/media of every instance where the person holds the key through H.
-- Nothing reached a person who could not already see it (the legacy Hub tab shows it, and the code always filters by the instance of the context),
-- but the second barrier did not hold the "one instance at a time" line that the first one does.
--
-- Fix: lock_served_tenant records WHICH instance it locked (app.acting_tenant, transaction-local like app.acting_hub), and a RESTRICTIVE policy on every
-- table a delegate can reach narrows each command to that instance. Fail closed: acting for a hub with no (or an invalid) acting tenant matches nothing.
-- Outside the delegated context (acting_hub() IS NULL) every policy is unchanged, so members and the legacy Hub tab behave exactly as before.

-- Reading the acting tenant safely, like acting_hub(): a text setting the application role can write; not a UUID = no valid context.
CREATE OR REPLACE FUNCTION acting_tenant() RETURNS UUID AS $$
  SELECT CASE WHEN v ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN v::UUID END
  FROM (SELECT NULLIF(current_setting('app.acting_tenant', true), '') AS v) s;
$$ LANGUAGE SQL STABLE SET search_path = pg_catalog, public, pg_temp;

CREATE OR REPLACE FUNCTION lock_served_tenant(p_tenant_id UUID, p_user_id UUID, p_hub_id UUID) RETURNS BOOLEAN AS $$
BEGIN
  IF NOT (p_user_id = public.current_user_id() OR public.is_system_admin()) THEN
    RETURN false;
  END IF;
  IF cardinality(public.delegated_permissions(p_tenant_id, p_user_id, p_hub_id)) = 0 THEN
    RETURN false;
  END IF;
  PERFORM 1 FROM public.tenants WHERE id = p_tenant_id FOR SHARE;
  PERFORM 1 FROM public.service_hubs WHERE id = p_hub_id FOR SHARE;
  PERFORM 1 FROM public.hub_tenant_service_contracts WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id FOR SHARE;
  PERFORM 1 FROM public.hub_memberships WHERE hub_id = p_hub_id AND user_id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.users WHERE id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.effective_access_grants WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id AND user_id = p_user_id FOR SHARE;
  IF cardinality(public.delegated_permissions(p_tenant_id, p_user_id, p_hub_id)) = 0 THEN
    RETURN false;
  END IF;
  PERFORM set_config('app.acting_hub', p_hub_id::TEXT, true);
  PERFORM set_config('app.acting_tenant', p_tenant_id::TEXT, true);
  RETURN true;
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

-- Where the pin goes. conversations and customer_accounts are the two tables whose delegated visibility does NOT hang from another table, so those
-- two are the effective barrier (proven by mutation). Everything else a delegate reads hangs from the conversation under the caller's RLS (messages,
-- the three media tables, contacts and their links all carry an EXISTS on a visible conversation or message), so it is pinned by inheritance; no
-- policy is added on the hot tables (messages, message_media*) because it would add a per-row evaluation to every member request for no extra
-- protection. contacts and contact_account_links are the exception kept on purpose: they are the two tables a delegate can WRITE, so they carry
-- their own pin as a second layer (redundant today, documented as such in scripts/test-hub-pin-mutations.sh).
-- FOR ALL: reads, and the writes 110 opened.
CREATE POLICY conversations_acting_one_instance ON conversations AS RESTRICTIVE
  USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
CREATE POLICY contacts_acting_one_instance ON contacts AS RESTRICTIVE
  USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
CREATE POLICY contact_account_links_acting_one_instance ON contact_account_links AS RESTRICTIVE
  USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
CREATE POLICY customer_accounts_acting_one_instance ON customer_accounts AS RESTRICTIVE
  USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
