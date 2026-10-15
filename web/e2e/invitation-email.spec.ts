import { test, expect, Page } from '@playwright/test';
import { startSmtpSink, SmtpSink } from './smtp-sink';

// Real stack (scripts/e2e-inbox.sh): the API is configured with SMTP pointing at this spec's sink.
const SMTP_PORT = Number(process.env.E2E_SMTP_PORT || 12525);
const WEB = process.env.E2E_BASE_URL || 'http://127.0.0.1:4173';
const API = process.env.E2E_API_URL || 'http://127.0.0.1:28961';
const ADMIN = 'admin@omnira.local';
const INVITEE = 'convidado-e2e@omnira.test';

test.describe.configure({ mode: 'serial' });

let sink: SmtpSink;
test.beforeAll(async () => {
  sink = await startSmtpSink(SMTP_PORT);
});
test.afterAll(async () => {
  await sink?.close();
});

async function login(page: Page, email: string) {
  await page.goto('/login');
  await page.getByLabel('E-mail').fill(email);
  await page.getByRole('button', { name: /Entrar/ }).click();
  await page.waitForURL('/inbox', { timeout: 10_000 });
}

function linkOf(raw: string): string {
  const m = raw.match(/https?:\/\/[^\s"<>]+\/invite\/[A-Za-z0-9_-]+/);
  if (!m) throw new Error('no invite link in the e-mail');
  return m[0];
}

// GET /invitations/{token}/status as the admin: a live token owned by someone else is
// "wrong_identity"; a dead/unknown token is "not_found".
async function tokenState(page: Page, link: string): Promise<string> {
  const token = link.split('/invite/')[1];
  const login = await page.request.post(`${API}/api/v1/auth/dev/login`, { data: { email: ADMIN } });
  const { token: jwt } = await login.json();
  const res = await page.request.get(`${API}/api/v1/invitations/${token}/status`, { headers: { Authorization: `Bearer ${jwt}` } });
  return (await res.json()).status;
}

test('admin invites by e-mail, sees the delivery, resends, and the old link stops working', async ({ page }) => {
  await login(page, ADMIN);
  await page.goto('/settings/people');

  await page.getByRole('button', { name: /Adicionar pessoa/ }).click();
  const dialog = page.getByRole('dialog', { name: 'Adicionar pessoa' });
  await dialog.getByLabel('E-mail').fill(INVITEE);
  await dialog.getByRole('button', { name: 'Enviar convite' }).click();
  await expect(page.getByText(`Convite enviado para ${INVITEE}.`)).toBeVisible();

  const [first] = await sink.waitForCount(1);
  expect(first.to).toBe(INVITEE);
  expect(first.raw).toContain('E2E Co');
  const link1 = linkOf(first.raw);
  expect(link1.startsWith(`${WEB}/invite/`)).toBe(true);
  expect(await tokenState(page, link1)).toBe('wrong_identity'); // alive, addressed to someone else

  const row = page.getByRole('button', { name: `Abrir a ficha de ${INVITEE}` });
  await expect(row).toContainText('Convite enviado');
  await expect(row).not.toContainText('não entregue');

  await row.click();
  const sheet = page.getByRole('dialog', { name: `Ficha de ${INVITEE}` });
  await sheet.getByRole('button', { name: 'Reenviar convite' }).click();
  await expect(sheet.getByText('Convite reenviado.')).toBeVisible();

  const mails = await sink.waitForCount(2);
  const link2 = linkOf(mails[1].raw);
  expect(link2).not.toBe(link1);
  expect(await tokenState(page, link1)).toBe('not_found'); // the previous link is dead
  expect(await tokenState(page, link2)).toBe('wrong_identity'); // the new one is alive
});
