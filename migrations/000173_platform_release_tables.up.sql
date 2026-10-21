-- F6 (ADR-0054 §2.4): tabelas globais do Update Manager API. Nenhuma tem tenant_id — são dados da
-- PLATAFORMA inteira, não de um tenant — e por isso a varredura de RLS por tenant_id
-- (internal/platform/db/rls_completeness_test.go) não as alcança; RLS é aplicada aqui manualmente,
-- no mesmo padrão de platform_operators (migration 000099): FORCE ROW LEVEL SECURITY, policy restrita
-- a is_system_admin() OR is_platform_operator(current_user_id()). O serviço Go
-- (internal/platformrelease/updatemgr) sempre escreve através de uma sessão de sistema, re-verificando
-- IsPlatformOperator explicitamente ANTES de qualquer query — RLS aqui é defesa em profundidade, nunca
-- a única barreira (mesmo padrão de internal/hub/companies.Service, ver authorize()).

-- platform_installations: uma linha por instalação (hoje: só esta SaaS compartilhada, ADR-0054 §2.4).
-- O truque da PK booleana com CHECK(singleton) garante, no próprio schema, que nunca existe uma segunda
-- linha — não depende de disciplina de aplicação.
CREATE TABLE platform_installations (
  singleton          BOOLEAN NOT NULL DEFAULT true PRIMARY KEY,
  channel            TEXT NOT NULL DEFAULT 'candidate',
  agent_protocol     INT NOT NULL DEFAULT 1,
  current_version    TEXT,
  current_release_id TEXT,
  current_digests    JSONB NOT NULL DEFAULT '{}'::jsonb,
  -- last_synced_run_id: o run_id (do ledger local do Update Agent, nunca um UUID gerado pelo
  -- Postgres) cujo SUCCEEDED já foi aplicado a current_version/current_release_id. Existe para
  -- que a sincronização nunca reaplique o mesmo fato duas vezes nem retroceda para um run mais
  -- antigo sob chamadas concorrentes (internal/platformrelease/updatemgr.Service, achado Codex).
  last_synced_run_id TEXT,
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (singleton)
);
INSERT INTO platform_installations DEFAULT VALUES;

-- platform_installation_version_requests: idempotência de "registrar a versão instalada
-- manualmente" (SetInstallationVersion) — achado Codex [P2]: sem isto, um retry (resposta
-- perdida) depois de um update real ter concluído reescrevia a versão ANTIGA e avançava
-- updated_at de novo, fazendo a sincronização (applyLatestSucceededRunToInstallation,
-- writeLedgerRun) tratar esse registro obsoleto como "mais recente" que o run SUCCEEDED
-- genuíno que aconteceu nesse meio tempo. Mesmo padrão das demais tabelas de idempotência.
CREATE TABLE platform_installation_version_requests (
  operator_id     UUID NOT NULL REFERENCES users(id),
  idempotency_key TEXT NOT NULL,
  request_hash    TEXT NOT NULL,
  version         TEXT NOT NULL,
  release_id      TEXT,
  applied_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (operator_id, idempotency_key)
);

