-- F9 (ADR-0054 §2.1/§2.2, release/policies/signing.yaml "revocation"): lista de releases
-- revogadas. A ASSINATURA desta lista (signing_mechanism em signing.yaml) permanece
-- BLOCKED_EXTERNAL nesta sessão (gh_cli_missing/ghcr_credentials_unconfirmed, achados F1/F3) —
-- esta tabela é a aplicação REAL do enforcement de revogação (rejeitar uma release revogada em
-- todo preflight/dispatch, nunca cacheada indefinidamente), nunca a infraestrutura de
-- assinatura em si. Mesmo padrão de RLS manual de platform_release_tables (migration 000173):
-- sem tenant_id, FORCE ROW LEVEL SECURITY, policy restrita a platform operator.
CREATE TABLE platform_release_revocations (
  release_id TEXT PRIMARY KEY REFERENCES platform_releases(release_id),
  revoked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  reason     TEXT NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 500),
  revoked_by UUID NOT NULL REFERENCES users(id)
);

-- idempotência de "revogar uma release" (ADR-0054 §2.2.1 exige Idempotency-Key em toda mutação
-- de plataforma) — mesmo padrão das demais tabelas de idempotência de platform_release_tables.
CREATE TABLE platform_release_revocation_requests (
  operator_id     UUID NOT NULL REFERENCES users(id),
  idempotency_key TEXT NOT NULL,
  request_hash    TEXT NOT NULL,
  release_id      TEXT NOT NULL REFERENCES platform_release_revocations(release_id),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (operator_id, idempotency_key)
);

-- Achado Codex [P1]: Service.RevokeRelease grava no Postgres (fato imutável, abaixo) e DEPOIS
-- espelha no ledger local do agente (nenhuma transação distribuída real entre os dois) — se o
-- processo da API morrer/cancelar exatamente entre as duas escritas, a revogação fica
-- PERMANENTEMENTE registrada no Postgres mas AUSENTE do ledger que Executor.Execute de fato
-- consulta, deixando um run já despachado instalar a release revogada indefinidamente até um
-- operador notar e repetir manualmente a chamada. Esta tabela de ENTREGA (nunca a tabela
-- append-only da revogação em si) é atualizável de propósito — rastreia só "este espelho já
-- chegou ao ledger local?", nunca o fato da revogação. refreshPendingRun (chamada em quase
-- toda operação real desta API — CreatePlan, StartRun, GetInstallation, ListRuns) tenta
-- entregar de novo QUALQUER revogação ainda marcada delivered_to_agent=false, dando entrega
-- confiável e eventual sem precisar de um worker de fundo separado.
CREATE TABLE platform_release_revocation_delivery (
  release_id         TEXT PRIMARY KEY REFERENCES platform_release_revocations(release_id),
  delivered_to_agent BOOLEAN NOT NULL DEFAULT false,
  delivered_at       TIMESTAMPTZ
);

DO $$
DECLARE
  t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['platform_release_revocations', 'platform_release_revocation_requests', 'platform_release_revocation_delivery']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY %I ON %I FOR ALL USING (is_system_admin() OR is_platform_operator(current_user_id())) WITH CHECK (is_system_admin() OR is_platform_operator(current_user_id()))',
      t || '_platform_operator_only', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO omnira_app', t);
  END LOOP;
END $$;

-- Revogação é append-only de intenção: uma vez revogada, uma release nunca "desrevoga" por
-- UPDATE direto (especificação §5 "Revogação" nunca descreve uma reversão silenciosa) — se um
-- dia for necessário reverter uma revogação feita por erro, isso é uma decisão de operador
-- documentada fora desta tabela (ex. uma NOVA linha de auditoria explicando o motivo), nunca um
-- UPDATE/DELETE aqui. REVOKE nivela o mesmo padrão de platform_update_events (migration 000173).
REVOKE UPDATE, DELETE ON platform_release_revocations FROM omnira_app;
CREATE FUNCTION platform_release_revocations_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'platform_release_revocations são append-only (revogação nunca é desfeita por UPDATE/DELETE)' USING ERRCODE = 'integrity_constraint_violation';
END $$;
CREATE TRIGGER platform_release_revocations_no_update BEFORE UPDATE ON platform_release_revocations FOR EACH ROW EXECUTE FUNCTION platform_release_revocations_immutable();
CREATE TRIGGER platform_release_revocations_no_delete BEFORE DELETE ON platform_release_revocations FOR EACH ROW EXECUTE FUNCTION platform_release_revocations_immutable();
