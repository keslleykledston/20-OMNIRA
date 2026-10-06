# E2E Tests — Agent Onboarding Flow

**Test Suite**: `e2e/agent-onboarding.spec.ts`

**Scope**: Complete workflow from invite → login → forced password change → agent online

---

## Prerequisites

1. **Stack running**:
   ```bash
   docker compose up -d
   # or locally:
   make dev
   ```

2. **Dev auth enabled** (OMNIRA_DEV_AUTH_ENABLED=true)
   - Tests use dev login: `admin@omnira.local` / `admin123`

3. **Playwright installed**:
   ```bash
   cd web
   npm install -D @playwright/test
   ```

4. **Base URL configured**:
   - Default: `http://localhost:5173`
   - Override: `PLAYWRIGHT_TEST_BASE_URL=http://your-server:3000`

---

## Running Tests

### All Tests
```bash
cd web
npx playwright test e2e/agent-onboarding.spec.ts
```

### Single Test
```bash
npx playwright test e2e/agent-onboarding.spec.ts -g "complete onboarding"
```

### Headed Mode (see browser)
```bash
npx playwright test e2e/agent-onboarding.spec.ts --headed
```

### Debug Mode
```bash
npx playwright test e2e/agent-onboarding.spec.ts --debug
```

---

## What The Test Does

### Test: "complete onboarding: invite → temp password → forced change → agent online"

**Flow**:

1. **Admin Login** → `/login` → dev login
2. **Create Invitation** → `/settings/team` → "Convidar" → fills form
3. **Extract Temp Password** → Captures from UI or API
4. **New Agent Accepts** → `/invite/{token}` → submits temp password
5. **Forced Password Change** → redirects to `/settings/password` → changes to new password
6. **Agent Online** → Verifies presence in admin view (`/settings/agents`)
7. **Queue Assignment** → Admin assigns agent to queue (capacity 2)
8. **Claim Conversation** → Agent navigates to `/inbox`, "Claim" button available
9. **Audit Verification** → Via API, confirms agent_profiles entry

**Assertions**:
- ✅ Invitation created
- ✅ Temp password matches pattern `[A-Za-z0-9!@#$%^&*]{12}`
- ✅ Redirect to `/settings/password`
- ✅ Password form has 2 inputs (new + confirm, no old)
- ✅ Redirect to `/inbox` after change
- ✅ Agent visible as online in admin view
- ✅ Agent can be added to queue
- ✅ API returns agent_profiles with status=active

---

## Validation Tests (Placeholder)

### "password expires after 72 hours"
- **Status**: Placeholder (requires DB access)
- **Future**: Add helper to query PostgreSQL directly
- **Validation**: `SELECT password_expires_at FROM users WHERE email=$1`
  - Assert: `password_expires_at - NOW() ≈ 72 hours ± 1 min`

### "atomic assignment: two agents cannot claim same conversation"
- **Status**: Placeholder (requires conversation fixture)
- **Future**: 
  1. Create conversation via API
  2. Agent 1: POST `/conversations/{id}/claim` → 200
  3. Agent 2: POST `/conversations/{id}/claim` → 409 Conflict
  4. Agent 1: Label shows ownership
  5. Agent 2: Label shows Agent 1 ownership (realtime via SSE)

---

## Troubleshooting

### Test Fails: "Dev Login button not found"
- **Cause**: Auth mode is OIDC, not dev
- **Fix**: Set `OMNIRA_DEV_AUTH_ENABLED=true` and restart
- **Check**: GET `/api/v1/auth/mode` → `{"mode": "dev", "dev_auth": true}`

### Test Fails: "Redirect to password change timeout"
- **Cause**: Backend not returning `password_expires_at`
- **Fix**: 
  1. Check migration 000081 is applied: `\d users` in psql
  2. Confirm fields: `password_hash`, `password_expires_at`, `password_set_at`
  3. Run: `make migrate-latest`

### Test Fails: "Agent not visible as online"
- **Cause**: Presence SSE not connected
- **Fix**: 
  1. Check presence service is running: `make dev` includes it
  2. Verify SSE endpoint: `GET /api/v1/tenants/{id}/presence/events`
  3. Add delay: `await page.waitForTimeout(2000)` before checking

### Test Fails: "Temp password does not match regex"
- **Cause**: Password generation or display issue
- **Fix**: 
  1. Check `internal/password/password.go` Generate()
  2. Verify email template includes `{{ .TemporaryPassword }}`
  3. Check `web/src/pages/AcceptInvitePage.tsx` shows password field

---

## Output

### Success Run
```
✓ e2e/agent-onboarding.spec.ts (3 tests, all passed)

STEP 1: Admin login and create invitation
✓ Invitation created with token: df6fdcc...

STEP 2: New agent accepts invitation
✓ Temp password received: abC1***

STEP 3: Forced password change
✓ Password changed, redirected to inbox

STEP 4: Verify agent online
✓ Agent appears as online in admin view

STEP 5: Assign agent to queue
✓ Agent assigned to queue (capacity: 2)

STEP 6: Agent claims conversation
✓ Agent can claim conversations

STEP 7: Audit trail
✓ Audit trail verified via API

✅ COMPLETE FLOW VALIDATED
```

### Failure Run (Example)
```
✗ e2e/agent-onboarding.spec.ts > complete onboarding
Error: Redirect to password change timeout

  at agent-onboarding.spec.ts:85
  Timeout 10000ms waiting for URL pattern /settings/password
```

---

## Integration with CI/CD

### GitHub Actions Example
```yaml
name: E2E Tests

on: [push, pull_request]

jobs:
  e2e:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:15
        env:
          POSTGRES_PASSWORD: postgres
      nats:
        image: nats:latest

    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-node@v3
        with:
          node-version: '18'

      - name: Start stack
        run: make dev

      - name: Run E2E tests
        env:
          PLAYWRIGHT_TEST_BASE_URL: http://localhost:5173
        run: cd web && npx playwright test e2e/

      - uses: actions/upload-artifact@v3
        if: always()
        with:
          name: playwright-report
          path: web/playwright-report/
```

---

## References

- **Playwright Docs**: https://playwright.dev/docs/intro
- **Test Structure**: `e2e/agent-onboarding.spec.ts`
- **Feature**: Task #6 (IAM5 E2E validation)
- **Password Flow**: `docs/implementation/PASSWORD-RESET-IMPLEMENTATION.md`
- **Agents**: `docs/implementation/AGENTS-CONSOLIDATION.md`
- **Gate R3**: `docs/delivery/GATES-REAL-VALIDATION.md`

---

**Status**: ✅ Ready to run

**Notes**:
- Tests use `dev` auth, not OIDC
- Temp passwords generated fresh each run
- Cleanup: Uses unique email per run (`agent-{timestamp}@omnira.local`)
- Idempotent: Can run multiple times without side effects
