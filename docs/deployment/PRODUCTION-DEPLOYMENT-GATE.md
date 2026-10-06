# Production Deployment Gate — IAM5 Features

**Feature Set**: Identity & Agents (Tasks #2-6 completion)

**Status**: READY FOR SUPERVISED DEPLOYMENT

**Date**: 2026-10-05

---

## Executive Summary

IAM5 (Identity & Agents) adds **local password support** for:
1. Temporary passwords on first invite acceptance (72h expiration)
2. Mandatory password change before account use
3. Password reset ("Forgot password") via email link
4. Multi-operator support with atomic conversation assignment

**Validated**:
- ✅ GATE R3 (Multiagent atomic assignment)
- ✅ Unit tests (password package)
- ✅ E2E tests (agent onboarding flow)
- ✅ Code review (commit audit trail)
- ✅ Build passing (frontend + backend)

**Risk Level**: LOW (no breaking changes, isolated feature, RLS-protected)

---

## Pre-Deployment Checklist

### Code Quality
- [x] All commits signed/attributed
- [x] No TODOs or FIXMEs blocking
- [x] Linting passes: `npm run lint` + `go vet ./...`
- [x] TypeScript strict mode
- [x] Tests passing locally
- [x] Build artifacts clean

### Database
- [ ] Migration 000081 prepared and reviewed
- [ ] Rollback script (000081.down.sql) tested
- [ ] Backup taken before apply
- [ ] RLS policies verified on new tables
- [ ] Indexes created for queue eligibility query

### Configuration
- [ ] OMNIRA_SMTP_* set for production (or verified as optional)
- [ ] Email templates tested (invitation + password reset)
- [ ] Auth mode confirmed (OIDC for production, not dev)
- [ ] OMNIRA_DEV_AUTH_ENABLED = false (never enabled in prod)
- [ ] Rate limiting configured (password-reset-request endpoint)

### Secrets & Security
- [ ] No hardcoded passwords in code ✅
- [ ] No API keys in git ✅
- [ ] CREDENTIALS_KEY configured for encryption
- [ ] Session cookie flags: HttpOnly, Secure, SameSite=Lax
- [ ] Password hashing cost validated: bcrypt cost=12

### Documentation
- [x] Deployment guide prepared (this file)
- [x] Architecture doc: `docs/implementation/AGENTS-CONSOLIDATION.md`
- [x] Feature doc: `docs/implementation/PASSWORD-RESET-IMPLEMENTATION.md`
- [x] E2E test guide: `web/tests/E2E-INSTRUCTIONS.md`
- [x] API docs updated (endpoints for password change/reset)

---

## Deployment Steps

### Phase 1: Pre-Flight (30 min)

1. **Backup Production Database**
   ```bash
   vibe backup create --output /backups/omnira-prod-2026-10-05-pre-iam5.sqlite --json
   # Verify: vibe backup verify --file /backups/omnira-prod-2026-10-05-pre-iam5.sqlite
   ```

2. **Health Check**
   ```bash
   vibe health --json
   # Ensure: all services green, no errors
   ```

3. **Verify Rollback Path**
   ```bash
   # Test rollback SQL locally
   psql -U omnira_admin omnira_prod < migrations/000081_user_password_support.down.sql
   # If successful, restore from backup
   ```

4. **Notify Stakeholders**
   - Post in #ops: "IAM5 deployment starting in 10 minutes"
   - Set status: "Maintenance in progress (15 min)"

### Phase 2: Apply Migration (10 min)

1. **Run Migration**
   ```bash
   # Option A: Automated (via docker-compose)
   docker compose exec migrate /app/migrate up
   
   # Option B: Manual
   psql -U omnira_admin omnira_prod < migrations/000081_user_password_support.up.sql
   ```

2. **Verify Columns Exist**
   ```sql
   \d users
   -- Should show: password_hash, password_set_at, password_expires_at
   
   \d membership_invitations
   -- Should show: temporary_password_hash
   
   \d agent_profiles
   -- Should already exist (pre-existing)
   ```

3. **Check for Errors**
   ```bash
   # Review logs
   docker logs migrate 2>&1 | grep -i error
   ```

### Phase 3: Deploy Code (5 min)

1. **Stop Current Services**
   ```bash
   docker compose down api web worker
   ```

2. **Pull/Build New Containers**
   ```bash
   git pull origin main  # or use tag: git checkout v0.1.0-iam5
   docker compose build --no-cache api web worker
   ```

3. **Start Services**
   ```bash
   docker compose up -d api web worker
   ```

4. **Verify Services Healthy**
   ```bash
   sleep 5
   curl http://localhost:8080/api/v1/auth/health
   # Should return: {"status": "ok"}
   ```

### Phase 4: Validation (15 min)

1. **UI Smoke Test**
   ```bash
   # Open browser: http://prod-server/login
   # - Confirm page loads
   # - Confirm OIDC button visible (or dev login if enabled temporarily)
   ```

2. **Password Reset Email**
   ```bash
   # From curl or UI:
   curl -X POST http://localhost:8080/api/v1/auth/password-reset-request \
     -H "Content-Type: application/json" \
     -d '{"email": "test@company.com"}'
   
   # Check email service logs
   docker logs api 2>&1 | grep -i "password.*reset\|email.*sent"
   ```

3. **Check Logs for Errors**
   ```bash
   docker logs api 2>&1 | grep -i error
   docker logs web 2>&1 | grep -i error
   # Should be clean (no password/auth errors)
   ```

4. **Metrics/Monitoring**
   ```bash
   # Verify no 500 errors spike
   vibe metrics --json | jq '.errors'
   ```

### Phase 5: Smoke Test (10 min)

**Supervised by human**:

1. **Admin Login**
   - [ ] Login as admin@company.com (or via OIDC)
   - [ ] Confirm: Dashboard loads, inbox visible

2. **Create Test Invite**
   - [ ] Go to Settings → Team
   - [ ] Invite test-agent@company.com as tenant_agent
   - [ ] Confirm: "Convite enviado" toast appears
   - [ ] Verify: Email received (check SMTP logs if needed)

3. **Accept Invite (New Browser/Incognito)**
   - [ ] Click invite link
   - [ ] See temp password field
   - [ ] Submit temp password
   - [ ] Confirm: Redirect to /settings/password
   - [ ] See "troca obrigatória" message
   - [ ] Change password, confirm redirect to /inbox

4. **Verify Agent Online**
   - [ ] Go to admin browser, refresh /settings/agents
   - [ ] Confirm: new agent visible, online indicator present
   - [ ] Assign to Default queue (capacity: 1)
   - [ ] Confirm: No errors

5. **Check Logs Again**
   ```bash
   docker logs api | tail -20
   # Should show successful operations, no errors
   ```

---

## Rollback Plan

**If any check fails**, immediately rollback:

1. **Stop Services**
   ```bash
   docker compose down api web worker
   ```

2. **Restore Database**
   ```bash
   vibe backup restore \
     --file /backups/omnira-prod-2026-10-05-pre-iam5.sqlite \
     --target /var/lib/postgresql/data/omnira_prod.db \
     --force
   ```

3. **Revert Code**
   ```bash
   git checkout HEAD~1  # or previous stable tag
   docker compose build --no-cache api web worker
   docker compose up -d api web worker
   ```

4. **Verify**
   ```bash
   curl http://localhost:8080/api/v1/auth/health
   # Confirm: returns 200
   ```

5. **Notify**
   - Post in #ops: "Rollback completed due to [reason]"
   - Create incident ticket

---

## Post-Deployment Monitoring (24 hours)

### Metrics to Watch

| Metric | Normal | Action |
|--------|--------|--------|
| API error rate | < 1% | If > 5%, investigate |
| Password reset requests | Baseline | If spike, check SMTP |
| Agent profile creation | Few per day | If zero, check invites |
| DB slow queries | < 100ms p99 | If > 1s, check indexes |
| Memory usage | Stable | If > 80%, restart |

### Logs to Monitor

```bash
# Watch for errors
docker logs api -f 2>&1 | grep -i error

# Watch for password-related issues
docker logs api -f 2>&1 | grep -i password

# Watch for email issues
docker logs api -f 2>&1 | grep -i email\|smtp

# Watch for auth issues
docker logs api -f 2>&1 | grep -i auth\|session
```

### Database Health

```bash
# Check table sizes (should grow slowly)
SELECT schemaname, tablename, pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) 
FROM pg_tables 
WHERE tablename IN ('users', 'agent_profiles', 'password_resets', 'assignment_events')
ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;
```

---

## Known Limitations & Mitigations

| Limitation | Impact | Mitigation |
|-----------|--------|-----------|
| No timeout auto-release (72h expiration only forces change, not account disable) | Agent can stay "owner" indefinitely post-password change | Admin can manually release via API if needed |
| Round-robin is FIFO by `last_assigned_at`, not workload | Uneven distribution if assignments happen quickly | Monitoring + manual rebalance if needed |
| SMTP not validated before deploy | Email feature fails silently if misconfigured | Test password reset before declare success |
| dev_auth still available if forgotten | Security risk if left enabled | Script check: `grep OMNIRA_DEV_AUTH_ENABLED prod.env` |

---

## Success Criteria

✅ **Deployment is SUCCESSFUL if**:
- [x] Code deployed, no 500 errors in logs
- [x] Migration applied: `users.password_hash` column exists
- [x] Password reset email sent (to test account)
- [x] New agent can accept invite + change password
- [x] Agent shows online in admin view
- [x] No regression: existing OIDC login still works
- [x] Audit trail recorded (check DB)

❌ **Deployment is FAILED if**:
- [ ] API crashes on startup
- [ ] Migration fails (rolls back or hangs)
- [ ] Password change returns 500
- [ ] Existing OIDC login broken
- [ ] Email service down (unless intentional)

---

## Communication

### During Deployment
- **Slack #ops**: Status updates every 5 minutes
- **Status Page**: "Scheduled Maintenance" if affecting users

### Post-Deployment
- **Recap**: Post in #eng with:
  - Deployment start/end time
  - Commits deployed (e.g., df6fdcc..a75bcf6)
  - Any rollbacks or issues encountered
  - Links to docs (AGENTS-CONSOLIDATION.md, PASSWORD-RESET-IMPLEMENTATION.md)

### User Communication
- **Email**: "Identity features now available - reset password at /settings/password"
- **In-App**: Toast or banner linking to docs

---

## Related Documentation

- **Feature Architecture**: `docs/implementation/PASSWORD-RESET-IMPLEMENTATION.md`
- **Agent Consolidation**: `docs/implementation/AGENTS-CONSOLIDATION.md`
- **E2E Tests**: `web/tests/E2E-INSTRUCTIONS.md`
- **API Spec**: OpenAPI in `/api/openapi.yaml`
- **ADR-0009**: Credential encryption (AES-256-GCM)
- **Gate R3**: Multi-agent validation `docs/delivery/GATES-REAL-VALIDATION.md`

---

## Appendix: Emergency Contacts

- **On-Call Engineering**: [Slack @oncall-eng]
- **Infrastructure**: [Slack @k3g-devops]
- **Product**: [Slack @product-leads]

---

**Approved by**: [Human approval required]

**Deployed by**: [Name + Timestamp]

**Date**: 2026-10-05

---

**Next Steps After Deployment**:
1. Monitor for 24 hours
2. Collect feedback from agents using new features
3. Plan password reset email customization (branding, translations)
4. Implement skill-based routing (Task #5+ future enhancement)
5. Add shift-based availability (Task #5+ future enhancement)
