-- ADR-0018 Wave 5: what KIND of conversation this is, and the internal (staff) side of a 1:1 conversation.
--   internal          every known human is a verified internal user
--   customer_service  at least one participant is a customer (agents being present does NOT make it internal)
--   external_other    external people, none a customer, none unclassified
--   unclassified      at least one external participant is not classified yet (or an identity conflict is open)
-- There is no "mixed": has_unclassified_participants flags a customer conversation that also has an unclassified person.
ALTER TABLE conversations ADD COLUMN conversation_kind TEXT NOT NULL DEFAULT 'unclassified'
  CHECK (conversation_kind IN ('internal','customer_service','external_other','unclassified'));
ALTER TABLE conversations ADD COLUMN has_unclassified_participants BOOLEAN NOT NULL DEFAULT false;
-- A 1:1 conversation with a VERIFIED internal user has no Contact (a Contact is an external person, never created for staff).
ALTER TABLE conversations ADD COLUMN internal_user_id UUID;
ALTER TABLE conversations ADD CONSTRAINT conversations_internal_user_fk
  FOREIGN KEY (tenant_id, internal_user_id) REFERENCES memberships(tenant_id, user_id) ON DELETE RESTRICT;
ALTER TABLE conversations ALTER COLUMN contact_id DROP NOT NULL;
ALTER TABLE conversations ADD CONSTRAINT conversations_party_chk CHECK ((contact_id IS NOT NULL) <> (internal_user_id IS NOT NULL));
ALTER TABLE conversations ADD CONSTRAINT conversations_internal_kind_chk CHECK ((conversation_kind = 'internal') = (internal_user_id IS NOT NULL));
CREATE UNIQUE INDEX conversations_open_internal_uq ON conversations (tenant_id, internal_user_id, channel_connection_id)
  WHERE status = 'open' AND internal_user_id IS NOT NULL AND channel_connection_id IS NOT NULL;
CREATE INDEX conversations_kind_idx ON conversations (tenant_id, conversation_kind, updated_at DESC, id DESC);

-- The kind of a 1:1 conversation with an external contact, from the contact's classification (spam is "other" for this
-- purpose: no customer automation either way). Kept in ONE place so SQL and Go agree (tested).
CREATE FUNCTION contact_kind_to_conversation_kind(contact_kind TEXT) RETURNS TEXT LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE contact_kind WHEN 'customer' THEN 'customer_service' WHEN 'unclassified' THEN 'unclassified' ELSE 'external_other' END
$$;
UPDATE conversations c SET conversation_kind = contact_kind_to_conversation_kind(ct.kind)
FROM contacts ct WHERE ct.tenant_id = c.tenant_id AND ct.id = c.contact_id;
UPDATE conversations SET has_unclassified_participants = (conversation_kind = 'unclassified');

-- Groups (ADR-0015 keeps them apart from conversations) get the same derived fields, computed from their participants.
ALTER TABLE wa_groups ADD COLUMN conversation_kind TEXT NOT NULL DEFAULT 'unclassified'
  CHECK (conversation_kind IN ('internal','customer_service','external_other','unclassified'));
ALTER TABLE wa_groups ADD COLUMN has_unclassified_participants BOOLEAN NOT NULL DEFAULT false;

-- The deterministic rule in ONE place (SQL); internal/conversations/domain.ClassifyKind is its twin and a test compares them.
CREATE FUNCTION conversation_kind_from_counts(n_internal INT, n_customer INT, n_other INT, n_unclassified INT) RETURNS TEXT
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE
    WHEN n_internal + n_customer + n_other + n_unclassified = 0 THEN 'unclassified'
    WHEN n_customer > 0 THEN 'customer_service'
    WHEN n_unclassified > 0 THEN 'unclassified'
    WHEN n_other > 0 THEN 'external_other'
    ELSE 'internal' END
$$;

-- Recompute the kind of every 1:1 conversation of ONE contact (after a reclassification or an identity conflict opens or
-- closes). An open identity conflict makes the contact's conversations "unclassified": nobody knows yet who this is.
-- Does not touch updated_at (the Inbox order must not change). Returns how many conversations changed.
CREATE FUNCTION recompute_contact_conversation_kinds(p_tenant UUID, p_contact UUID) RETURNS INT
LANGUAGE sql AS $$
  WITH k AS (
    SELECT CASE WHEN EXISTS (SELECT 1 FROM identity_resolution_conflicts x WHERE x.tenant_id = p_tenant AND x.contact_id = p_contact AND x.status = 'open')
                THEN 'unclassified' ELSE contact_kind_to_conversation_kind(ct.kind) END AS kind
    FROM contacts ct WHERE ct.tenant_id = p_tenant AND ct.id = p_contact
  ), u AS (
    UPDATE conversations c SET conversation_kind = k.kind, has_unclassified_participants = (k.kind = 'unclassified')
    FROM k
    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact AND c.internal_user_id IS NULL
      AND (c.conversation_kind <> k.kind OR c.has_unclassified_participants <> (k.kind = 'unclassified'))
    RETURNING 1)
  SELECT count(*)::int FROM u
