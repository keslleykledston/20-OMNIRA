import { test, expect, Page } from '@playwright/test';
import { execFileSync } from 'node:child_process';

// Real stack incl. a throwaway WAHA (real QR). Seeded by scripts/e2e-inbox.sh.
// This spec is the sole product E2E for Channels (DESIGN.5-A retired the legacy
// /integrations shell + its own spec) — ChannelsPage + WahaWizardPage +
// features/channels/*. Same real backend/provider fixture mechanism, no mocked
// QR, no fake state — the id/status assertions read the real database.
const DB = process.env.E2E_DB || 'omnira_e2e';
const PG = process.env.E2E_PG_CONTAINER || 'omnira-postgres';
const WAHA_URL = process.env.E2E_WAHA_URL || 'http://127.0.0.1:23200';
const WAHA_KEY = process.env.E2E_WAHA_KEY || 'e2ekey';
const API_URL = process.env.E2E_API_URL || 'http://127.0.0.1:28961';
const ADMIN = { email: 'admin@omnira.local', id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa' };
const AGENT = { email: 'test@omnira.local' };

function sql(query: string): string {
  return execFileSync('docker', ['exec', PG, 'psql', '-U', 'omnira', '-d', DB, '-tA', '-c', query], { encoding: 'utf8' }).trim();
}

async function login(page: Page, email: string) {
  await page.goto('/login');
  await page.getByLabel('E-mail').fill(email);
  await page.getByRole('button', { name: /Entrar/ }).click();
  await page.waitForURL('/inbox', { timeout: 10_000 });
}

// Runs regardless of where the test fails, so a failed run never leaves a
// residual row that would break another test in this file that queries the
// same admin id's risk_acknowledged_by count.
test.afterEach(() => {
  sql(`DELETE FROM channel_connections WHERE risk_acknowledged_by='${ADMIN.id}'`);
});

// Visiting /channels as admin renders every connection through
// LiveChannelConnectionCard (useLiveConnection), which reconciles each row
// against its real WAHA session — this is real ChannelsPage behavior, not a
// test artifact. The seeded fixture connection has no real WAHA session, so
// it gets demoted from 'active' to 'pending' the first time any test here
// renders the list as admin. web/e2e/inbox.spec.ts's fixture conversation
// uses this exact connection and requires it 'active' to accept a reply
// (internal/messages/application/send.go: ErrChannelUnavailable otherwise) —
// this restores what visiting /channels disturbs, for every spec that shares
// the tenant and runs after this file (found via TEST.2-A: without this,
// inbox.spec.ts's send test fails whenever a channels-page test runs first).
const FIXTURE_CONNECTION = 'c0000000-0000-0000-0000-00000000c001';
test.afterAll(() => {
  sql(`UPDATE channel_connections SET status='active' WHERE id='${FIXTURE_CONNECTION}'`);
});

// DESIGN.5-A: /integrations was the legacy duplicate Channels UI, now retired.
// The route is kept only as a compatibility redirect for old bookmarks/links.
test('/integrations redirects to the canonical /channels page', async ({ page }) => {
  await login(page, ADMIN.email);
  await page.goto('/integrations');
  await expect(page).toHaveURL(/\/channels$/);
  await expect(page.getByRole('heading', { name: 'Canais' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Adicionar canal' })).toBeVisible();
});

// Migrated from the retired legacy spec (DESIGN.5-A): non-admin authorization is
// a real backend invariant (403 on providers/connections), not legacy-UI-specific
// — integrationErrorMessage() maps 403 to the same "Somente administradores..."
// text on both pages, ChannelsPage just renders it inside the generic ErrorState
// instead of a page-level banner.
test('a non-admin sees a permission message instead of the connections', async ({ page }) => {
  await login(page, AGENT.email);
  await page.goto('/channels');
  await expect(page.getByText('Somente administradores do tenant gerenciam integrações.')).toBeVisible();
  await expect(page.getByRole('button', { name: /Ações de/ })).toHaveCount(0);
});

// DESIGN.4-0.1: WahaConnectionService.List (backend) never reconciles live WAHA
// session state — only Get(id) does. ChannelsPage now reconciles each row via
// LiveChannelConnectionCard (useLiveConnection, same polling contract already
// used by the wizard), so a card freshly created while awaiting QR correctly
// shows "Aguardando QR" and the "Detalhes" resume action, instead of the
// stale "Desconectado" the raw list() response would otherwise render.
test('admin connects WhatsApp via /channels, pairs via a real QR, and disconnects it', async ({ page }) => {
  await login(page, ADMIN.email);
  await page.goto('/channels');
  await expect(page.getByRole('heading', { name: 'Canais' })).toBeVisible();

  // Step 1: pick the WAHA provider from the real descriptor list.
  await page.getByRole('button', { name: 'Adicionar canal' }).click();
  const addDialog = page.getByRole('dialog', { name: 'Adicionar canal' });
  await addDialog.getByRole('button', { name: /WhatsApp \(não oficial\)/ }).click();
  await expect(page).toHaveURL(/\/channels\/whatsapp\/new\?provider=waha/);

  // Step 2: WAHA has no configurable inputs — only the risk acknowledgement gates creation.
  const continueBtn = page.getByRole('button', { name: 'Continuar' });
  await expect(continueBtn).toBeDisabled();
  await page.getByRole('checkbox').check();
  await continueBtn.click();

  // The wizard's ConfigureStep creates the connection and starts the session
  // automatically (real API: POST .../connections then POST .../session/start).
  await expect.poll(() => sql(`SELECT count(*) FROM channel_connections WHERE risk_acknowledged_by='${ADMIN.id}'`)).toBe('1');
  const id = sql(`SELECT id FROM channel_connections WHERE risk_acknowledged_by='${ADMIN.id}'`);
  expect(sql(`SELECT provider||'|'||provider_kind||'|'||risk_acknowledged_by FROM channel_connections WHERE id='${id}'`))
    .toBe(`waha|unofficial|${ADMIN.id}`);
  expect(sql(`SELECT count(*) FROM audit_events WHERE resource_id='${id}' AND action='channel.connection_created' AND actor_id='${ADMIN.id}'`)).toBe('1');
  // The webhook HMAC key is never sent to the browser.
  expect(await page.content()).not.toContain('webhook_hmac_key');

  // Real API wiring of the WAHA webhook: verification must read the connection's encrypted
  // HMAC key (needs a tenant-scoped session). A bad signature is 401; 503 would mean the
  // key was unreadable; the endpoint requires an active connection first (409 otherwise) —
  // same as the legacy spec, this is backend behavior independent of which page is open.
  sql(`UPDATE channel_connections SET status='active' WHERE id='${id}'`);
  const bad = await page.request.post(`${API_URL}/webhooks/v1/whatsapp/waha/${id}`, {
    headers: { 'X-Webhook-Hmac': 'ab'.repeat(64), 'X-Webhook-Hmac-Algorithm': 'sha512', 'Content-Type': 'application/json' },
    data: '{}',
  });
  expect(bad.status()).toBe(401);
  sql(`UPDATE channel_connections SET status='pending' WHERE id='${id}'`);

  // Step 3: the wizard polls the real connection status and a real QR from the throwaway WAHA.
  await expect(page.getByTestId('wizard-status')).toBeVisible({ timeout: 15_000 });
  const qr = page.getByTestId('qr-image');
  await expect(qr).toBeVisible({ timeout: 40_000 }); // real WAHA renders a real QR
  const rendered = await qr.evaluate((img: HTMLImageElement) => new Promise<{ w: number; h: number }>((resolve, reject) => {
    if (img.complete && img.naturalWidth > 0) return resolve({ w: img.naturalWidth, h: img.naturalHeight });
    img.onload = () => resolve({ w: img.naturalWidth, h: img.naturalHeight });
    img.onerror = () => reject(new Error('QR image failed to decode'));
  }));
  expect(rendered.w).toBeGreaterThan(100); // a decodable PNG, not a broken placeholder

  // Abandon the wizard (does not stop the session — that's a separate, explicit
  // action from the list, matching the real product's actual affordance).
  await page.getByRole('button', { name: 'Voltar' }).click();
  const cancelDialog = page.getByRole('dialog', { name: 'Cancelar conexão' });
  await cancelDialog.getByRole('button', { name: 'Cancelar conexão' }).click();
  await expect(page).toHaveURL(/\/channels$/);

  // Step 4: locate the created connection's card on the real list by its visible
  // truncated session id (no test id exists on ChannelConnectionCard; using only
  // what the page already renders, no product code changed for this spec), then
  // walk up to the card container itself (Card primitive: rounded-card).
  const shortId = id.slice(0, 8);
  const sessionValue = page.getByText(`${shortId}…`, { exact: true });
  const card = sessionValue.locator('xpath=ancestor::div[contains(@class, "rounded-card")][1]');

  // The live reconciliation fix: the card must reflect the real qr_required
  // state (not the stale "Desconectado" list() alone would produce), and the
  // resume action must be available.
  await expect(card.getByText('Aguardando QR')).toBeVisible({ timeout: 10_000 });
  await expect(card.getByRole('button', { name: 'Detalhes' })).toBeVisible();

  // Disconnect through the real list action (DropdownMenu → Desconectar → confirm),
  // exercising the same stop() lifecycle the legacy spec proves via its "Parar" button.
  await card.getByRole('button', { name: /Ações de/ }).click();
  await page.getByRole('menuitem', { name: 'Desconectar' }).click();
  await page.getByRole('dialog', { name: 'Desconectar canal' }).getByRole('button', { name: 'Desconectar' }).click();
  await expect(card.getByText('Desconectado')).toBeVisible({ timeout: 15_000 });
  expect(sql(`SELECT status FROM channel_connections WHERE id='${id}'`)).toBe('disconnected');
  expect(sql(`SELECT count(*) FROM audit_events WHERE resource_id='${id}' AND action IN ('channel.session_started','channel.session_stopped')`)).toBe('2');

  // Best-effort cleanup of the WAHA session created for this connection.
  await page.request.delete(`${WAHA_URL}/api/sessions/omnira_${id}`, { headers: { 'X-Api-Key': WAHA_KEY } }).catch(() => undefined);
});
