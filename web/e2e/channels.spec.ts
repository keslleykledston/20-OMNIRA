import { test, expect, Page } from '@playwright/test';
import { execFileSync } from 'node:child_process';

// Real stack incl. a throwaway WAHA (real QR). Seeded by scripts/e2e-inbox.sh.
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
  await page.fill('input[placeholder="seu@email.com"]', email);
  await page.fill('input[type="password"]', 'irrelevant');
  await page.click('button:has-text("Entrar")');
  await page.waitForURL('/', { timeout: 10_000 });
}

test('a non-admin sees a permission message instead of the connections', async ({ page }) => {
  await login(page, AGENT.email);
  await page.click('a:has-text("Canais")');
  await expect(page.getByRole('alert')).toContainText('Only tenant administrators');
  await expect(page.getByRole('button', { name: 'Create connection' })).toHaveCount(0);
});

test('admin creates a connection with the risk acknowledgement, pairs via a real QR and stops it', async ({ page }) => {
  await login(page, ADMIN.email);
  await page.click('a:has-text("Canais")');

  const create = page.getByRole('button', { name: 'Create connection' });
  await expect(create).toBeDisabled(); // no acknowledgement, no connection
  await page.getByRole('checkbox').check();
  await create.click();

  const card = page.locator('[data-testid^="connection-"]').first();
  await expect(card).toBeVisible();
  await expect(card.getByTestId('conn-status')).toHaveText('pending');
  const id = (await card.getAttribute('data-testid'))!.replace('connection-', '');
  expect(sql(`SELECT provider||'|'||provider_kind||'|'||status||'|'||risk_acknowledged_by FROM channel_connections WHERE id='${id}'`))
    .toBe(`waha|unofficial|pending|${ADMIN.id}`);
  expect(sql(`SELECT count(*) FROM audit_events WHERE resource_id='${id}' AND action='channel.connection_created' AND actor_id='${ADMIN.id}'`)).toBe('1');
  // The webhook HMAC key is never sent to the browser.
  expect(await page.content()).not.toContain('webhook_hmac_key');

  // Real API wiring of the WAHA webhook: verification must read the connection's encrypted HMAC key
  // (needs a tenant-scoped session). A bad signature is 401; 503 would mean the key was unreadable.
  sql(`UPDATE channel_connections SET status='active' WHERE id='${id}'`);
  const bad = await page.request.post(`${API_URL}/webhooks/v1/whatsapp/waha/${id}`, {
    headers: { 'X-Webhook-Hmac': 'ab'.repeat(64), 'X-Webhook-Hmac-Algorithm': 'sha512', 'Content-Type': 'application/json' },
    data: '{}',
  });
  expect(bad.status()).toBe(401);
  sql(`UPDATE channel_connections SET status='pending' WHERE id='${id}'`);

  await card.getByRole('button', { name: 'Start session' }).click();
  const qr = card.getByAltText('WhatsApp pairing QR code');
  await expect(qr).toBeVisible({ timeout: 40_000 }); // real WAHA renders a real QR
  const rendered = await qr.evaluate((img: HTMLImageElement) => new Promise<{ w: number; h: number }>((resolve, reject) => {
    if (img.complete && img.naturalWidth > 0) return resolve({ w: img.naturalWidth, h: img.naturalHeight });
    img.onload = () => resolve({ w: img.naturalWidth, h: img.naturalHeight });
    img.onerror = () => reject(new Error('QR image failed to decode'));
  }));
  expect(rendered.w).toBeGreaterThan(100); // a decodable PNG, not a broken placeholder
  await expect(card.getByText('Session: needs qr')).toBeVisible();

  await card.getByRole('button', { name: 'Stop' }).click();
  await expect(card.getByTestId('conn-status')).toHaveText('disconnected', { timeout: 15_000 });
  expect(sql(`SELECT status FROM channel_connections WHERE id='${id}'`)).toBe('disconnected');
  expect(sql(`SELECT count(*) FROM audit_events WHERE resource_id='${id}' AND action IN ('channel.session_started','channel.session_stopped')`)).toBe('2');

  // Best-effort cleanup of the WAHA session created for this connection.
  await page.request.delete(`${WAHA_URL}/api/sessions/omnira_${id}`, { headers: { 'X-Api-Key': WAHA_KEY } }).catch(() => undefined);
});