-- platform_releases: cache local do catálogo assinado (a fonte de verdade é o catálogo remoto
-- assinado, F3/F9; esta tabela é só índice de consulta de manifestos já validados pelo parser
-- estrito de F1 — internal/platformrelease/domain.ParseManifest/Validate — antes de entrar aqui).
CREATE TABLE platform_releases (
  release_id       TEXT PRIMARY KEY,
  channel          TEXT NOT NULL,
  platform_version TEXT NOT NULL,
  manifest_json    JSONB NOT NULL,
  ingested_by      UUID NOT NULL REFERENCES users(id),
  ingested_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- platform_release_ingest_requests: idempotência de POST /platform/releases pela PRÓPRIA
-- Idempotency-Key — achado Codex [P2]: o dedup por release_id (acima) protege contra reingerir
-- a MESMA release imutável, mas nunca detectava "a mesma chave usada para uma release
-- DIFERENTE", que o contrato documentado (422) promete. Mesmo padrão das outras tabelas de
-- idempotência deste arquivo.
CREATE TABLE platform_release_ingest_requests (
  operator_id     UUID NOT NULL REFERENCES users(id),
  idempotency_key TEXT NOT NULL,
  request_hash    TEXT NOT NULL,
  release_id      TEXT NOT NULL REFERENCES platform_releases(release_id),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (operator_id, idempotency_key)
);

-- platform_update_plans: resultado do preflight (especificação §7.1) para uma release candidata —
-- PASS ou BLOCK, nunca "tentar com aviso" para uma incompatibilidade real (compatibility.yaml).
CREATE TABLE platform_update_plans (
  id               UUID PRIMARY KEY,
  release_id       TEXT NOT NULL REFERENCES platform_releases(release_id),
  preflight_status TEXT NOT NULL CHECK (preflight_status IN ('PASS', 'BLOCK')),
  preflight_detail JSONB NOT NULL,
  created_by       UUID NOT NULL REFERENCES users(id),
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at       TIMESTAMPTZ NOT NULL
);

-- platform_update_plan_requests: idempotência de "criar um plano" (ADR-0054 §2.2.1 exige
-- Idempotency-Key em toda mutação de plataforma) — achado Codex [P2]: faltava até aqui, então
-- uma resposta perdida/timeout fazia um retry do operador criar um SEGUNDO plano (e um segundo
-- evento de auditoria) em vez de recuperar o original. Mesmo padrão das demais tabelas.
CREATE TABLE platform_update_plan_requests (
  operator_id      UUID NOT NULL REFERENCES users(id),
  idempotency_key  TEXT NOT NULL,
  request_hash     TEXT NOT NULL,
  created_plan_id  UUID NOT NULL REFERENCES platform_update_plans(id),
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (operator_id, idempotency_key)
);

-- platform_update_run_requests: idempotência de "iniciar um run", mesmo padrão de
-- company_creation_requests (migration 000033-ish) — guarda o hash do pedido, nunca o corpo inteiro.
CREATE TABLE platform_update_run_requests (
  operator_id     UUID NOT NULL REFERENCES users(id),
  idempotency_key TEXT NOT NULL,
  request_hash    TEXT NOT NULL,
  created_run_id  TEXT NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (operator_id, idempotency_key)
);

-- platform_update_runs: espelho em Postgres do que está no ledger LOCAL do Update Agent (fora do
-- Postgres, ADR-0054 §2.4) — histórico/auditoria para a view administrativa. A autoridade de
-- execução continua sendo o ledger local; esta tabela nunca decide "o que fazer agora", só registra
-- o que já foi decidido/observado lá. `id` é TEXT porque o run_id do ledger (internal/platformrelease/
-- agent.Ledger) já é uma string de aplicação, não um UUID gerado pelo Postgres.
CREATE TABLE platform_update_runs (
  id              TEXT PRIMARY KEY,
  plan_id         UUID NOT NULL REFERENCES platform_update_plans(id),
  release_id      TEXT NOT NULL REFERENCES platform_releases(release_id),
  state           TEXT NOT NULL,
  manifest_json   JSONB NOT NULL,
  idempotency_key TEXT NOT NULL,
  actor_id        UUID NOT NULL REFERENCES users(id),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- platform_update_events: trilha de progresso append-only (especificação §6.1/§8) — espelha os
-- checkpoints do ledger local. REVOKE UPDATE/DELETE + trigger reforçam a imutabilidade no próprio
-- banco (mesmo padrão de conversation_closures, migration 000087), não só disciplina de aplicação.
CREATE TABLE platform_update_events (
  id         BIGSERIAL PRIMARY KEY,
  run_id     TEXT NOT NULL REFERENCES platform_update_runs(id),
  seq        BIGINT NOT NULL,
  kind       TEXT NOT NULL,
  detail     JSONB NOT NULL DEFAULT '{}'::jsonb,
  actor_id   UUID REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (run_id, seq)
);

-- platform_backup_records: evidência de backup/restore (release/policies/backup.yaml) — bookkeeping
-- informativo nesta fase (F6); o VERIFICADOR que a torna um gate bloqueante de promoção "stable" é
-- trabalho do F9, como o próprio backup.yaml já documenta ("aplicado por um verificador real a
-- construir em F9"). Aqui ela só alimenta o preflight como WARN informativo, nunca BLOCK.
CREATE TABLE platform_backup_records (
  id         UUID PRIMARY KEY,
  kind       TEXT NOT NULL,
  taken_at   TIMESTAMPTZ NOT NULL,
  verified   BOOLEAN NOT NULL,
  size_bytes BIGINT,
  sha256_hex TEXT,
  detail     JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_by UUID NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- platform_backup_record_requests: idempotência de "registrar evidência de backup", mesmo
-- padrão de platform_update_run_requests acima — achado Codex [P2]: POST /platform/backups
-- exigia Idempotency-Key no contrato (ADR-0054 §2.2.1) mas o handler nunca a lia, então um
-- retry (resposta perdida, timeout do cliente) sempre criava uma segunda linha.
CREATE TABLE platform_backup_record_requests (
  operator_id     UUID NOT NULL REFERENCES users(id),
  idempotency_key TEXT NOT NULL,
  request_hash    TEXT NOT NULL,
  created_record_id UUID NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (operator_id, idempotency_key)
);

-- platform_component_versions: unidades realmente instaladas por build/deploy unit (ADR-0054 §2.4).
-- Schema criado nesta fase para não divergir do ADR; a POPULAÇÃO real (a partir do que o Update Agent
-- de fato implantou) é uma pendência documentada desta fase — ver docs/release/IMPLEMENTATION-STATUS.md
-- F6 "Pendências".
CREATE TABLE platform_component_versions (
  build_unit TEXT PRIMARY KEY,
  version    TEXT,
  digest     TEXT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

DO $$
DECLARE
  t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['platform_installations', 'platform_installation_version_requests', 'platform_releases',
                            'platform_update_plans', 'platform_update_run_requests', 'platform_update_runs',
                            'platform_update_events', 'platform_release_ingest_requests', 'platform_update_plan_requests',
                            'platform_backup_records', 'platform_backup_record_requests', 'platform_component_versions']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY %I ON %I FOR ALL USING (is_system_admin() OR is_platform_operator(current_user_id())) WITH CHECK (is_system_admin() OR is_platform_operator(current_user_id()))',
      t || '_platform_operator_only', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO omnira_app', t);
  END LOOP;
END $$;

-- platform_update_events é append-only de verdade: nem o operador nem um bug de aplicação reescreve
-- um evento já publicado.
REVOKE UPDATE, DELETE ON platform_update_events FROM omnira_app;
CREATE FUNCTION platform_update_events_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'platform_update_events são append-only' USING ERRCODE = 'integrity_constraint_violation';
END $$;
CREATE TRIGGER platform_update_events_no_update BEFORE UPDATE ON platform_update_events FOR EACH ROW EXECUTE FUNCTION platform_update_events_immutable();
CREATE TRIGGER platform_update_events_no_delete BEFORE DELETE ON platform_update_events FOR EACH ROW EXECUTE FUNCTION platform_update_events_immutable();
