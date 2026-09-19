-- audit_events.causation_id nunca foi criada nesta tabela, mas o código
-- (internal/audit/domain.AuditEvent.CausationID, SetCausationID) sempre
-- assumiu que existia: todo INSERT/SELECT em
-- internal/audit/adapters/postgres.go referencia a coluna. Sem ela, o
-- próprio registro de eventos de auditoria (RecordEvent) sempre falhava.
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS causation_id UUID;
CREATE INDEX IF NOT EXISTS idx_audit_events_causation_id ON audit_events(causation_id);
