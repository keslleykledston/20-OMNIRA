import { test, expect, Page } from '@playwright/test';
import { execFileSync } from 'node:child_process';

// Real stack: backend API + worker + NATS + Postgres. Data is seeded by scripts/e2e-inbox.sh.
const DB = process.env.E2E_DB || 'omnira_e2e';
const PG = process.env.E2E_PG_CONTAINER || 'omnira-postgres';
const TENANT = '11111111-1111-1111-1111-111111111111';
const CONV = 'd0d0d0d0-0000-0000-0000-000000000001';
const FOREIGN_CONV = 'd0d0d0d0-0000-0000-0000-0000000000ff';
const AGENT = { email: 'test@omnira.local', id: '22222222-2222-2222-2222-222222222222' };

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

test.beforeEach(() => {
  // Reset the mutable state of the fixture conversation.
  sql(`UPDATE conversations SET assigned_to_user_id=NULL WHERE id='${CONV}';
       DELETE FROM messages WHERE conversation_id='${CONV}' AND body <> 'Olá, preciso de ajuda com meu pedido';`);
});

test('login uses the real backend and stores the tenant', async ({ page }) => {
  await login(page, AGENT.email);
  const session = await page.evaluate(() => ({ token: localStorage.getItem('token'), tenant: localStorage.getItem('tenantId') }));
  expect(session.tenant).toBe(TENANT);
  expect(session.token?.split('.')).toHaveLength(3);
  expect(session.token).not.toContain('mock-signature'); // a real RS256 token from the backend
});

test('unknown email is rejected by the backend', async ({ page }) => {
  await page.goto('/login');
  await page.fill('input[placeholder="seu@email.com"]', 'nobody@example.com');
  await page.fill('input[type="password"]', 'x');
  await page.click('button:has-text("Entrar")');
  await expect(page.locator('text=/não encontrado|not found|email/i').first()).toBeVisible();
  expect(new URL(page.url()).pathname).toBe('/login');
});

test('inbox lists the conversation, opens it and shows header + messages', async ({ page }) => {
  await login(page, AGENT.email);
  await page.click('a:has-text("Inbox")');
  await expect(page.getByText('Maria Souza')).toBeVisible();
  await page.getByText('Maria Souza').click();
  await expect(page).toHaveURL(new RegExp(`/inbox/${CONV}$`));
  await expect(page.getByRole('heading', { name: 'Maria Souza' })).toBeVisible(); // header from GET conversation
  await expect(page.getByText('+5511988887777')).toBeVisible();
  await expect(page.getByText('Olá, preciso de ajuda com meu pedido')).toBeVisible();
});

test('claim, reply and release through the UI hit the real API and persist', async ({ page }) => {
  await login(page, AGENT.email);
  await page.goto(`/inbox/${CONV}`);
  await page.getByRole('button', { name: 'Assign to Me' }).click();
  await expect(page.getByText(/Assigned to 22222222/)).toBeVisible();
  expect(sql(`SELECT assigned_to_user_id FROM conversations WHERE id='${CONV}'`)).toBe(AGENT.id);

  await page.getByPlaceholder('Type a message...').fill('Olá Maria, já estou verificando');
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page.getByText('Olá Maria, já estou verificando')).toBeVisible();
  await expect(page.locator('.message-outbound .status').first()).toHaveText('queued');
  expect(sql(`SELECT status||'|'||sent_by_user_id FROM messages WHERE body='Olá Maria, já estou verificando'`)).toBe(`queued|${AGENT.id}`);

  await page.reload(); // state comes from the API, not local memory
  await expect(page.getByText('Olá Maria, já estou verificando')).toBeVisible();
  await expect(page.getByText(/Assigned to 22222222/)).toBeVisible();

  await page.getByRole('button', { name: 'Release' }).click();
  await expect(page.getByRole('button', { name: 'Assign to Me' })).toBeVisible();
  expect(sql(`SELECT count(*) FROM conversations WHERE id='${CONV}' AND assigned_to_user_id IS NULL`)).toBe('1');
});

test('sending before claiming shows the actionable error and keeps the text', async ({ page }) => {
  await login(page, AGENT.email);
  await page.goto(`/inbox/${CONV}`);
  await page.getByPlaceholder('Type a message...').fill('não deveria sair');
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page.getByRole('alert')).toContainText('Assign this conversation to yourself before replying');
  await expect(page.getByPlaceholder('Type a message...')).toHaveValue('não deveria sair');
  expect(sql(`SELECT count(*) FROM messages WHERE body='não deveria sair'`)).toBe('0');
});

test('realtime: inbound message and status change appear without reload', async ({ page }) => {
  await login(page, AGENT.email);
  await page.goto(`/inbox/${CONV}`);
  await expect(page.getByText('Olá, preciso de ajuda com meu pedido')).toBeVisible();
  // A new inbound message is written to the database (as the WAHA webhook intake would):
  // trigger -> Outbox -> worker publisher -> NATS -> SSE -> UI refetch.
  sql(`INSERT INTO messages(tenant_id,conversation_id,channel_connection_id,direction,message_type,body,provider_message_id,status)
       SELECT tenant_id,id,channel_connection_id,'inbound','text','mensagem em tempo real','e2e-rt-1','received' FROM conversations WHERE id='${CONV}'`);
  await expect(page.getByText('mensagem em tempo real')).toBeVisible({ timeout: 15_000 });

  // Delivery status change of an outbound message shows live too.
  sql(`INSERT INTO messages(tenant_id,conversation_id,channel_connection_id,direction,message_type,body,provider_message_id,status)
       SELECT tenant_id,id,channel_connection_id,'outbound','text','resposta rastreada','e2e-out-1','sent' FROM conversations WHERE id='${CONV}'`);
  const row = page.locator('.message', { hasText: 'resposta rastreada' });
  await expect(row.locator('.status')).toHaveText('sent', { timeout: 15_000 });
  sql(`UPDATE messages SET status='delivered' WHERE provider_message_id='e2e-out-1'`);
  await expect(row.locator('.status')).toHaveText('delivered', { timeout: 15_000 });
});

test('realtime: the inbox list picks up a new conversation', async ({ page }) => {
  await login(page, AGENT.email);
  await page.goto('/inbox');
  await expect(page.getByText('Maria Souza')).toBeVisible();
  sql(`WITH c AS (INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES (gen_random_uuid(),'${TENANT}','Novo Contato','+5511977776666') ON CONFLICT DO NOTHING RETURNING id)
       INSERT INTO conversations(id,tenant_id,contact_id,status) SELECT gen_random_uuid(),'${TENANT}',id,'open' FROM c`);
  await expect(page.getByText('Novo Contato')).toBeVisible({ timeout: 15_000 });
  sql(`DELETE FROM conversations WHERE contact_id IN (SELECT id FROM contacts WHERE display_name='Novo Contato'); DELETE FROM contacts WHERE display_name='Novo Contato'`);
});

test('a conversation id from another tenant reveals nothing', async ({ page }) => {
  await login(page, AGENT.email);
  await page.goto(`/inbox/${FOREIGN_CONV}`);
  await expect(page.getByText('Bruno Outro')).toHaveCount(0);
  await expect(page.getByText('mensagem secreta do outro tenant')).toHaveCount(0);
});

test('an expired/invalid token sends the user back to login', async ({ page }) => {
  await login(page, AGENT.email);
  await page.evaluate(() => localStorage.setItem('token', 'not.a.jwt'));
  await page.goto('/inbox');
  await page.waitForURL('**/login', { timeout: 10_000 });
  expect(await page.evaluate(() => localStorage.getItem('token'))).toBeNull();
});
