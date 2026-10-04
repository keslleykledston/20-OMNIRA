ALTER TABLE group_message_topic_links DROP CONSTRAINT IF EXISTS group_message_topic_links_tenant_id_routing_decision_id_fkey;
ALTER TABLE message_topic_links DROP CONSTRAINT IF EXISTS message_topic_links_tenant_id_routing_decision_id_fkey;
DROP TABLE IF EXISTS conversation_topic_focus;
DROP TABLE IF EXISTS ambiguity_cases;
DROP TABLE IF EXISTS routing_decisions;
DROP TABLE IF EXISTS topic_group_links;
DROP TABLE IF EXISTS group_message_topic_links;
DROP TABLE IF EXISTS topic_entities;
