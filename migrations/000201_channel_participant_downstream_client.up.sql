-- ADR-0015 addendum 3: a empresa vinculada a um participante de grupo (migration 000197) nem sempre é
-- cliente direto do tenant — pode ser cliente do Cliente (ex.: contato da Uplink, que é cliente da 4WNET,
-- que é Cliente do tenant). is_downstream_client marca esse caso: a empresa vinculada é, ela mesma, um
-- cliente do Cliente que o grupo já representa, não a própria empresa do Cliente.
ALTER TABLE channel_participants
  ADD COLUMN is_downstream_client BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE channel_participants
  ADD CONSTRAINT channel_participants_downstream_client_requires_account
    CHECK (NOT is_downstream_client OR company_account_id IS NOT NULL);
