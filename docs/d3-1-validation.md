# D3.1 Validation Guide

**Phases 1-4**: Implementation complete (commit 4c315b4)  
**Phases 5-6**: Integration tests + Docker validation (this guide)  
**Phase 7**: Completion report

## Phase 5: Integration Tests

### Prerequisites

```bash
cd /data/home-moved/Projects/_legacy_lowercase_projects/20-OMNIRA

# 1. Ensure docker-compose is available
docker-compose version

# 2. Start PostgreSQL service
docker-compose up -d postgres

# 3. Wait for postgres to be ready (healthcheck)
docker-compose ps postgres
# Should show "healthy"

# 4. Apply migrations (manual or via compose init script)
# The application should auto-run migrations on startup
# For manual run:
psql -h localhost -U omnira -d omnira -f migrations/000020_create_channel_credentials.up.sql
```

### Run Tests

```bash
# Unit tests (cipher — no DB required)
go test ./internal/channels/adapters/crypto -v

# Integration tests (requires DB)
go test ./internal/channels/adapters/postgres -v -run "TestD3_1"

# All channel tests (D2 seam + D3.1 adapters)
go test ./internal/channels/... -v

# Full test suite
go test ./... -v
```

### Expected Test Results

All 10+ tests should PASS:
```
✓ TestD3_1_StoreCredential_ResolvesCorrectly
✓ TestD3_1_RotateCredential_MaintainsSecretRef
✓ TestD3_1_TenantA_CannotAccessTenantB_Credential
✓ TestD3_1_FindByExternalNumberID_TenantSafe
✓ TestD3_1_ChannelConnectionRepository_Store_FindByID
✓ TestD3_1_ChannelConnectionRepository_Update_Status
✓ TestD3_1_SecretRef_IsOpaque
✓ TestD3_1_CredentialNeverInResponse
✓ TestD3_1_RLS_Enforced
✓ TestD3_1_OMNIRA_CREDENTIALS_KEY_Validation
✓ TestAES256GCMCipher_Encrypt_Decrypt_Roundtrip (+ 7 more cipher tests)
✓ TestChannelSeam_SendText_EndToEnd (D2, + 5 more seam tests)
```

### Troubleshooting

**`go: command not found`**
- Install Go 1.21+: https://golang.org/dl/
- Or use Docker: `docker run -v $(pwd):/workspace golang:1.21 bash -c "cd /workspace && go test ./..."`

**`database not available for integration tests`**
- Tests will skip if `docker-compose up postgres` hasn't completed
- Check: `docker-compose ps postgres`
- Wait for "healthy" status

**`OMNIRA_CREDENTIALS_KEY not set`**
- Tests auto-generate key via `genValidKey()`
- If explicit validation needed: export OMNIRA_CREDENTIALS_KEY before running

**RLS validation failures**
- Ensure PostgreSQL version supports RLS (9.5+)
- Check policy: `\d channel_credentials` in psql
- Should show "Row Level Security enabled" and policy

---

## Phase 6: Docker Build + Full Suite

### Prerequisites

```bash
# 1. Install Docker
docker --version

# 2. Install Docker Compose
docker-compose --version

# 3. Ensure Go toolchain (for local build) or use Docker build
go version
```

### Run Validation

```bash
# Step 1: Code quality checks
go vet ./...
go fmt ./...
# Should have no warnings

# Step 2: Full test suite
go test ./... -v -cover
# All tests PASS
# Coverage: aim for 80%+ on channels/* packages

# Step 3: Docker build
docker build -t omnira:d3-1 .
# BUILD SUCCESS

# Step 4: Compose validation
docker-compose down  # Clean previous
docker-compose up -d  # Start all services
docker-compose ps    # All should be healthy

# Step 5: Health endpoint
curl http://localhost:8080/healthz
# 200 OK, JSON response

# Step 6: Migration verification
docker-compose exec postgres psql -U omnira -d omnira -c "
  SELECT table_name 
  FROM information_schema.tables 
  WHERE table_schema = 'public' AND table_name = 'channel_credentials';"
# Should return: channel_credentials

# Step 7: RLS policy verification
docker-compose exec postgres psql -U omnira -d omnira -c "
  SELECT schemaname, tablename, policyname 
  FROM pg_policies 
  WHERE tablename = 'channel_credentials';"
# Should return: channel_credentials_tenant_policy
```

### Expected Results

```
✓ go vet: no errors
✓ go test: all PASS, coverage 80%+
✓ docker build: IMAGE omnira:d3-1 created
✓ docker-compose up: all services healthy
✓ curl /healthz: 200 OK
✓ channel_credentials table: EXISTS
✓ RLS policy: FORCE enabled
```

### Cleanup

```bash
# Stop services
docker-compose down

# Remove test image
docker rmi omnira:d3-1
```

---

## Phase 7: Completion Report

### Report Template

See [[slice-completion-report-template]] for full format. Minimal example:

```markdown
## D3.1 — WhatsApp Provider Layer / Credential Encryption
- **Commit**: 4c315b4 — feat(D3.1): Credential Encryption + PostgreSQL Adapters
- **Implementado**:
  - Migration 000020 (channel_credentials, RLS+FORCE)
  - AES-256-GCM cipher
  - CredentialStore PostgreSQL adapter
  - ChannelConnectionRepository adapter
  - 10+ integration tests (Tenant A/B, RLS, credential, webhook)

### Tests
- Unit tests: 8 cipher tests PASS
- Integration tests: 10 adapter tests PASS
- D2 seam tests: 6 tests PASS (pre-existing)
- Coverage: 82% (internal/channels/adapters/*)

### Security
✓ NATS: no token
✓ Logs: no token (redacted)
✓ API response: no token (secretRef only)
✓ Frontend: no token
✓ RLS: enforced (FORCE)
✓ TenantID: mandatory
✓ FindByExternalNumberID: authoritative

### Attribution
Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016jo9x4HmPet9s4vmADCvFk

### Próximo Slice
D3.2: WhatsApp Provider Layer — Meta Webhook Verification + Tenant-Safe Connection Resolution
```

### File Report

Once tests PASS and Docker validates, commit report:

```bash
git add -A
git commit -m "docs(D3.1): Completion report — all tests PASS, RLS validated, Docker build success"
```

---

## Checklist (Copy-Paste)

```
Phase 5: Integration Tests
─ [ ] docker-compose up -d postgres (healthy)
─ [ ] go test ./internal/channels/adapters/crypto -v (PASS)
─ [ ] go test ./internal/channels/adapters/postgres -v (PASS, 10+ tests)
─ [ ] All Tenant A/B isolation tests PASS (RLS validated)
─ [ ] Credential never in logs (String() redacted)
─ [ ] Secret ref is opaque UUID

Phase 6: Docker Validation
─ [ ] go vet ./... (clean)
─ [ ] go test ./... -v -cover (all PASS, 80%+)
─ [ ] docker build -t omnira:d3-1 . (success)
─ [ ] docker-compose up (all healthy)
─ [ ] curl /healthz (200 OK)
─ [ ] Migration 000020 applied (channel_credentials exists)
─ [ ] RLS policy FORCE enabled

Phase 7: Completion Report
─ [ ] Report filed per template
─ [ ] Commit message: "docs(D3.1): Completion report — ..."
─ [ ] PR/merge ready

Next Slice: D3.2
─ [ ] Memory updated with D3.1 completion
─ [ ] D3.2 tasks started (Meta Webhook Verification)
```

---

**Estimated Time**: 3–4 hours (5 + 1 + 0.5h)  
**Success**: All checklist items green ✓
