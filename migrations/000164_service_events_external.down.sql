SET LOCAL lock_timeout = '5s';
DROP FUNCTION IF EXISTS external_installation_has_scope(UUID, UUID, TEXT);
DROP TRIGGER IF EXISTS domain_events_service_context_trg ON context_events;
DROP FUNCTION IF EXISTS domain_events_service_context();
-- the journal keeps no service facts after this: they are removed with their deliveries (the constraints below could not hold them)
DELETE FROM webhook_deliveries d USING domain_events e WHERE e.id = d.event_id AND e.event_type IN ('service_session.opened.v1', 'service_session.classified.v1', 'service_session.status_changed.v1', 'service_session.closed.v1', 'service_session.reopened.v1', 'service_case.created.v1', 'service_case.updated.v1', 'service_case.resolved.v1', 'service_case.assigned.v1', 'service_case.session_linked.v1', 'service_task.created.v1', 'service_task.updated.v1', 'service_task.status_changed.v1', 'service_task.completed.v1', 'service_task.assigned.v1', 'external_record.linked.v1', 'external_record.unlinked.v1', 'external_record.status_changed.v1', 'external_request.created.v1', 'external_request.state_changed.v1');
DELETE FROM domain_events WHERE event_type IN ('service_session.opened.v1', 'service_session.classified.v1', 'service_session.status_changed.v1', 'service_session.closed.v1', 'service_session.reopened.v1', 'service_case.created.v1', 'service_case.updated.v1', 'service_case.resolved.v1', 'service_case.assigned.v1', 'service_case.session_linked.v1', 'service_task.created.v1', 'service_task.updated.v1', 'service_task.status_changed.v1', 'service_task.completed.v1', 'service_task.assigned.v1', 'external_record.linked.v1', 'external_record.unlinked.v1', 'external_record.status_changed.v1', 'external_request.created.v1', 'external_request.state_changed.v1');
UPDATE webhook_subscriptions SET event_types = ARRAY(SELECT t FROM unnest(event_types) t WHERE t IN ('conversation.created.v1', 'conversation.control_changed.v1', 'conversation.closed.v1', 'message.created.v1', 'message.status_changed.v1', 'contact.updated.v1', 'ticket.linked.v1'))
 WHERE NOT event_types <@ ARRAY['conversation.created.v1', 'conversation.control_changed.v1', 'conversation.closed.v1', 'message.created.v1', 'message.status_changed.v1', 'contact.updated.v1', 'ticket.linked.v1']::TEXT[];
DELETE FROM webhook_subscriptions WHERE cardinality(event_types) = 0;
-- an installation never disappears with this rollback: the scope is taken away; one whose ONLY scope it was keeps events.read and is suspended (it can do nothing
-- until an administrator reviews it)
UPDATE external_app_installations SET scopes = array_remove(scopes, 'service.read') WHERE 'service.read' = ANY (scopes) AND cardinality(scopes) > 1;
UPDATE external_app_installations SET scopes = ARRAY['events.read'], status = CASE WHEN status = 'revoked' THEN status ELSE 'suspended' END,
       suspended_at = CASE WHEN status = 'revoked' THEN suspended_at ELSE COALESCE(suspended_at, now()) END
 WHERE 'service.read' = ANY (scopes);
ALTER TABLE webhook_subscriptions DROP CONSTRAINT webhook_subscriptions_event_types_check;
ALTER TABLE webhook_subscriptions ADD CONSTRAINT webhook_subscriptions_event_types_check
  CHECK (cardinality(event_types) BETWEEN 1 AND 7 AND event_types <@ ARRAY['conversation.created.v1', 'conversation.control_changed.v1', 'conversation.closed.v1', 'message.created.v1', 'message.status_changed.v1', 'contact.updated.v1', 'ticket.linked.v1']::TEXT[]);
ALTER TABLE domain_events DROP CONSTRAINT domain_events_aggregate_type_check;
ALTER TABLE domain_events ADD CONSTRAINT domain_events_aggregate_type_check CHECK (aggregate_type IN ('conversation', 'message', 'contact', 'ticket'));
ALTER TABLE domain_events DROP CONSTRAINT domain_events_event_type_check;
ALTER TABLE domain_events ADD CONSTRAINT domain_events_event_type_check CHECK (event_type IN ('conversation.created.v1', 'conversation.control_changed.v1', 'conversation.closed.v1', 'message.created.v1', 'message.status_changed.v1', 'contact.updated.v1', 'ticket.linked.v1'));
ALTER TABLE external_app_installations DROP CONSTRAINT external_app_installations_scopes_check;
ALTER TABLE external_app_installations ADD CONSTRAINT external_app_installations_scopes_check
  CHECK (cardinality(scopes) BETWEEN 1 AND 16
     AND scopes <@ ARRAY['conversation.read', 'message.read', 'media.read', 'contact.read', 'events.read',
                         'conversation.claim', 'conversation.transfer', 'conversation.finalize', 'message.send']::TEXT[]);
