-- ADR-0058 addendum: the alert channel (the one real send exception in the whole product) must be an
-- internal group — Interno · Gestão or Interno · Equipe — never a client/partner/supplier group, even one
-- with no customer account linked. Same defense-in-depth layer as wa_groups_alert_channel_not_client_group:
-- the app already validates this in SetEnabled, this CHECK makes it true no matter what writes the row.
ALTER TABLE wa_groups ADD CONSTRAINT wa_groups_alert_channel_internal_purpose_only
  CHECK (NOT is_alert_channel OR purpose IN ('internal_management', 'internal_team'));
