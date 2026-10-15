-- Owner (2026-10-14): a person may take any number of conversations at once in a queue. Capacity 0 = no limit of simultaneous
-- conversations (routing, the engine and the eligibility rule read it so); 1 to 100 as before. The invitation plan follows the same rule.
ALTER TABLE queue_members DROP CONSTRAINT IF EXISTS queue_members_capacity_check;
ALTER TABLE queue_members ADD CONSTRAINT queue_members_capacity_check CHECK (capacity >= 0 AND capacity <= 100);

ALTER TABLE membership_invitations DROP CONSTRAINT IF EXISTS membership_invitations_queue_capacity_range;
ALTER TABLE membership_invitations ADD CONSTRAINT membership_invitations_queue_capacity_range CHECK (queue_capacity BETWEEN 0 AND 100);
