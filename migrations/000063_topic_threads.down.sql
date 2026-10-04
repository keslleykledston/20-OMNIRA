DELETE FROM role_permissions WHERE permission_key IN ('topic.read','topic.manage');
DELETE FROM permissions WHERE key IN ('topic.read','topic.manage');
DROP TABLE IF EXISTS topic_summaries;
DROP TABLE IF EXISTS topic_ticket_links;
DROP TABLE IF EXISTS topic_conversation_links;
DROP TABLE IF EXISTS message_topic_links;
DROP TABLE IF EXISTS topic_threads;
