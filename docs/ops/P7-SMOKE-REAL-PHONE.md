# P7 — WhatsApp Real Phone Smoke Test

**Status:** Automated setup ✅. Human steps required: pairing + inbound + confirmation.

**Bloqueador de:** INTERNAL_PILOT gate, produção.

## Prerequisites

- **Two WhatsApp accounts** on separate phones (one for pairing, one for inbound):
  - **Phone A (disposable):** will be linked to OMNIRA via QR
  - **Phone B (any):** will send inbound messages to Phone A
- **Containers running:**
  ```bash
  docker ps | grep omnira-postgres  # 127.0.0.1:55434
  docker ps | grep omnira-nats      # 127.0.0.1:4222
  ```
- **Go 1.25 or Docker available** to build API/worker images

## How to Run

```bash
scripts/w3-smoke.sh
```

**Output:** Full test log + summary. Script pauses at human steps.

**Optional flags:**
- `--until-qr` — automated part only (infra up, QR generated, then cleanup)
- `--keep` — leave containers/DB running after test (manual cleanup needed)

## Script Phases (6/6)

### 1 ✅ Pre-flight (auto)
- Check docker, curl, python3
- Verify omnira-postgres and omnira-nats containers
- Pull WAHA image

### 2 ✅ Setup (auto)
- Create throwaway DB `omnira_w3`
- Run migrations 000001–000027
- Seed users (test@omnira.local, admin@omnira.local) + tenant + roles + queue
- Build and start API + worker + WAHA containers
- Verify all healthchecks pass

### 3 ✅ Connection + Session (auto)
- Create WAHA connection in API
- Start session (WAHA generates fresh QR)
- Poll for QR PNG and save to `/tmp/omnira-w3-qr.png` (or $TMPDIR)
- Display: `--until-qr stops here`

### 4 🟡 **Pair Phone A (HUMAN)**

**What to do:**
1. Open the QR file (script shows path, e.g., `xdg-open /tmp/omnira-w3-qr.png` on Linux, or Finder on macOS)
2. On **Phone A** (the disposable one):
   - Open WhatsApp
   - Settings → **Linked devices**
   - Tap "Link a device"
   - **Scan the QR** with the phone's camera
3. Wait for pairing to complete (a few seconds)
4. Press **[Enter]** in the terminal when the QR scan is done

**What the script does next:**
- Polls the API every 3 seconds (up to 60s) waiting for `connection.status == "active"`
- When active, logs the paired account ID (phone number)
- If it fails, logs which status was observed and exits

### 5 🟡 **Send Inbound (HUMAN)**

**What to do:**
1. On **Phone B** (any WhatsApp account):
   - Open a chat with the **Phone A** number (the one you just paired)
   - Send any message, e.g., "Test from Phone B"
2. Press **[Enter]** in the terminal

**What the script does next:**
- Polls the API every 3 seconds (up to 40s) waiting for a new conversation in `/inbox/conversations`
- When found, fetches the messages and verifies an inbound message was persisted
- If it fails, logs that the webhook was not reachable from WAHA to the API

### 6 ✅ Reply via API (auto)
- Agent (test@omnira.local) claims the conversation
- Sends a text reply: "Resposta de teste do OMNIRA ✅"
- Worker picks up the outbound job and delivers via WAHA to the real phone
- Polls the database every 3 seconds (up to 40s) watching message.status:
  - `queued` → `sending` → `sent` (worker delivered to WAHA)
  - `sent` → `delivered` (WAHA webhook confirms delivery)
  - `delivered` → `read` (phone user read the message)

### 7 🟡 **Confirm Delivery (HUMAN)**

**What to do:**
1. On **Phone A**, check WhatsApp:
   - You should see the reply from Phone B (your own test message echoed)
   - **More importantly**, you should see the outbound: "Resposta de teste do OMNIRA ✅"
   - Optionally, open the message (marks it as "read")
2. In the terminal, when prompted `Did the reply arrive on the phone? [y/N]`, answer:
   - **y** — if the message appeared ✅
   - **N** — if it did not (indicates webhook issue, WAHA config problem, or network)

### 8 ✅ Summary (auto)
- Prints pass/fail counts
- Lists any failures
- Exit code 0 if all passed, non-zero if any failed

## What Gets Tested

| Aspect | Coverage |
|--------|----------|
| **Auth** | Login (mock) works; JWT passed in Authorization header |
| **API** | Connection create, session start, QR fetch, assignment, send, fetch inbox |
| **WAHA** | QR generation, webhook delivery from the phone (inbound + ACK), outbound delivery |
| **Worker** | Outbound job processing, idempotency, delivery polling |
| **Database** | RLS isolation, message persistence, conversation state |
| **Realtime** | Events appear in the Inbox (not explicit in this test, but API lists them) |
| **Security** | Risk acknowledgement checked; tenant isolation verified |

## Troubleshooting

### "no valid QR PNG produced"
- WAHA may have crashed. Check: `docker logs omnira-w3-waha` or `--keep` the run and debug manually.

### "connection did not become active"
- Phone A may not have scanned the QR, or WhatsApp on Phone A does not have internet.
- Verify the QR file is readable: `file /tmp/omnira-w3-qr.png` should print "PNG image data".

### "no conversation appeared"
- **Most likely:** WAHA webhook cannot reach the API. The WAHA container runs in bridge network; the API must be reachable at `http://host.docker.internal:$API_PORT`.
- Check: `docker logs omnira-w3-waha` for POST errors to the webhook endpoint.
- Or: send a test inbound from Phone B again (the QR is still valid for 8s per attempt).

### "message never reached 'sent'"
- Worker may be hung. Check: `docker logs omnira-w3-worker`.
- WAHA may have rejected the send. Check WAHA logs or try a shorter text.

### "Did the reply arrive?" → "no"
- If the test shows `sent` but the phone does not display it:
  - Network issue between WAHA and Phone A
  - Phone A's WhatsApp may need a restart or internet reset
  - WAHA may have a session issue (try running P7 again with `--keep`, or check WAHA logs)

## Cleanup

If you used `--keep`, cleanup manually:
```bash
docker rm -f omnira-w3-api omnira-w3-worker omnira-w3-waha
docker exec omnira-postgres psql -U omnira -d postgres -c "DROP DATABASE omnira_w3"
```

## Gate Criteria (P7 PASS)

For production candidacy, this test must pass **6/6 phases**, including human confirmation at phase 7. Specifically:
- ✅ QR generated and scannable
- ✅ Phone A pairs successfully
- ✅ Inbound message from Phone B arrives in Inbox
- ✅ Agent can claim and reply via API
- ✅ Reply is delivered by worker through WAHA
- ✅ Delivery ACK is persisted (status moves from sent → delivered)
- ✅ **Human confirms the reply appeared on Phone A** (visual confirmation)

**If any step fails**, log the issue in `docs/audit/GATE-INBOX-WAHA-LAB.md` under "P7 blocker" and resolve before production. Typical issues are webhook routing (WAHA ↔ API) or network isolation.
