-- F9 (ADR-0054 §2.1/§2.2, release/policies/channels.yaml "retention_days"): idempotência de
-- "excluir uma release do catálogo" (ADR-0054 §2.2.1 exige Idempotency-Key em toda mutação de
-- plataforma). SEM FOREIGN KEY para platform_releases(release_id) de propósito — a exclusão
-- bem-sucedida remove exatamente essa linha; um FK aqui tornaria a PRÓPRIA linha de
-- idempotência impossível de reter depois do sucesso que ela precisa lembrar.
CREATE TABLE platform_release_gc_requests (
  operator_id     UUID NOT NULL REFERENCES users(id),
  idempotency_key TEXT NOT NULL,
  request_hash    TEXT NOT NULL,
  release_id      TEXT NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (operator_id, idempotency_key)
);

ALTER TABLE platform_release_gc_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform_release_gc_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_release_gc_requests_platform_operator_only ON platform_release_gc_requests
  FOR ALL USING (is_system_admin() OR is_platform_operator(current_user_id()))
  WITH CHECK (is_system_admin() OR is_platform_operator(current_user_id()));
GRANT SELECT, INSERT, UPDATE, DELETE ON platform_release_gc_requests TO omnira_app;
