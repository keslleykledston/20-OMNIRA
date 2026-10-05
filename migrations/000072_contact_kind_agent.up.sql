-- ADR-0014 amendment: a fourth contact kind, 'agent' = a member of the K3G team who writes to the service number
-- (not a customer). Their conversations stay visible but are never routed to a queue, like spam, and they have no
-- effect on customer-facing counts. Classification stays manual: nothing infers it.
ALTER TABLE contacts DROP CONSTRAINT contacts_kind_check;
ALTER TABLE contacts ADD CONSTRAINT contacts_kind_check CHECK (kind IN ('customer', 'other', 'spam', 'agent'));