$$;

-- Recompute the kind of ONE group from the participants seen writing in it. Each participant counts individually:
--   internal     a VERIFIED internal identity (provider participant of this connection, or the phone a "<digits>@c.us" id
--                carries) of an ACTIVE member, with no open conflict;
--   customer / other / unclassified   from the classification of the contact the participant is bound to;
--   unclassified an unbound participant (never guessed).
CREATE FUNCTION recompute_group_kind(p_tenant UUID, p_group UUID) RETURNS TEXT
LANGUAGE plpgsql AS $$
DECLARE n_i INT; n_c INT; n_o INT; n_u INT; v_kind TEXT;
BEGIN
  WITH parts AS (
    SELECT DISTINCT cp.id, cp.tenant_id, cp.contact_id, cp.provider, cp.channel_connection_id, cp.external_participant_id
    FROM wa_group_messages m
    JOIN channel_participants cp ON cp.tenant_id = m.tenant_id AND cp.id = m.sender_channel_participant_id
    WHERE m.tenant_id = p_tenant AND m.group_id = p_group
  ), classed AS (
    SELECT CASE
      WHEN EXISTS (
        SELECT 1 FROM user_channel_identities i
        JOIN memberships mb ON mb.tenant_id = i.tenant_id AND mb.user_id = i.user_id AND mb.status = 'active'
        WHERE i.tenant_id = p.tenant_id AND i.status = 'verified'
          AND ((i.identity_type = 'provider_participant' AND i.scope = lower(p.provider) || ':' || p.channel_connection_id::text
                AND i.normalized_value = lower(p.external_participant_id))
            OR (i.identity_type = 'phone' AND p.external_participant_id ~ '^[0-9]{8,15}@c\.us$'
                AND i.normalized_value = '+' || split_part(p.external_participant_id, '@', 1)))
          AND NOT EXISTS (SELECT 1 FROM identity_resolution_conflicts x WHERE x.tenant_id = i.tenant_id AND x.identity_id = i.id AND x.status = 'open')
      ) THEN 'internal'
      WHEN p.contact_id IS NOT NULL THEN
        CASE WHEN EXISTS (SELECT 1 FROM identity_resolution_conflicts x WHERE x.tenant_id = p.tenant_id AND x.contact_id = p.contact_id AND x.status = 'open') THEN 'unclassified'
        ELSE (SELECT CASE ct.kind WHEN 'customer' THEN 'customer' WHEN 'unclassified' THEN 'unclassified' ELSE 'other' END
              FROM contacts ct WHERE ct.tenant_id = p.tenant_id AND ct.id = p.contact_id) END
      ELSE 'unclassified' END AS cls
    FROM parts p
  )
  SELECT count(*) FILTER (WHERE cls = 'internal'), count(*) FILTER (WHERE cls = 'customer'),
         count(*) FILTER (WHERE cls = 'other'), count(*) FILTER (WHERE cls IS NULL OR cls = 'unclassified')
  INTO n_i, n_c, n_o, n_u FROM classed;
  v_kind := conversation_kind_from_counts(n_i, n_c, n_o, n_u);
  UPDATE wa_groups SET conversation_kind = v_kind, has_unclassified_participants = (n_u > 0)
  WHERE tenant_id = p_tenant AND id = p_group AND (conversation_kind <> v_kind OR has_unclassified_participants <> (n_u > 0));
  RETURN v_kind;
END $$;

-- Groups whose participants are bound to one contact (after a reclassification), and every group of a tenant (after an
-- identity changes: it is rare and cheap enough, and never guesses which group an identity touches).
CREATE FUNCTION recompute_groups_for_contact(p_tenant UUID, p_contact UUID) RETURNS INT
LANGUAGE plpgsql AS $$
DECLARE g UUID; n INT := 0;
BEGIN
  FOR g IN SELECT DISTINCT m.group_id FROM wa_group_messages m
           JOIN channel_participants cp ON cp.tenant_id = m.tenant_id AND cp.id = m.sender_channel_participant_id
           WHERE m.tenant_id = p_tenant AND cp.contact_id = p_contact LOOP
    PERFORM recompute_group_kind(p_tenant, g); n := n + 1;
  END LOOP;
  RETURN n;
END $$;

CREATE FUNCTION recompute_tenant_group_kinds(p_tenant UUID) RETURNS INT
LANGUAGE plpgsql AS $$
DECLARE g UUID; n INT := 0;
BEGIN
  FOR g IN SELECT id FROM wa_groups WHERE tenant_id = p_tenant LOOP
    PERFORM recompute_group_kind(p_tenant, g); n := n + 1;
  END LOOP;
  RETURN n;
END $$;

-- Backfill: existing groups from their participants (none is internal yet: no identity is verified).
DO $$
DECLARE r RECORD;
BEGIN
  FOR r IN SELECT tenant_id, id FROM wa_groups LOOP
    PERFORM recompute_group_kind(r.tenant_id, r.id);
  END LOOP;
END $$;
