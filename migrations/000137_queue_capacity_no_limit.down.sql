-- Back to 1..100. A seat or a plan with no limit (0) becomes 100, the largest limit the old rule allows, so nobody stops receiving.
UPDATE queue_members SET capacity = 100 WHERE capacity = 0;
UPDATE membership_invitations SET queue_capacity = 100 WHERE queue_capacity = 0;
ALTER TABLE queue_members DROP CONSTRAINT IF EXISTS queue_members_capacity_check;
ALTER TABLE queue_members ADD CONSTRAINT queue_members_capacity_check CHECK (capacity > 0 AND capacity <= 100);
ALTER TABLE membership_invitations DROP CONSTRAINT IF EXISTS membership_invitations_queue_capacity_range;
ALTER TABLE membership_invitations ADD CONSTRAINT membership_invitations_queue_capacity_range CHECK (queue_capacity BETWEEN 1 AND 100);
