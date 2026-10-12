import { expect, test, type Page } from '@playwright/test';

// THE REAL SIGN-IN (scripts/e2e-hub-keycloak.sh): the browser goes through the Keycloak login form, the API runs the OIDC code flow, and each kind of person
// lands where the product says they should. Nothing here injects a session: the cookie comes from the API's own callback.
const kc = process.env.E2E_KC_URL ?? '';

async function signInThroughKeycloak(page: Page, email: string, password: string) {
  await page.goto('/login');
  await page.getByRole('button', { name: 'Continuar com SSO' }).click();
  await page.waitForURL(`${kc}/**`);
  await page.locator('#username').fill(email);
  await page.locator('#password').fill(password);
  await page.locator('#kc-login').click();
}

test('quem tem empresa própria entra pelo Keycloak e chega ao aplicativo', async ({ page }) => {
  await signInThroughKeycloak(page, process.env.E2E_MEMBER_EMAIL!, process.env.E2E_MEMBER_PW!);
  await expect(page).not.toHaveURL(/\/login/);
  await expect(page).not.toHaveURL(/no-access/);
  await expect(page.getByRole('link', { name: /^Conversas/ })).toBeVisible();
});

test('quem só pertence ao Hub (sem empresa própria) entra pelo Keycloak e chega às conversas das instâncias liberadas', async ({ page }) => {
  await signInThroughKeycloak(page, process.env.E2E_HUBONLY_EMAIL!, process.env.E2E_HUBONLY_PW!);
  // the sign-in itself lands them on the conversations of the instances the Hub delegated (no company of their own, no "Sem Acesso")
  await expect(page).toHaveURL(/\/inbox/);
  await expect(page).not.toHaveURL(/no-access/);
  const queue = page.getByRole('region', { name: 'Caixa do Hub' });
  await expect(queue).toBeVisible();
  await expect(queue.getByText('Jose Carlos')).toBeVisible();
  await expect(queue.getByText('Maria Souza')).toBeVisible();
});

test('uma senha errada não entra', async ({ page }) => {
  await page.goto('/login');
  await page.getByRole('button', { name: 'Continuar com SSO' }).click();
  await page.waitForURL(`${kc}/**`);
  await page.locator('#username').fill(process.env.E2E_MEMBER_EMAIL!);
  await page.locator('#password').fill('senha-errada-Aa1');
  await page.locator('#kc-login').click();
  await expect(page.locator('#input-error, .kc-feedback-text, [class*="alert"]').first()).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`^${kc}`));
});

test('quem existe no Keycloak mas nunca recebeu nada no OMNIRA entra e para em "Sem Acesso": existir no provedor não dá acesso', async ({ page }) => {
  await signInThroughKeycloak(page, process.env.E2E_GHOST_EMAIL!, process.env.E2E_GHOST_PW!);
  await expect(page).toHaveURL(/no-access/);
  await expect(page.getByRole('heading', { name: 'Sem Acesso' })).toBeVisible();
  // and the application's own routes stay closed to them
  await page.goto('/inbox');
  await expect(page.getByRole('region', { name: 'Caixa do Hub' })).toHaveCount(0);
});

test('convite por e-mail: o administrador autoriza um endereço sem conta e o primeiro login pelo Keycloak entrega as conversas, só as autorizadas', async ({ browser }) => {
  // the Hub administrator signs in for real and authorizes an address that has no account in OMNIRA yet
  const adminCtx = await browser.newContext();
  const admin = await adminCtx.newPage();
  await signInThroughKeycloak(admin, process.env.E2E_ADMIN_EMAIL!, process.env.E2E_ADMIN_PW!);
  await expect(admin).not.toHaveURL(/no-access/);
  const res = await admin.request.post(`/api/v1/hubs/${process.env.E2E_HUB}/access/invitations`, {
    headers: { Origin: process.env.E2E_BASE_URL! }, // a browser always says where a write comes from; the API refuses a cookie write that does not
    data: { email: process.env.E2E_INVITED_EMAIL, access: [{ tenant_id: process.env.E2E_TA, mode: 'reply' }] },
  });
  expect(res.status()).toBe(200);
  expect((await res.json()).status).toBe('pending');
  await adminCtx.close();
  // the invited person signs in for the first time: the choice is applied at that sign-in and the Hub is on their first screen
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  await signInThroughKeycloak(page, process.env.E2E_INVITED_EMAIL!, process.env.E2E_INVITED_PW!);
  await expect(page).toHaveURL(/\/inbox/);
  const queue = page.getByRole('region', { name: 'Caixa do Hub' });
  await expect(queue.getByText('Jose Carlos')).toBeVisible(); // company A, authorized
  await expect(queue.getByText('Maria Souza')).toHaveCount(0); // company B, never authorized to this person
  await ctx.close();
});

test('um site de fora não consegue escrever com o cookie da pessoa (Origin de outro site ou ausente é recusado)', async ({ browser }) => {
  const ctx = await browser.newContext();
  const admin = await ctx.newPage();
  await signInThroughKeycloak(admin, process.env.E2E_ADMIN_EMAIL!, process.env.E2E_ADMIN_PW!);
  const path = `/api/v1/hubs/${process.env.E2E_HUB}/access/invitations`;
  const body = { email: 'csrf-alvo@e2e.test', access: [] };
  expect((await admin.request.post(path, { headers: { Origin: 'https://evil.example' }, data: body })).status()).toBe(403);
  expect((await admin.request.post(path, { data: body })).status()).toBe(403); // neither Origin nor Referer
  expect((await admin.request.post(path, { headers: { Origin: process.env.E2E_BASE_URL! }, data: body })).status()).not.toBe(403);
  await ctx.close();
});
