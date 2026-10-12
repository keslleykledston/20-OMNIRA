-- Sector entry rule (ADR-0043, S2): a channel line may name the queue ("sector") its NEW conversations go to. NULL keeps the
-- old behaviour (the tenant's default queue). The composite foreign key makes it impossible to point a line at another tenant's
-- queue; RESTRICT makes a queue that still receives a line's conversations undeletable until the line is moved.
ALTER TABLE channel_connections ADD COLUMN default_queue_id UUID;
ALTER TABLE channel_connections
  ADD CONSTRAINT channel_connections_default_queue_fk
  FOREIGN KEY (tenant_id, default_queue_id) REFERENCES queues (tenant_id, id) ON DELETE RESTRICT;
CREATE INDEX idx_channel_connections_default_queue ON channel_connections (tenant_id, default_queue_id) WHERE default_queue_id IS NOT NULL;
